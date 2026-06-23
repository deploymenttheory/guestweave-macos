//go:build darwin

package qemu

import (
	"slices"
	"strings"
	"testing"

	weaveplatform "github.com/deploymenttheory/weave/internal/platform"
	"github.com/deploymenttheory/weave/internal/vmconfig"
	"github.com/deploymenttheory/weave/internal/vmdirectory"
)

func testSpec(iso string) Spec {
	return Spec{
		Toolchain: &Toolchain{
			SystemAARCH64:        "/usr/bin/qemu-system-aarch64",
			Img:                  "/usr/bin/qemu-img",
			FirmwareCode:         "/fw/code.fd",
			FirmwareVarsTemplate: "/fw/vars.fd",
			Accel:                "hvf",
		},
		Config: &vmconfig.VMConfig{
			OS:         weaveplatform.OSWindows,
			Arch:       weaveplatform.ArchitectureARM64,
			CPUCount:   6,
			MemorySize: 8 * 1024 * 1024 * 1024,
		},
		VMDir:      vmdirectory.NewVMDirectory("/vms/win11"),
		InstallISO: iso,
		VNCDisplay: 2,
	}
}

// argValue returns the token following the first occurrence of flag.
func argValue(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func TestBuildArgsCoreShape(t *testing.T) {
	args := BuildArgs(testSpec("/iso/win.iso"))

	if v, _ := argValue(args, "-machine"); !strings.HasPrefix(v, "virt") {
		t.Errorf("-machine = %q, want virt…", v)
	}
	if v, _ := argValue(args, "-accel"); v != "hvf" {
		t.Errorf("-accel = %q, want hvf", v)
	}
	if v, _ := argValue(args, "-cpu"); v != "host" {
		t.Errorf("-cpu = %q, want host under hvf", v)
	}
	if v, _ := argValue(args, "-smp"); v != "6" {
		t.Errorf("-smp = %q, want 6", v)
	}
	if v, _ := argValue(args, "-m"); v != "8192M" {
		t.Errorf("-m = %q, want 8192M", v)
	}
	if v, _ := argValue(args, "-vnc"); v != "127.0.0.1:2" {
		t.Errorf("-vnc = %q, want 127.0.0.1:2", v)
	}

	joined := strings.Join(args, " ")
	for _, want := range []string{
		"if=pflash,format=raw,unit=0,file=/fw/code.fd,readonly=on",
		"if=pflash,format=raw,unit=1,file=/vms/win11/efi_vars.fd",
		"nvme,drive=disk0,serial=weave0,bootindex=1", // disk after CD when installing
		"usb-storage,drive=cd0,bootindex=0",          // CD boots first during install
		"ramfb",
		"unix:/vms/win11/qmp.sock,server=on,wait=off",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\n got: %s", want, joined)
		}
	}
}

func TestBuildArgsNoISOBootsDisk(t *testing.T) {
	args := BuildArgs(testSpec(""))
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "nvme,drive=disk0,serial=weave0,bootindex=0") {
		t.Errorf("without ISO the system disk should be bootindex=0\n got: %s", joined)
	}
	if strings.Contains(joined, "usb-storage") {
		t.Errorf("no install ISO should mean no CD device\n got: %s", joined)
	}
}

func TestBuildArgsTCGUsesMaxCPU(t *testing.T) {
	s := testSpec("")
	s.Toolchain.Accel = "tcg"
	args := BuildArgs(s)
	if v, _ := argValue(args, "-cpu"); v != "max" {
		t.Errorf("-cpu = %q, want max under tcg", v)
	}
	if v, _ := argValue(args, "-accel"); v != "tcg" {
		t.Errorf("-accel = %q, want tcg", v)
	}
}

func TestBuildArgsVNCPassword(t *testing.T) {
	s := testSpec("")
	s.VNCPasswordSet = true
	args := BuildArgs(s)
	if v, _ := argValue(args, "-vnc"); v != "127.0.0.1:2,password=on" {
		t.Errorf("-vnc = %q, want password=on suffix", v)
	}
}
