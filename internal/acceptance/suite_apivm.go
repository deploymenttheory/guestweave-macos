// Theme: the HTTP API exercised end-to-end against a real, booted Linux guest
// (api-vm). Drives clone → run → get/ip → ssh/exec → stop → rename → export/
// import → delete entirely over HTTP, with every response validated against the
// OpenAPI spec (via the shared apiSpec validator). Gated: selected only with
// `-suites api-vm`. Uses a small Linux OCI image (no 20GB macOS IPSW), so it is
// the layer that can run on a self-hosted Apple-silicon CI runner.
//
// A bootable Linux guest image must be cached first (one-time):
//
//	weave pull ghcr.io/cirruslabs/ubuntu:latest
//
// The suite fails its Setup cleanly when no image is available. The default
// cirruslabs image authenticates as admin/admin, so guest commands use the ssh
// endpoint (which takes credentials); the exec endpoint hard-codes weave/weave,
// so it is only checked for a documented, schema-conforming response.
//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func apiVMSuite() *Suite {
	const port = 17778
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	cfg := loadNetBehaviorConfig()

	const (
		vm  = "acc-api-vm"
		vm2 = "acc-api-vm-renamed"
		vm3 = "acc-api-vm-imported"
	)
	var server *background

	return &Suite{
		Name: "api-vm",
		Setup: func(h *Harness) error {
			// Reuse the real ~/.weave OCI cache so the multi-GB image is not
			// re-downloaded into the isolated home.
			if err := shareOCICache(h); err != nil {
				return err
			}
			if !imageAvailable(cfg.image) {
				return fmt.Errorf(
					"no bootable Linux guest image cached — run: weave pull %s (or set WEAVE_ACC_LINUX_IMAGE)",
					cfg.image)
			}
			bg, _, err := startValidatedAPIServer(h, port)
			if err != nil {
				return err
			}
			server = bg
			return nil
		},
		Teardown: func(h *Harness) {
			h.Run("delete", vm, vm2, vm3)
			apiSpec = nil
			if server != nil {
				server.Stop()
			}
		},
		Cases: []Case{
			{"POST /weave/vms/clone clones the image to a VM", func(t *T, h *Harness) {
				status, body := mustPost(t, base+"/weave/vms/clone",
					fmt.Sprintf(`{"name":%q,"newName":%q}`, cfg.image, vm))
				if status != 201 {
					t.Fatalf("clone status = %d, want 201\n%s", status, body)
				}
			}},
			{"GET /weave/vms lists the clone", func(t *T, h *Harness) {
				_, body := mustGet(t, base+"/weave/vms")
				if !strings.Contains(body, vm) {
					t.Errorf("VM %q absent from listing:\n%s", vm, body)
				}
			}},
			{"GET the VM shows it stopped", func(t *T, h *Harness) {
				_, body := mustGet(t, base+"/weave/vms/"+vm)
				if d := decodeDetails(t, body); d.Running {
					t.Errorf("VM reports running before it was started")
				}
			}},
			{"POST run starts the VM headless", func(t *T, h *Harness) {
				status, body := mustPost(t, base+"/weave/vms/"+vm+"/run", `{}`)
				if status != 200 {
					t.Fatalf("run status = %d, want 200\n%s", status, body)
				}
			}},
			{"the VM reaches the running state with an IP", func(t *T, h *Harness) {
				deadline := time.Now().Add(120 * time.Second)
				for time.Now().Before(deadline) {
					_, body := mustGet(t, base+"/weave/vms/"+vm)
					if d := decodeDetails(t, body); d.Running {
						return
					}
					time.Sleep(2 * time.Second)
				}
				t.Fatalf("VM did not reach the running state within 120s")
			}},
			{"GET ip resolves the guest address", func(t *T, h *Harness) {
				status, body := mustGet(t, base+"/weave/vms/"+vm+"/ip?wait=120")
				if status != 200 {
					t.Fatalf("ip status = %d, want 200\n%s", status, body)
				}
				var resp struct {
					IP string `json:"ip"`
				}
				if err := json.Unmarshal([]byte(body), &resp); err != nil || resp.IP == "" {
					t.Fatalf("no IP resolved: %v\n%s", err, body)
				}
			}},
			{"POST ssh runs a command in the guest", func(t *T, h *Harness) {
				status, body := mustPost(t, base+"/weave/vms/"+vm+"/ssh",
					fmt.Sprintf(`{"command":["echo","weave-ok"],"user":%q,"password":%q,"wait":120}`,
						cfg.user, cfg.password))
				if status != 200 {
					t.Fatalf("ssh status = %d, want 200\n%s", status, body)
				}
				var resp struct {
					ExitCode int32  `json:"exitCode"`
					Output   string `json:"output"`
				}
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("parsing ssh response: %v\n%s", err, body)
				}
				if resp.ExitCode != 0 || !strings.Contains(resp.Output, "weave-ok") {
					t.Errorf("ssh echo failed: exit=%d output=%q", resp.ExitCode, resp.Output)
				}
			}},
			{"POST exec returns a documented response", func(t *T, h *Harness) {
				// The exec endpoint uses the weave/weave convention; the default
				// cirruslabs image authenticates as admin/admin, so the command
				// may fail auth. We only require a documented, schema-conforming
				// response (validated by mustPost) with a declared status.
				status, _ := mustPost(t, base+"/weave/vms/"+vm+"/exec", `{"command":["echo","exec-ok"]}`)
				if status != 200 && status != 500 {
					t.Errorf("exec status = %d, want 200 or 500", status)
				}
			}},
			{"POST stop shuts the VM down", func(t *T, h *Harness) {
				status, body := mustPost(t, base+"/weave/vms/"+vm+"/stop", `{}`)
				if status != 200 {
					t.Fatalf("stop status = %d, want 200\n%s", status, body)
				}
			}},
			{"POST rename renames the stopped VM", func(t *T, h *Harness) {
				status, _ := mustPost(t, base+"/weave/vms/"+vm+"/rename",
					fmt.Sprintf(`{"newName":%q}`, vm2))
				wantStatus(t, status, 200, "rename")
				status, _ = mustGet(t, base+"/weave/vms/"+vm2)
				wantStatus(t, status, 200, "get renamed VM")
				status, _ = mustGet(t, base+"/weave/vms/"+vm)
				wantStatus(t, status, 404, "old name gone")
			}},
			{"export + import round-trips the VM (set WEAVE_ACC_API_HEAVY=1)", func(t *T, h *Harness) {
				if os.Getenv("WEAVE_ACC_API_HEAVY") == "" {
					t.Skip("set WEAVE_ACC_API_HEAVY=1 to exercise the multi-GB export/import round-trip")
				}
				archive := filepath.Join(h.WeaveHome, vm2+".tvm")
				status, body := mustPost(t, base+"/weave/vms/"+vm2+"/export",
					fmt.Sprintf(`{"path":%q}`, archive))
				if status != 200 {
					t.Fatalf("export status = %d, want 200\n%s", status, body)
				}
				status, body = mustPost(t, base+"/weave/vms/import",
					fmt.Sprintf(`{"path":%q,"name":%q}`, archive, vm3))
				if status != 201 {
					t.Fatalf("import status = %d, want 201\n%s", status, body)
				}
			}},
			{"DELETE removes the VM", func(t *T, h *Harness) {
				status, _ := mustDelete(t, base+"/weave/vms/"+vm2)
				wantStatus(t, status, 200, "delete")
			}},
		},
	}
}

type vmDetails struct {
	Running   bool   `json:"running"`
	IPAddress string `json:"ipAddress"`
}

func decodeDetails(t *T, body string) vmDetails {
	var d vmDetails
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("parsing VM details: %v\n%s", err, body)
	}
	return d
}
