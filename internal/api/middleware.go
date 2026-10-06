package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/store"
)

// loggingResponseWriter records the status code written by the handler.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// logRequests logs method, path, status and duration for every request.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lw := &loggingResponseWriter{ResponseWriter: w}
		next.ServeHTTP(lw, r)
		if lw.status == 0 {
			lw.status = http.StatusOK
		}
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", lw.status)
	})
}

// recoverPanics converts handler panics into 500 problem responses.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "err", fmt.Sprint(rec), "method", r.Method, "path", r.URL.Path)
				writeProblem(w, fmt.Errorf("panic: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// idempotency wraps POST requests carrying an Idempotency-Key header. Same key
// + same body replays the stored response with Idempotent-Replayed: true; same
// key + different body is a 422. Other requests pass through untouched.
func (s *Server) idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if r.Method != http.MethodPost || key == "" {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			writeProblem(w, domain.Errf(domain.Invalid, "request body too large or unreadable"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		hash := bodyHash(body)
		ctx := r.Context()

		rec, err := s.db.LookupIdempotency(ctx, key, r.Method, r.URL.Path)
		switch {
		case err == nil:
			if rec.RequestHash != hash {
				writeProblem(w, domain.Errf(domain.Unprocessable,
					"Idempotency-Key %s was already used for a different request", key))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(rec.StatusCode)
			_, _ = w.Write([]byte(rec.ResponseBody))
			return
		case isNotFoundErr(err):
			// no cached response yet
		default:
			writeProblem(w, err)
			return
		}

		iw := &idempotencyResponseWriter{ResponseWriter: w}
		next.ServeHTTP(iw, r)
		if iw.status == 0 {
			iw.status = http.StatusOK
		}
		if iw.status < http.StatusInternalServerError {
			if err := s.db.SaveIdempotency(ctx, store.IdempotencyRecord{
				Key: key, Method: r.Method, Path: r.URL.Path,
				RequestHash: hash, StatusCode: iw.status, ResponseBody: iw.buf.String(),
			}); err != nil {
				writeProblem(w, err)
				return
			}
		}
	})
}

// idempotencyResponseWriter buffers the response so it can be cached and
// replayed for later identical requests.
type idempotencyResponseWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (w *idempotencyResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *idempotencyResponseWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func bodyHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// isNotFoundErr reports whether err is a domain NotFound (the sentinel the
// store uses for a missing idempotency record).
func isNotFoundErr(err error) bool {
	var de *domain.Error
	return errors.As(err, &de) && de.Kind == domain.NotFound
}

