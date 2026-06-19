// Theme: the HTTP API server (serve). Boots the server against the isolated
// home and exercises the whole route table at the contract level — status
// codes, validation errors, 404/405 routing, and response shapes. Endpoints
// whose happy path needs a real VM, the network, or the host keychain (create,
// run, pull/push, login/logout, exec/ssh against a live guest) are covered by
// their error/validation paths here and end-to-end by the lifecycle, network
// and guest suites.
//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func serveSuite() *Suite {
	const port = 17777
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var server *background

	return &Suite{
		Name: "serve",
		Setup: func(h *Harness) error {
			bg, err := h.Start(nil, "serve", "--port", fmt.Sprintf("%d", port))
			if err != nil {
				return err
			}
			server = bg
			validator, err := newSpecValidator()
			if err != nil {
				return fmt.Errorf("building OpenAPI validator: %w", err)
			}
			apiSpec = validator
			// Wait until the server answers.
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if _, _, err := httpGet(base + "/weave/host/status"); err == nil {
					return nil
				}
				time.Sleep(300 * time.Millisecond)
			}
			return fmt.Errorf("server did not become ready; output:\n%s", server.Output())
		},
		Teardown: func(h *Harness) {
			apiSpec = nil
			if server != nil {
				server.Stop()
			}
		},
		Cases: []Case{
			// --- system -------------------------------------------------
			{"GET /weave/host/status returns host info", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/host/status")
				wantStatus(t, status, 200, "host/status")
				var host struct {
					CPUCount int `json:"cpuCount"`
				}
				if err := json.Unmarshal([]byte(body), &host); err != nil {
					t.Fatalf("parsing host status: %v\n%s", err, body)
				}
				if host.CPUCount <= 0 {
					t.Errorf("cpuCount = %d, want > 0", host.CPUCount)
				}
			}},
			{"GET /weave/openapi.yaml serves the schema", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/openapi.yaml")
				wantStatus(t, status, 200, "openapi.yaml")
				if !strings.Contains(body, "openapi:") || !strings.Contains(body, "/weave/vms") {
					t.Errorf("schema body does not look like the OpenAPI doc:\n%.200s", body)
				}
			}},
			{"GET /weave/logs returns text", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/logs?type=info")
				wantStatus(t, status, 200, "logs")
			}},
			{"POST /weave/prune runs garbage collection", func(t *T, h *Harness) {
				status, body := mustPost(t, base+"/weave/prune", `{"entries":"caches","gc":true}`)
				wantStatus(t, status, 200, "prune")
				if !strings.Contains(body, "pruned") {
					t.Errorf("prune ack absent:\n%s", body)
				}
			}},
			{"POST /weave/prune without a criterion returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/prune", `{"entries":"caches"}`)
				wantStatus(t, status, 400, "prune without criterion")
			}},
			{"unknown route returns 404", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/nope")
				wantStatus(t, status, 404, "unknown route")
			}},
			{"wrong method returns 405", func(t *T, h *Harness) {
				status, _ := mustDelete(t, base+"/weave/vms")
				wantStatus(t, status, 405, "DELETE /weave/vms")
			}},

			// --- vms lifecycle -----------------------------------------
			{"GET /weave/vms returns a JSON array", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/vms")
				wantStatus(t, status, 200, "vms list")
				var vms []any
				if err := json.Unmarshal([]byte(body), &vms); err != nil {
					t.Fatalf("expected a JSON array: %v\n%s", err, body)
				}
			}},
			{"GET a missing VM returns 404", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/vms/ghost")
				wantStatus(t, status, 404, "missing VM")
			}},
			{"POST /weave/vms without a name returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms", `{}`)
				wantStatus(t, status, 400, "create without name")
			}},
			{"POST /weave/vms/clone without names returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/clone", `{}`)
				wantStatus(t, status, 400, "clone without names")
			}},
			{"POST /weave/vms/push without fields returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/push", `{}`)
				wantStatus(t, status, 400, "push without fields")
			}},
			{"POST /weave/vms/import without fields returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/import", `{}`)
				wantStatus(t, status, 400, "import without fields")
			}},
			{"POST /weave/vms/import/upload without name returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/import/upload", ``)
				wantStatus(t, status, 400, "upload without name")
			}},
			{"DELETE a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustDelete(t, base+"/weave/vms/ghost")
				wantError(t, status, "delete missing VM")
			}},
			{"PATCH a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustPatch(t, base+"/weave/vms/ghost", `{"cpu":2}`)
				wantError(t, status, "patch missing VM")
			}},
			{"POST stop on a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/stop", `{}`)
				wantError(t, status, "stop missing VM")
			}},
			{"POST suspend on a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/suspend", ``)
				wantError(t, status, "suspend missing VM")
			}},
			{"POST rename without newName returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/rename", `{}`)
				wantStatus(t, status, 400, "rename without newName")
			}},
			{"POST setup without unattended is an error", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/setup", `{}`)
				wantError(t, status, "setup without preset")
			}},
			{"POST export on a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/export", `{}`)
				wantError(t, status, "export missing VM")
			}},
			{"GET export/download on a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/vms/ghost/export/download")
				wantError(t, status, "export download missing VM")
			}},

			// --- guest --------------------------------------------------
			{"GET fqn echoes a local name", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/vms/local-name/fqn")
				wantStatus(t, status, 200, "fqn")
				var resp struct {
					FQN string `json:"fqn"`
				}
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("parsing fqn: %v\n%s", err, body)
				}
				if resp.FQN != "local-name" {
					t.Errorf("fqn = %q, want %q", resp.FQN, "local-name")
				}
			}},
			{"GET ip on a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/vms/ghost/ip")
				wantError(t, status, "ip missing VM")
			}},
			{"POST exec without a command returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/exec", `{}`)
				wantStatus(t, status, 400, "exec without command")
			}},
			{"POST exec on a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/exec", `{"command":["true"]}`)
				wantError(t, status, "exec missing VM")
			}},
			{"POST ssh without a command returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/ghost/ssh", `{}`)
				wantStatus(t, status, 400, "ssh without command")
			}},
			{"GET exec/ws without cmd returns 400", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/vms/ghost/exec/ws")
				wantStatus(t, status, 400, "exec/ws without cmd")
			}},
			{"GET ssh/ws on a missing VM is an error", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/vms/ghost/ssh/ws")
				wantError(t, status, "ssh/ws missing VM")
			}},

			// --- images / registry ------------------------------------
			{"POST pull without an image returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/pull", `{}`)
				wantStatus(t, status, 400, "pull without image")
			}},
			{"POST pull/start without an image returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/pull/start", `{}`)
				wantStatus(t, status, 400, "pull/start without image")
			}},
			{"GET an unknown pull job returns 404", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/pull/999999")
				wantStatus(t, status, 404, "unknown pull job")
			}},
			{"GET images without a repository returns 400", func(t *T, h *Harness) {
				status, _ := mustGet(t, base+"/weave/images")
				wantStatus(t, status, 400, "images without repository")
			}},
			{"POST registry/login without fields returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/registry/login", `{}`)
				wantStatus(t, status, 400, "login without fields")
			}},

			// --- config -------------------------------------------------
			{"GET /weave/config returns settings", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/config")
				wantStatus(t, status, 200, "config get")
				var settings map[string]any
				if err := json.Unmarshal([]byte(body), &settings); err != nil {
					t.Fatalf("parsing config: %v\n%s", err, body)
				}
			}},
			{"GET /weave/config/cache returns a directory", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/config/cache")
				wantStatus(t, status, 200, "cache get")
				if !strings.Contains(body, "cacheDir") {
					t.Errorf("cacheDir absent:\n%s", body)
				}
			}},
			{"POST /weave/config/cache sets the directory", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/config/cache",
					`{"cacheDir":"`+h.WeaveHome+`"}`)
				wantStatus(t, status, 200, "cache set")
			}},
			{"POST /weave/config/registry/ghcr sets the organization", func(t *T, h *Harness) {
				status, body := mustPost(t, base+"/weave/config/registry/ghcr",
					`{"organization":"acme"}`)
				wantStatus(t, status, 200, "ghcr set")
				if !strings.Contains(body, "acme") {
					t.Errorf("organization absent:\n%s", body)
				}
			}},
			{"GET /weave/config/network/interfaces returns a list", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/config/network/interfaces")
				wantStatus(t, status, 200, "network interfaces")
				var resp struct {
					Interfaces []string `json:"interfaces"`
				}
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("parsing interfaces: %v\n%s", err, body)
				}
			}},
			{"GET/POST /weave/config/logging round-trips", func(t *T, h *Harness) {
				status, body := mustPost(t, base+"/weave/config/logging", `{"maxSizeMB":42,"keepRotated":false}`)
				wantStatus(t, status, 200, "logging set")
				if !strings.Contains(body, "42") {
					t.Errorf("maxSizeMB absent:\n%s", body)
				}
				status, body = mustGet(t, base+"/weave/config/logging")
				wantStatus(t, status, 200, "logging get")
				var logging struct {
					MaxSizeMB   int  `json:"maxSizeMB"`
					KeepRotated bool `json:"keepRotated"`
				}
				if err := json.Unmarshal([]byte(body), &logging); err != nil {
					t.Fatalf("parsing logging: %v\n%s", err, body)
				}
				if logging.MaxSizeMB != 42 || logging.KeepRotated {
					t.Errorf("logging = %+v, want {42 false}", logging)
				}
			}},
			{"POST /weave/config/logging rejects a negative cap", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/config/logging", `{"maxSizeMB":-1}`)
				wantStatus(t, status, 400, "negative maxSizeMB")
			}},
			{"GET /weave/config/registry returns the default", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/config/registry")
				wantStatus(t, status, 200, "registry status")
				if !strings.Contains(body, "host") {
					t.Errorf("host absent:\n%s", body)
				}
			}},
			{"registry profile CRUD over HTTP", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/config/registry/profiles",
					`{"name":"acme","organization":"acme-org","default":true}`)
				wantStatus(t, status, 201, "add profile")

				_, body := mustGet(t, base+"/weave/config/registry/profiles")
				if !strings.Contains(body, "acme") || !strings.Contains(body, "acme-org") {
					t.Errorf("profile absent after creation:\n%s", body)
				}

				status, _ = mustPost(t, base+"/weave/config/registry/profiles/default/acme", ``)
				wantStatus(t, status, 200, "set default profile")

				status, _ = mustDelete(t, base+"/weave/config/registry/profiles/acme")
				wantStatus(t, status, 200, "remove profile")
			}},
			{"add registry profile without organization returns 400", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/config/registry/profiles", `{"name":"x"}`)
				wantStatus(t, status, 400, "profile without org")
			}},
			{"set a missing default registry profile returns 404", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/config/registry/profiles/default/ghost", ``)
				wantStatus(t, status, 404, "missing default profile")
			}},
			{"config storage location CRUD over HTTP", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/config/locations",
					`{"name":"http-loc","path":"`+h.WeaveHome+`/http-loc"}`)
				wantStatus(t, status, 201, "add location")

				_, body := mustGet(t, base+"/weave/config/locations")
				if !strings.Contains(body, "http-loc") {
					t.Errorf("location absent after creation:\n%s", body)
				}

				status, _ = mustPost(t, base+"/weave/config/locations/default/http-loc", ``)
				wantStatus(t, status, 200, "set default location")

				status, _ = mustDelete(t, base+"/weave/config/locations/http-loc")
				wantStatus(t, status, 200, "remove location")
			}},
		},
	}
}

