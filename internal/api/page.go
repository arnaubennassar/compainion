package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/arnaubennassar/compainion/internal/domain"
)

// maxBodyBytes is the request/response body cap (1 MiB).
const maxBodyBytes = 1 << 20

// writeJSON marshals v as JSON and writes it with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

// Envelope is the standard list response.
type Envelope struct {
	Items      any    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// decode strictly decodes a JSON request body into T: unknown fields are
// rejected and the body is capped at 1 MiB. An empty body is an error.
func decode[T any](r *http.Request) (T, error) {
	var out T
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, domain.Errf(domain.Invalid, "invalid JSON body: %v", err)
	}
	if dec.More() {
		return out, domain.Errf(domain.Invalid, "unexpected trailing data in body")
	}
	return out, nil
}

// parsePage reads ?limit (default 50, max 200) and ?cursor (opaque, the last id).
func parsePage(r *http.Request) (limit int, cursor string, err error) {
	limit = 50
	if q := r.URL.Query().Get("limit"); q != "" {
		limit, err = strconv.Atoi(q)
		if err != nil || limit < 1 || limit > 200 {
			return 0, "", domain.Errf(domain.Invalid, "limit must be an integer between 1 and 200")
		}
	}
	cursor = r.URL.Query().Get("cursor")
	return limit, cursor, nil
}

// parseIfMatch parses the If-Match header, which must hold a quoted positive
// integer version (ETag), e.g. `"3"`. Missing -> PreconditionRequired (428);
// malformed -> Invalid (400).
func parseIfMatch(r *http.Request) (int, error) {
	h := r.Header.Get("If-Match")
	if h == "" {
		return 0, domain.Errf(domain.PreconditionRequired, "If-Match header is required")
	}
	v, err := strconv.Atoi(strings.Trim(h, `"`))
	if err != nil || v < 1 || strings.TrimSpace(h) != h || strings.HasPrefix(h, `"`) != strings.HasSuffix(h, `"`) {
		return 0, domain.Errf(domain.Invalid, "If-Match must be a quoted positive integer ETag like \"3\"")
	}
	return v, nil
}

// etag renders the ETag header value for a resource version.
func etag(version int) string {
	return fmt.Sprintf(`"%d"`, version)
}