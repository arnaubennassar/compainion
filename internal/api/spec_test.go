package api_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	rootapi "github.com/arnaubennassar/compainion/api"
	"github.com/arnaubennassar/compainion/internal/api"
	"github.com/arnaubennassar/compainion/internal/store"
)

// TestRouteSpecParity pins Task 3.5: the routes registered on the server and
// the operations documented in the embedded OpenAPI spec must match exactly,
// in both directions.
func TestRouteSpecParity(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(rootapi.Spec)
	if err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	srv := api.New(db)

	// Spec side: set of "METHOD /path" (paths in template form, e.g.
	// /plans/{id}/steps).
	specRoutes := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			specRoutes[strings.ToUpper(method)+" "+path] = true
		}
	}

	// Server side: routes look like "METHOD /path/{param}".
	serverRoutes := map[string]bool{}
	for _, pattern := range srv.Routes() {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			continue // catch-all "/" has no method; not part of the API
		}
		serverRoutes[method+" "+path] = true
	}

	var missingInSpec, missingOnServer []string
	for r := range serverRoutes {
		if !specRoutes[r] {
			missingInSpec = append(missingInSpec, r)
		}
	}
	for r := range specRoutes {
		if !serverRoutes[r] {
			missingOnServer = append(missingOnServer, r)
		}
	}
	sort.Strings(missingInSpec)
	sort.Strings(missingOnServer)
	if len(missingInSpec) > 0 || len(missingOnServer) > 0 {
		t.Fatalf("route/spec parity mismatch:\n  registered but not documented:\n    %s\n  documented but not registered:\n    %s",
			strings.Join(missingInSpec, "\n    "), strings.Join(missingOnServer, "\n    "))
	}
}
