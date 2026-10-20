package mcpserver

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shutdownTimeout bounds how long a shutdown waits for the requests running.
const shutdownTimeout = 15 * time.Second

// callsRunning counts the requests running other than GETs: a GET is a
// stream (an MCP session's, the console's events) that lasts until its
// client leaves, or a short read (health, metrics); the others are tool
// calls and session requests, answered before a shutdown closes the
// sessions.
type callsRunning struct{ n atomic.Int64 }

func (c *callsRunning) count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			c.n.Add(1)
			defer c.n.Add(-1)
		}
		next.ServeHTTP(w, r)
	})
}

// answered waits, until ctx ends, for the calls running to be answered.
func (c *callsRunning) answered(ctx context.Context) {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for c.n.Load() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// endStreamsAtShutdown ends the admin console's event streams when srv
// shuts down: they last until the console leaves, and the shutdown would
// wait for them.
func endStreamsAtShutdown(srv *http.Server, admin http.Handler) {
	if streams, ok := admin.(interface{ EndStreams() }); ok {
		srv.RegisterOnShutdown(streams.EndStreams)
	}
}

// closeSessionsAtShutdown closes the MCP sessions of s when srv shuts down,
// once the calls running (calls) are answered or the shutdown's time is up:
// a session's stream lasts until its client leaves, and the shutdown would
// wait for it. A session closed rolls back its transactions
// (HTTPConfig.OnSessionClosed), as the process ending would.
func closeSessionsAtShutdown(srv *http.Server, s *mcp.Server, calls *callsRunning) {
	srv.RegisterOnShutdown(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		calls.answered(ctx)
		for ss := range s.Sessions() {
			_ = ss.Close()
		}
	})
}
