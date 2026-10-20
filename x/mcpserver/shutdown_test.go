package mcpserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
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
