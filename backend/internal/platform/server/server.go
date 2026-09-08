// Package server runs an HTTP listener with a graceful shutdown.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Timeouts protecting the listener from slow or idle peers.
//
// ReadHeaderTimeout bounds the header phase and readTimeout bounds the body
// after it: without the second, a client that has sent complete headers may
// then feed the 1 MiB body one byte at a time and hold a goroutine and a
// connection for as long as it likes. Thirty seconds is far beyond any
// legitimate JSON body — the largest thing the API accepts is a roster import
// — and far below "indefinitely".
//
// "Legitimate JSON body" is the premise, and one route now carries something
// else: a chunk of an uploaded game dump, megabytes of it, sent from wherever
// an organiser happens to be with nothing in front buffering the request. It
// does not weaken this value — it replaces the deadline for its own request
// with one sized to its own body, through http.ResponseController (see
// internal/api's minChunkUploadBytesPerSecond). Anything added here that
// takes a large body has to do the same; raising the number below instead
// would hand every other route the same slack for no reason.
//
// WriteTimeout is deliberately absent, not forgotten. It is measured from the
// end of the request headers rather than from the start of the response, so
// any value at all would cut off the streaming endpoints (SSE) that keep a
// response open for the length of a contest. Slow readers are bounded by
// IdleTimeout and by the reverse proxy in front.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	idleTimeout       = 2 * time.Minute
)

// Server owns one HTTP listener.
type Server struct {
	name string
	log  *slog.Logger
	http *http.Server

	mu       sync.Mutex
	listener net.Listener
	done     chan struct{}
}

// New creates a server bound to addr. The name identifies it in logs, since
// the service runs a public and an internal listener side by side.
func New(name, addr string, handler http.Handler, log *slog.Logger) *Server {
	return &Server{
		name: name,
		log:  log,
		http: &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			IdleTimeout:       idleTimeout,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
	}
}

// Start binds the listener and serves in the background. Binding happens
// synchronously so an occupied port is reported to the caller instead of
// vanishing into a goroutine.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.listener = ln
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()

	s.log.Info("http server listening", "server", s.name, "addr", ln.Addr().String())

	go func() {
		defer close(done)
		// Serve always ends with an error; a closed listener is the expected
		// outcome of a graceful shutdown, not a failure.
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("http server stopped", "server", s.name, "error", err)
		}
	}()

	return nil
}

// Addr returns the address the listener actually bound, which differs from the
// configured one when port 0 was requested.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener == nil {
		return s.http.Addr
	}
	return s.listener.Addr().String()
}

// Shutdown stops accepting connections and waits for in-flight requests to
// finish, or for ctx to expire. Calling it on a server that never started is a
// no-op, which keeps startup error paths simple.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	started := s.listener != nil
	done := s.done
	s.mu.Unlock()

	if !started {
		return nil
	}

	s.log.Info("http server shutting down", "server", s.name)

	err := s.http.Shutdown(ctx)

	select {
	case <-done:
	case <-ctx.Done():
	}

	return err
}
