package api

import (
	"net/http"

	rootapi "github.com/arnaubennassar/compainion/api"
)

// registerOpenapi wires GET /openapi.yaml (the embedded spec).
func (s *Server) registerOpenapi() {
	s.handle("GET /openapi.yaml", s.serveOpenapi)
}

// serveOpenapi serves the embedded OpenAPI document.
func (s *Server) serveOpenapi(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rootapi.Spec)
}
