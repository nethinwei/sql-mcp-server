package mcpserver

import (
	"container/heap"
	"context"
	"fmt"
	"iter"
	"slices"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// eventStoreMaxBytes bounds the events kept, over all sessions, for clients
// that reconnect to a stream.
const eventStoreMaxBytes = 10 << 20

// fairEventStore keeps the events of every stream so a client can resume it,
// like mcp.MemoryEventStore, but when over its bound it drops the oldest
// events of the session holding the most: one session's large results never
// cost the other sessions their ability to resume. Sessions, and each
// session's streams, are max-heaps by bytes held, so making room costs
// O(log n) per event dropped.
type fairEventStore struct {
	mu       sync.Mutex
	maxBytes int
	nBytes   int
	sessions map[string]*sessionEvents
	bySize   bySize[*sessionEvents]
}

type sessionEvents struct {
	held
	streams map[string]*streamEvents
	bySize  bySize[*streamEvents]
}

// streamEvents are a stream's events from index first on. A stream stays
// when all its events are dropped: first keeps its indexes going.
type streamEvents struct {
	held
	first int
	data  [][]byte
}

// held is the bytes something holds and its place in a bySize heap.
type held struct {
	size  int
	index int
}

func (h *held) holding() *held { return h }

// bySize is a max-heap by bytes held (container/heap).
type bySize[T interface{ holding() *held }] []T

func (h bySize[T]) Len() int           { return len(h) }
func (h bySize[T]) Less(i, j int) bool { return h[i].holding().size > h[j].holding().size }
func (h bySize[T]) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].holding().index, h[j].holding().index = i, j
}
func (h *bySize[T]) Push(x any) {
	x.(T).holding().index = len(*h)
	*h = append(*h, x.(T))
}
func (h *bySize[T]) Pop() any {
	old := *h
	x := old[len(old)-1]
	var zero T
	old[len(old)-1] = zero
	*h = old[:len(old)-1]
	return x
}

// grow adds n (negative to shrink) to what x holds and restores h.
func grow[T interface{ holding() *held }](h *bySize[T], x T, n int) {
	x.holding().size += n
	heap.Fix(h, x.holding().index)
}

func newFairEventStore(maxBytes int) *fairEventStore {
	return &fairEventStore{maxBytes: maxBytes, sessions: map[string]*sessionEvents{}}
}

func (s *fairEventStore) stream(sessionID, streamID string) (*sessionEvents, *streamEvents) {
	session, ok := s.sessions[sessionID]
	if !ok {
		session = &sessionEvents{streams: map[string]*streamEvents{}}
		s.sessions[sessionID] = session
		heap.Push(&s.bySize, session)
	}
	stream, ok := session.streams[streamID]
	if !ok {
		stream = &streamEvents{}
		session.streams[streamID] = stream
		heap.Push(&session.bySize, stream)
	}
	return session, stream
}

// Open implements mcp.EventStore.
func (s *fairEventStore) Open(_ context.Context, sessionID, streamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stream(sessionID, streamID)
	return nil
}

// Append implements mcp.EventStore. Room is made before data is added, so
// the latest event is always kept, even one larger than the bound.
func (s *fairEventStore) Append(_ context.Context, sessionID, streamID string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, stream := s.stream(sessionID, streamID)
	for s.nBytes+len(data) > s.maxBytes {
		if !s.dropOldestOfLargest() {
			break
		}
	}
	stream.data = append(stream.data, data)
	grow(&session.bySize, stream, len(data))
	grow(&s.bySize, session, len(data))
	s.nBytes += len(data)
	return nil
}

// dropOldestOfLargest drops the first event of the largest stream of the
// session holding the most bytes; false when nothing is held.
func (s *fairEventStore) dropOldestOfLargest() bool {
	if len(s.bySize) == 0 || s.bySize[0].size == 0 {
		return false
	}
	session := s.bySize[0]
	stream := session.bySize[0]
	n := len(stream.data[0])
	stream.data[0] = nil
	stream.data = stream.data[1:]
	if len(stream.data) == 0 {
		stream.data = nil // release the backing array
	}
	stream.first++
	grow(&session.bySize, stream, -n)
	grow(&s.bySize, session, -n)
	s.nBytes -= n
	return true
}

// After implements mcp.EventStore.
func (s *fairEventStore) After(_ context.Context, sessionID, streamID string, index int) iter.Seq2[[]byte, error] {
	events, err := s.after(sessionID, streamID, index)
	return func(yield func([]byte, error) bool) {
		if err != nil {
			yield(nil, err)
			return
		}
		for _, d := range events {
			if !yield(d, nil) {
				return
			}
		}
	}
}

func (s *fairEventStore) after(sessionID, streamID string, index int) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("event store: unknown session %q", sessionID)
	}
	stream, ok := session.streams[streamID]
	if !ok {
		return nil, fmt.Errorf("event store: unknown stream %q in session %q", streamID, sessionID)
	}
	start := index + 1
	if stream.first > start {
		return nil, fmt.Errorf("event store: index %d of stream %q: %w", index, streamID, mcp.ErrEventsPurged)
	}
	return slices.Clone(stream.data[min(start-stream.first, len(stream.data)):]), nil
}

// SessionClosed implements mcp.EventStore.
func (s *fairEventStore) SessionClosed(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[sessionID]; ok {
		s.nBytes -= session.size
		heap.Remove(&s.bySize, session.index)
		delete(s.sessions, sessionID)
	}
	return nil
}
