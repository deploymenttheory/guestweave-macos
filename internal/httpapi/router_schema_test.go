// Guards that the OpenAPI document stays valid and in lock-step with the chi
// router: every served route must be documented, and every documented
// operation must be served. Runs as a plain `go test` (no VM, no network).
//go:build darwin

package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/deploymenttheory/guestweave/internal/httpapi/schema"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
)

// loadSpec loads and validates the embedded OpenAPI document.
func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(schema.OpenAPI)
	if err != nil {
		t.Fatalf("loading OpenAPI spec: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("OpenAPI spec is invalid: %v", err)
	}
	return doc
}

// TestOpenAPISpecIsValid fails when openapi.yaml is malformed or has broken
// $ref references.
func TestOpenAPISpecIsValid(t *testing.T) {
	loadSpec(t)
}

// TestRouterMatchesSpec fails when a route is added/removed without updating
// the spec, or vice versa.
func TestRouterMatchesSpec(t *testing.T) {
	doc := loadSpec(t)

	specOps := map[string]bool{}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			specOps[method+" "+normalizePath(path)] = true
		}
	}

	routerOps := map[string]bool{}
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routerOps[method+" "+normalizePath(route)] = true
		return nil
	}
	if err := chi.Walk(NewAPIServer(0).router(), walk); err != nil {
		t.Fatalf("walking router: %v", err)
	}

	for op := range routerOps {
		if !specOps[op] {
			t.Errorf("route %q is served but not documented in openapi.yaml", op)
		}
	}
	for op := range specOps {
		if !routerOps[op] {
			t.Errorf("openapi.yaml documents %q but no route serves it", op)
		}
	}
}

// normalizePath strips a trailing slash (chi reports group roots as "…/")
// so router and spec paths compare equal.
func normalizePath(p string) string {
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}
