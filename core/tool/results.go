package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/nethinwei/sql-mcp-server/core/budget"
)

// ResultMemory bounds the bytes of the results the process holds at once,
// across calls: each call's rows hold their share from when they are read
// until the call ends. It belongs to the process and outlives
// configurations. A nil ResultMemory bounds nothing.
type ResultMemory struct {
	mu       sync.Mutex
	used     int64
	capacity int64 // 0: no bound
}

// NewResultMemory returns a bound of capacity bytes (0: none).
func NewResultMemory(capacity int64) *ResultMemory {
	return &ResultMemory{capacity: capacity}
}

// SetCapacity changes the bound for the reservations that follow.
func (r *ResultMemory) SetCapacity(capacity int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.capacity = capacity
}

// InUse returns the bytes held.
func (r *ResultMemory) InUse() int64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.used
}

// reserve holds n bytes; past the capacity it refuses them.
func (r *ResultMemory) reserve(n int64) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.capacity > 0 && r.used+n > r.capacity {
		return false
	}
	r.used += n
	return true
}

func (r *ResultMemory) release(n int64) {
	if r == nil || n == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.used -= n
}

// resultMeter counts what one call returns as its rows are read (see
// statement.query) and stops the call at the first row past its limits: the
// rows of its budget and, for a read, the bytes of cost.maxBytes and of its
// budget. Their bytes are held in the process's ResultMemory until the call
// ends. The size of a row is its JSON encoding, which the result is encoded
// to; a row the driver already read whole is the most a call reads past its
// limit.
type resultMeter struct {
	mu                sync.Mutex
	rows, bytes, held int64
	maxRows, maxBytes int64
	memory            *ResultMemory
	// write: a write's rows are read after it took effect, so past its rows
	// or the memory they are cut (errResultCut) rather than refused (failing
	// the call would invite a duplicate retry, see finalizeToolResult).
	write bool
	// closed: the call ended. An execution it shared (singleflight) may
	// still read rows for the calls waiting on it; they are no longer held
	// for this call, which released what it held.
	closed bool
}

// meterCall gives tc's call a meter unless it has one, and returns the
// function ending the call's metering (nothing when it had one).
func meterCall(tc Context, readOnly bool) (Context, func()) {
	if tc.results != nil {
		return tc, func() {}
	}
	m := &resultMeter{maxRows: tc.BudgetLimits.MaxReturnedRows, memory: tc.ResultMemory, write: !readOnly}
	if readOnly {
		m.maxBytes = minLimit(tc.MaxReturnedBytes, tc.BudgetLimits.MaxReturnedBytes)
	}
	tc.results = m
	return tc, m.close
}

// errResultCut stops reading a write's rows past the call's rows or the
// process's result memory: the call returns the rows read so far, marked
// truncated.
var errResultCut = errors.New("tool: the result of a write was cut short")

// minLimit is the stricter of two limits, where 0 is no limit.
func minLimit(a, b int64) int64 {
	if a <= 0 || (b > 0 && b < a) {
		return b
	}
	return a
}

// add counts row against the call's limits.
func (m *resultMeter) add(row map[string]any) error {
	if m == nil {
		return nil
	}
	n, err := encodedSize(row)
	if err != nil {
		return fmt.Errorf("encode a result row: %w", err)
	}
	return m.addSized(1, n)
}

// rowBuffers are the buffers rows are encoded into to be counted: a read
// counts each of its rows, thousands.
var rowBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// encodedSize is the size of v encoded as JSON, and its separator (the
// newline an encoder ends it with stands for it).
func encodedSize(v any) (int64, error) {
	buf := rowBuffers.Get().(*bytes.Buffer)
	defer rowBuffers.Put(buf)
	buf.Reset()
	if err := json.NewEncoder(buf).Encode(v); err != nil {
		return 0, err
	}
	return int64(buf.Len()), nil
}

// addValues counts a row of values, as an escape hatch query returns it (a
// list: its columns may share a name).
func (m *resultMeter) addValues(values []any) error {
	if m == nil {
		return nil
	}
	n, err := encodedSize(values)
	if err != nil {
		return fmt.Errorf("encode a result row: %w", err)
	}
	return m.addSized(1, n)
}

// addSized counts rows of n bytes in all (each with its separator) against
// the call's limits: the rows of a read cached, counted as they were read.
// Only a read's rows come at once: a write's are cut one at a time.
func (m *resultMeter) addSized(rows, n int64) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows += rows
	if m.maxRows > 0 && m.rows > m.maxRows {
		if m.write {
			m.rows -= rows
			return errResultCut
		}
		return budget.ErrExceeded
	}
	m.bytes += n
	if m.maxBytes > 0 && m.bytes > m.maxBytes {
		return fmt.Errorf("%w: the result exceeds %d bytes", budget.ErrExceeded, m.maxBytes)
	}
	if m.closed {
		return nil
	}
	if !m.memory.reserve(n) {
		if m.write {
			m.rows, m.bytes = m.rows-rows, m.bytes-n
			return errResultCut
		}
		return fmt.Errorf("%w: the server holds too many results in memory; narrow the request or retry later",
			budget.ErrExceeded)
	}
	m.held += n
	return nil
}

// counted returns the rows and bytes counted.
func (m *resultMeter) counted() (rows, bytes int64) {
	if m == nil {
		return 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows, m.bytes
}

// close releases the call's results from the process's memory.
func (m *resultMeter) close() {
	m.mu.Lock()
	held := m.held
	m.held, m.closed = 0, true
	m.mu.Unlock()
	m.memory.release(held)
}
