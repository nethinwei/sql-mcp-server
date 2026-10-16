package mcpserver

import (
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
// cost the other sessions their ability to resume.
type fairEventStore struct {
	mu       sync.Mutex
	maxBytes int
	nBytes   int
	sessions map[string]*sessionEvents
}

type sessionEvents struct {
	size    int
	streams map[string]*streamEvents
}

// streamEvents are a stream's events from index first on.
type streamEvents struct {
	first int
	data  [][]byte
}

func newFairEventStore(maxBytes int) *fairEventStore {
	return &fairEventStore{maxBytes: maxBytes, sessions: map[string]*sessionEvents{}}
}

func (s *fairEventStore) stream(sessionID, streamID string) *streamEvents {
	session, ok := s.sessions[sessionID]
	if !ok {
		session = &sessionEvents{streams: map[string]*streamEvents{}}
		s.sessions[sessionID] = session
	}
	stream, ok := session.streams[streamID]
	if !ok {
		stream = &streamEvents{}
		session.streams[streamID] = stream
	}
	return stream
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
	stream := s.stream(sessionID, streamID)
	for s.nBytes+len(data) > s.maxBytes {
		if !s.dropOldestOfLargest() {
			break
		}
	}
	stream.data = append(stream.data, data)
	s.sessions[sessionID].size += len(data)
	s.nBytes += len(data)
	return nil
}

// dropOldestOfLargest drops the first event of the largest stream of the
// session holding the most bytes; false when nothing is held.
func (s *fairEventStore) dropOldestOfLargest() bool {
	var session *sessionEvents
	for _, candidate := range s.sessions {
		if candidate.size > 0 && (session == nil || candidate.size > session.size) {
			session = candidate
		}
	}
	if session == nil {
		return false
	}
	var stream *streamEvents
	streamSize := -1
	for _, candidate := range session.streams {
		size := 0
		for _, d := range candidate.data {
			size += len(d)
		}
		if len(candidate.data) > 0 && size > streamSize {
			stream, streamSize = candidate, size
		}
	}
	n := len(stream.data[0])
	stream.data[0] = nil
	stream.data = stream.data[1:]
	stream.first++
	session.size -= n
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
		delete(s.sessions, sessionID)
	}
	return nil
}
