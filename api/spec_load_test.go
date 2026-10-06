package api_test

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/arnaubennassar/compainion/api"
)

// TestSpecLoadsAndValidates pins the contract: the embedded OpenAPI spec must
// parse and pass kin-openapi validation (dangling $refs, bad schemas, etc).
func TestSpecLoadsAndValidates(t *testing.T) {
	ctx := context.Background()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(api.Spec)
	if err != nil {
		t.Fatalf("LoadFromData: %v", err)
	}
	if doc.OpenAPI != "3.0.3" {
		t.Fatalf("want OpenAPI 3.0.3, got %s", doc.OpenAPI)
	}
	if err := doc.Validate(ctx); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(doc.Paths.Map()) == 0 {
		t.Fatal("spec defines no paths")
	}
}