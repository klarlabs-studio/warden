package attach

import (
	"encoding/json"
	"net"
	"os"
	"sync"

	"go.klarlabs.de/warden/internal/application"
)

// clientBuffer bounds each watcher's pending messages. A watcher that falls
// behind drops events rather than back-pressuring the run.
const clientBuffer = 256

// Server publishes a run's events on a Unix socket. It satisfies
// application.Observer. Construction is best-effort: NewServer returns nil (a
// no-op) when the socket can't be created, so a run never fails over attach.
type Server struct {
	ln      net.Listener
	path    string
	mu      sync.Mutex
	clients map[chan Event]struct{}
	closed  bool
}

// NewServer starts listening on the repo's socket. A stale socket from a crashed
// run is removed first. Returns nil if listening fails — attach is optional.
func NewServer(gitDir string) *Server {
	path := SocketPath(gitDir)
	_ = os.Remove(path) // clear a stale socket from a previous run
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil
	}
	s := &Server{ln: ln, path: path, clients: map[chan Event]struct{}{}}
	go s.acceptLoop()
	return s
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		go s.serve(conn)
	}
}

// serve registers a client channel and streams its events to conn until the
// channel closes (run ended) or the write fails (watcher left).
func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	ch := make(chan Event, clientBuffer)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.clients[ch] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	enc := json.NewEncoder(conn)
	for ev := range ch {
		if err := enc.Encode(ev); err != nil {
			return
		}
	}
}

// OnStep broadcasts a step event (nil-safe, so a nil *Server is a no-op).
func (s *Server) OnStep(e application.StepEvent) {
	if s == nil {
		return
	}
	s.broadcast(eventFromStep(e))
}

// PublishDone broadcasts the terminal outcome.
func (s *Server) PublishDone(res application.RunResult) {
	if s == nil {
		return
	}
	s.broadcast(doneEvent(res))
}

// broadcast fans an event to every client, non-blocking: a full client buffer
// drops the event rather than stalling the run.
func (s *Server) broadcast(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- ev:
		default: // slow watcher — drop
		}
	}
}

// Close stops accepting, closes every client stream, and removes the socket.
// Safe to call on a nil *Server.
func (s *Server) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for ch := range s.clients {
		close(ch)
		delete(s.clients, ch)
	}
	s.mu.Unlock()

	_ = s.ln.Close()
	_ = os.Remove(s.path)
}

var _ application.Observer = (*Server)(nil)
