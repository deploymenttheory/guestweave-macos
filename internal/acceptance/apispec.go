// Live OpenAPI conformance for the HTTP API suites: every response the server
// returns is validated against internal/httpapi/schema/openapi.yaml, which both
// proves the responses are correct and that the spec matches the running
// server. Requests whose path/method are not in the spec (the intentional
// unknown-route / wrong-method negatives) skip validation.
//go:build darwin

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deploymenttheory/guestweave/internal/httpapi/schema"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// startValidatedAPIServer starts `weave serve` on port, installs the shared
// OpenAPI validator (so mustGet/mustPost/… validate every response), and waits
// until the server answers. The caller must Stop the returned background and
// reset apiSpec in its Teardown.
func startValidatedAPIServer(h *Harness, port int) (*background, string, error) {
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	bg, err := h.Start(nil, "serve", "--port", strconv.Itoa(port))
	if err != nil {
		return nil, "", err
	}
	validator, err := newSpecValidator()
	if err != nil {
		bg.Stop()
		return nil, "", fmt.Errorf("building OpenAPI validator: %w", err)
	}
	apiSpec = validator

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := httpGet(base + "/weave/host/status"); err == nil {
			return bg, base, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	bg.Stop()
	return nil, "", fmt.Errorf("server did not become ready:\n%s", bg.Output())
}

// apiSpec is the validator shared by the HTTP API suites; it is set in each
// suite's Setup and consumed by mustGet/mustPost/mustDelete/mustPatch.
var apiSpec *specValidator

type specValidator struct {
	router routers.Router
}

// newSpecValidator loads and validates the embedded spec and builds a
// path-only router (the test server runs on a different port than the spec's
// declared server URL).
func newSpecValidator() (*specValidator, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(schema.OpenAPI)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	doc.Servers = nil // match on path, ignore host/port
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, err
	}
	v := &specValidator{router: router}

	// Self-check: confirm the router actually matches our paths, so a routing
	// mismatch fails loudly instead of silently skipping all validation.
	probe, _ := http.NewRequest(http.MethodGet, "http://localhost/weave/host/status", nil)
	if _, _, err := router.FindRoute(probe); err != nil {
		return nil, fmt.Errorf("spec validator self-check failed (paths not matching the spec): %w", err)
	}
	return v, nil
}

// request performs the HTTP call and validates the response against the spec,
// reporting any mismatch through t. It returns the status and body so callers
// keep asserting as before.
func (v *specValidator) request(t *T, method, url, jsonBody string) (int, string) {
	var body io.Reader
	if jsonBody != "" {
		body = strings.NewReader(jsonBody)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, url, err)
	}
	if jsonBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)

	v.validate(t, req, resp, out)
	return resp.StatusCode, string(out)
}

// validate matches the request to a spec operation and checks the response
// status + body against it. Unmatched requests (intentional negatives) are
// skipped.
func (v *specValidator) validate(t *T, req *http.Request, resp *http.Response, body []byte) {
	route, pathParams, err := v.router.FindRoute(req)
	if err != nil {
		return // not described by the spec
	}
	input := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
		},
		Status: resp.StatusCode,
		Header: resp.Header,
	}
	input.SetBodyBytes(body)
	if err := openapi3filter.ValidateResponse(context.Background(), input); err != nil {
		t.Errorf("response does not conform to the OpenAPI spec for %s %s (status %d): %v\nbody: %s",
			req.Method, req.URL.Path, resp.StatusCode, err, truncate(body))
	}
}

func truncate(b []byte) string {
	const max = 400
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}
