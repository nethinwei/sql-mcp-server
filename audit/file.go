package audit

import (
	"encoding/json"
	"os"
	"sync"
)

// FileSink is a long-lived JSON-lines audit sink.
type FileSink struct {
	mu     sync.Mutex
	file   *os.File
	closed bool
}

// OpenFileSink opens path with owner-only permissions.
func OpenFileSink(path string) (*FileSink, error) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileSink{file: file}, nil
}

// Record appends one event.
func (s *FileSink) Record(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSinkClosed
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = s.file.Write(encoded)
	return err
}

// Close flushes and closes the file.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if err := s.file.Sync(); err != nil {
		_ = s.file.Close()
		return err
	}
	return s.file.Close()
}