// --- HTTP helpers ----------------------------------------------------------

func httpGet(url string) (int, string, error) {
	return httpReq(http.MethodGet, url, "")
}

func httpPost(url, jsonBody string) (int, string, error) {
	return httpReq(http.MethodPost, url, jsonBody)
}

func httpDelete(url string) (int, string, error) {
	return httpReq(http.MethodDelete, url, "")
}

func httpPatch(url, jsonBody string) (int, string, error) {
	return httpReq(http.MethodPatch, url, jsonBody)
}

func httpReq(method, url, jsonBody string) (int, string, error) {
	var body io.Reader
	if jsonBody != "" {
		body = strings.NewReader(jsonBody)
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, "", err
	}
	if jsonBody != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	out, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(out), nil
}

// --- assertion helpers -----------------------------------------------------

// mustGet/mustPost/mustDelete/mustPatch issue the request and, when a spec
// validator is active (set in the suite Setup), validate the response against
// the OpenAPI document before returning.

func mustGet(t *T, url string) (int, string) {
	if apiSpec != nil {
		return apiSpec.request(t, http.MethodGet, url, "")
	}
	status, body, err := httpGet(url)
	if err != nil {
		t.Fatalf("GET %s failed: %v", url, err)
	}
	return status, body
}

func mustPost(t *T, url, body string) (int, string) {
	if apiSpec != nil {
		return apiSpec.request(t, http.MethodPost, url, body)
	}
	status, out, err := httpPost(url, body)
	if err != nil {
		t.Fatalf("POST %s failed: %v", url, err)
	}
	return status, out
}

func mustDelete(t *T, url string) (int, string) {
	if apiSpec != nil {
		return apiSpec.request(t, http.MethodDelete, url, "")
	}
	status, out, err := httpDelete(url)
	if err != nil {
		t.Fatalf("DELETE %s failed: %v", url, err)
	}
	return status, out
}

func mustPatch(t *T, url, body string) (int, string) {
	if apiSpec != nil {
		return apiSpec.request(t, http.MethodPatch, url, body)
	}
	status, out, err := httpPatch(url, body)
	if err != nil {
		t.Fatalf("PATCH %s failed: %v", url, err)
	}
	return status, out
}

func wantStatus(t *T, got, want int, context string) {
	if got != want {
		t.Errorf("%s: status = %d, want %d", context, got, want)
	}
}

// wantError asserts a non-2xx status (the exact 4xx/5xx depends on the
// underlying command's error classification).
func wantError(t *T, got int, context string) {
	if got >= 200 && got < 300 {
		t.Errorf("%s: status = %d, want an error (>= 400)", context, got)
	}
}
