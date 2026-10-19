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

// What the store holds besides the events' data, counted against its bound
// with the data: each event, and each stream (one per request). Without them
// empty events (the SDK's priming events) and the streams of a long session
// would grow unbounded, holding no bytes to evict. Approximations of the
// memory they take.
const (
	eventCost  = 32
	streamCost = 256
)

// fairEventStore keeps the events of every stream so a client can resume it,
// like mcp.MemoryEventStore, but when over its bound it drops the oldest
// events of the session holding the most: one session's large results never
// cost the other sessions their ability to resume. Sessions, and each
// session's streams, are max-heaps by bytes held, so making room costs
// O(log n) per event dropped. A stream whose events were all dropped keeps
// its entry, so that its indexes go on, until room is needed again; one
// dropped then is lost (see streamEvents.lost).
type fairEventStore struct {
	mu       sync.Mutex
	maxBytes int
	nBytes   int // the events' data and what the store holds besides (see eventCost)
	sessions map[string]*sessionEvents
	bySize   bySize[*sessionEvents]
	// eventCost and streamCost are the costs of the same name (zero in tests
	// of the bytes alone).
	eventCost, streamCost int
}

type sessionEvents struct {
	held
	id      string
	streams map[string]*streamEvents
	bySize  bySize[*streamEvents]
}

// streamEvents are a stream's events from index first on. A stream stays
// when all its events are dropped: first keeps its indexes going. A stream
// appended to after its entry was dropped is lost: where its indexes stand
// is unknown, so it keeps no event and every replay of it is refused as
// purged.
type streamEvents struct {
	held
	id    string
	first int
	data  [][]byte
	lost  bool
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
	return &fairEventStore{maxBytes: maxBytes, sessions: map[string]*sessionEvents{}, eventCost: eventCost,
		streamCost: streamCost}
}

// lookup returns the entry of a stream, nil when it has none.
func (s *fairEventStore) lookup(sessionID, streamID string) (*sessionEvents, *streamEvents) {
	if session, ok := s.sessions[sessionID]; ok {
		return session, session.streams[streamID]
	}
	return nil, nil
}

// add makes room for, and adds, the entry of a stream that has none.
func (s *fairEventStore) add(sessionID, streamID string, lost bool) {
	s.makeRoom(s.streamCost, nil)
	session, ok := s.sessions[sessionID]
	if !ok {
		session = &sessionEvents{id: sessionID, streams: map[string]*streamEvents{}}
		s.sessions[sessionID] = session
		heap.Push(&s.bySize, session)
	}
	stream := &streamEvents{id: streamID, lost: lost}
	session.streams[streamID] = stream
	heap.Push(&session.bySize, stream)
	s.hold(session, stream, s.streamCost)
}

// hold adds n (negative to release) to what stream, of session, holds.
func (s *fairEventStore) hold(session *sessionEvents, stream *streamEvents, n int) {
	grow(&session.bySize, stream, n)
	grow(&s.bySize, session, n)
	s.nBytes += n
}

// makeRoom drops what the store holds until n more fit in its bound, or
// nothing but keep's entry is left to drop.
func (s *fairEventStore) makeRoom(n int, keep *streamEvents) {
	for s.nBytes+n > s.maxBytes {
		if !s.dropOldestOfLargest(keep) {
			return
		}
	}
}

// Open implements mcp.EventStore. The SDK opens every stream before
// appending to it.
func (s *fairEventStore) Open(_ context.Context, sessionID, streamID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, stream := s.lookup(sessionID, streamID); stream == nil {
		s.add(sessionID, streamID, false)
	}
	return nil
}

// Append implements mcp.EventStore. Room is made before data is added, never
// by dropping the entry of its stream, so the latest event is always kept,
// even one larger than the bound. A stream opened and then dropped is lost.
func (s *fairEventStore) Append(_ context.Context, sessionID, streamID string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, stream := s.lookup(sessionID, streamID)
	if stream == nil {
		s.add(sessionID, streamID, true)
		return nil
	}
	if stream.lost {
		return nil
	}
	s.makeRoom(s.eventCost+len(data), stream)
	stream.data = append(stream.data, data)
	s.hold(session, stream, s.eventCost+len(data))
	return nil
}

// dropOldestOfLargest drops the first event of the largest stream of the
// session holding the most, or that stream's entry when it has no event
// (and the session's when it was its last), unless the stream is keep; false
// when it dropped nothing.
func (s *fairEventStore) dropOldestOfLargest(keep *streamEvents) bool {
	if len(s.bySize) == 0 || s.bySize[0].size == 0 {
		return false
	}
	session := s.bySize[0]
	stream := session.bySize[0]
	if len(stream.data) == 0 {
		if stream == keep {
			return false
		}
		s.dropStream(session, stream)
		return true
	}
	n := s.eventCost + len(stream.data[0])
	stream.data[0] = nil
	stream.data = stream.data[1:]
	if len(stream.data) == 0 {
		stream.data = nil // release the backing array
	}
	stream.first++
	s.hold(session, stream, -n)
	return true
}

// dropStream drops the entry of stream, which holds no event, and session's
// when it was its last.
func (s *fairEventStore) dropStream(session *sessionEvents, stream *streamEvents) {
	s.hold(session, stream, -stream.size)
	heap.Remove(&session.bySize, stream.index)
	delete(session.streams, stream.id)
	if len(session.streams) == 0 {
		heap.Remove(&s.bySize, session.index)
		delete(s.sessions, session.id)
	}
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
	if stream.lost || stream.first > start {
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
