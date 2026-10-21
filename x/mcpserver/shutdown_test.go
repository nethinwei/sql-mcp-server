package mcpserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// streamingAdmin answers with a stream that lasts until EndStreams.
type streamingAdmin struct {
	started chan struct{}
	ended   chan struct{}
	once    sync.Once
}

func (a *streamingAdmin) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.(http.Flusher).Flush()
	close(a.started)
	<-a.ended
}

func (a *streamingAdmin) EndStreams() { a.once.Do(func() { close(a.ended) }) }

// A console's event stream does not hold the shutdown: it ends with it.
func TestShutdownEndsTheConsoleStreams(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	admin := &streamingAdmin{started: make(chan struct{}), ended: make(chan struct{})}
	srv := &http.Server{Addr: addr, Handler: admin, ReadHeaderTimeout: time.Second}
	endStreamsAtShutdown(srv, admin)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serveHTTPWithShutdown(ctx, nil, srv, false) }()
	read := make(chan struct{})
	go func() {
		defer close(read)
		for {
			res, err := http.Get("http://" + addr + "/")
			if err == nil {
				_, _ = io.Copy(io.Discard, res.Body) // until the stream ends
				_ = res.Body.Close()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	<-admin.started
	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the shutdown")
	}
}

// At shutdown the tool calls running are answered first; then the MCP
// sessions close, ending their streams, so the shutdown completes instead
// of waiting out its timeout.
func TestShutdownAnswersCallsThenClosesSessions(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "shutdown"}, nil)
	started, release := make(chan struct{}), make(chan struct{})
	addSlowTool(s, started, release)
	calls := &callsRunning{}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	srv := &http.Server{Handler: calls.count(handler), ReadHeaderTimeout: time.Second}
	closeSessionsAtShutdown(srv, s, calls)
	go func() { _ = srv.Serve(l) }()
	transport := &mcp.StreamableClientTransport{Endpoint: "http://" + l.Addr().String()}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	answered := make(chan error, 1)
	go func() {
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "slow"})
		answered <- err
	}()
	<-started
	shutdown := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdown <- srv.Shutdown(ctx)
	}()
	time.Sleep(100 * time.Millisecond) // the shutdown waits for the call
	close(release)
	if err := <-answered; err != nil {
		t.Fatalf("the call running at shutdown was not answered: %v", err)
	}
	select {
	case err := <-shutdown:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session's stream held the shutdown")
	}
}

// addSlowTool adds a tool to s that answers once release closes, after
// closing started.
func addSlowTool(s *mcp.Server, started, release chan struct{}) {
	mcp.AddTool(s, &mcp.Tool{Name: "slow"}, func(context.Context, *mcp.CallToolRequest, struct{}) (
		*mcp.CallToolResult, any, error,
	) {
		close(started)
		<-release
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
	})
}
