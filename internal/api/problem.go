package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
)

// statusForKind maps a domain error Kind to its HTTP status.
func statusForKind(k domain.Kind) int {
	switch k {
	case domain.NotFound:
		return http.StatusNotFound
	case domain.Conflict:
		return http.StatusConflict
	case domain.Invalid:
		return http.StatusBadRequest
	case domain.Unprocessable:
		return http.StatusUnprocessableEntity
	case domain.PreconditionFailed:
		return http.StatusPreconditionFailed
	case domain.PreconditionRequired:
		return http.StatusPreconditionRequired
	default:
		return http.StatusInternalServerError
	}
}

// titleForStatus is a short RFC 7807 title per status.
func titleForStatus(code int) string {
	switch code {
	case http.StatusNotFound:
		return "Not Found"
	case http.StatusConflict:
		return "Conflict"
	case http.StatusBadRequest:
		return "Bad Request"
	case http.StatusUnprocessableEntity:
		return "Unprocessable Entity"
	case http.StatusPreconditionFailed:
		return "Precondition Failed"
	case http.StatusPreconditionRequired:
		return "Precondition Required"
	default:
		return "Internal Server Error"
	}
}

// problem is the RFC 7807 document written to the client.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// writeProblem maps err to a problem+json response. domain.Error kinds map to
// their status; anything else is a 500 with a generic detail (the real error is
// logged, never leaked).
func writeProblem(w http.ResponseWriter, err error) {
	var de *domain.Error
	if !errors.As(err, &de) {
		slog.Error("internal error", "err", err)
		writeProblemDoc(w, &problem{
			Type:   "about:blank",
			Title:  titleForStatus(http.StatusInternalServerError),
			Status: http.StatusInternalServerError,
			Detail: "internal server error",
		})
		return
	}
	code := statusForKind(de.Kind)
	writeProblemDoc(w, &problem{
		Type:   "about:blank",
		Title:  titleForStatus(code),
		Status: code,
		Detail: de.Msg,
	})
}

func writeProblemDoc(w http.ResponseWriter, p *problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
