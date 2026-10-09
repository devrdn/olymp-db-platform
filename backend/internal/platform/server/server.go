// Package server runs an HTTP listener with a graceful shutdown. It does not
// route or apply middleware; httpx and internal/api do.
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
// readTimeout bounds the body after the headers, so a client cannot feed the
// 1 MiB body a byte at a time and hold a connection indefinitely. Thirty
// seconds is far beyond any JSON body. A route taking a large body (game dump
// chunks) sets its own deadline through http.ResponseController instead of
// raising this value for every route.
//
// WriteTimeout is absent on purpose: it counts from the end of the request
// headers, so any value would cut off SSE streams that stay open for a whole
// contest. Slow readers are bounded by IdleTimeout and the reverse proxy.
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

// New creates a server bound to addr. The name tells the public and internal
// listeners apart in logs.
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

// Start binds the listener synchronously, so an occupied port is reported to
// the caller, and serves in the background.
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
		// A closed listener is the normal end of a graceful shutdown.
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
// finish, or for ctx to expire. It is a no-op on a server that never started.
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
