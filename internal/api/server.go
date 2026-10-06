// Package api implements the HTTP layer: routing, RFC 7807 problem responses,
// middleware (logging, panic recovery, idempotency), pagination and
// conditional-request helpers.
package api

import (
	"log/slog"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// Server owns the HTTP mux, the route registry and the store.
type Server struct {
	db     *store.DB
	log    *slog.Logger
	mux    *http.ServeMux
	routes []string
}

// New builds a Server over db with the default (JSON) logger.
func New(db *store.DB) *Server {
	return NewWithLogger(db, slog.Default())
}

// NewWithLogger builds a Server using the given logger.
func NewWithLogger(db *store.DB, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{db: db, log: log, mux: http.NewServeMux()}
	s.handle("GET /healthz", s.healthz)
	s.registerPlans()
	s.registerSteps()
	s.registerTasks()
	s.registerWorkstreams()
	s.registerAgents()
	s.registerEvents()
	s.registerSubscriptions()
	s.registerOpenapi()
	s.registerStream()
	s.registerInterruptions()
	s.registerFindings()
	s.handle("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, domain.Errf(domain.NotFound, "no route for %s %s", r.Method, r.URL.Path))
	})
	return s
}

// handle registers a Go 1.22 method+path pattern on the mux and records it in
// the route registry (exposed via Routes for the OpenAPI parity test).
func (s *Server) handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h)
	s.routes = append(s.routes, pattern)
}

// Routes returns the registered route patterns in registration order.
func (s *Server) Routes() []string {
	out := make([]string, len(s.routes))
	copy(out, s.routes)
	return out
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.recoverPanics(s.logRequests(s.idempotency(s.mux)))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
