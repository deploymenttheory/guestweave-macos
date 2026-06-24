//go:build darwin

package winimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeISO writes a minimal ISO9660 image to a temp file: a 32 KB System Area
// followed by a Primary Volume Descriptor whose volume identifier is volID. If
// validPVD is false the standard identifier is corrupted so the descriptor is
// rejected. truncate, when > 0, caps the file to that many bytes (to exercise
// short-file handling).
func makeISO(t *testing.T, volID string, validPVD bool, truncate int) string {
	t.Helper()

	const total = isoPVDOffset + 2048
	buf := make([]byte, total)

	pvd := buf[isoPVDOffset:]
	pvd[0] = isoPVDType
	copy(pvd[1:6], isoStdID)
	pvd[6] = 1 // version
	if !validPVD {
		copy(pvd[1:6], "XXXXX")
	}
	// Volume id is space-padded a-characters, left-justified.
	id := []byte(volID)
	for i := range isoVolIDLen {
		if i < len(id) {
			pvd[isoVolIDOffset+i] = id[i]
		} else {
			pvd[isoVolIDOffset+i] = ' '
		}
	}

	if truncate > 0 && truncate < len(buf) {
		buf = buf[:truncate]
	}

	path := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write iso: %v", err)
	}
	return path
}

func TestInspectISOArch(t *testing.T) {
	tests := []struct {
		name    string
		volID   string
		valid   bool
		trunc   int
		want    string
		wantErr bool
	}{
		{name: "arm64", volID: "CCCOMA_ARM64FRE", valid: true, want: archARM64},
		{name: "arm64 lowercase", volID: "cccoma_arm64fre", valid: true, want: archARM64},
		{name: "x64", volID: "CCCOMA_X64FRE", valid: true, want: archX64},
		{name: "amd64 label", volID: "WIN11_AMD64", valid: true, want: archX64},
		{name: "unknown label", volID: "SOME_OTHER_DISC", valid: true, want: archUnknown},
		{name: "bad pvd magic", volID: "CCCOMA_ARM64FRE", valid: false, wantErr: true},
		{name: "short file", volID: "CCCOMA_ARM64FRE", valid: true, trunc: 4096, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := makeISO(t, tc.volID, tc.valid, tc.trunc)
			got, err := InspectISOArch(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got arch %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("arch = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInspectISOArchMissingFile(t *testing.T) {
	if _, err := InspectISOArch(filepath.Join(t.TempDir(), "nope.iso")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestRequireARM64ISO(t *testing.T) {
	if err := RequireARM64ISO(makeISO(t, "CCCOMA_ARM64FRE", true, 0)); err != nil {
		t.Fatalf("ARM64 ISO should pass: %v", err)
	}

	err := RequireARM64ISO(makeISO(t, "CCCOMA_X64FRE", true, 0))
	if err == nil {
		t.Fatal("x64 ISO should be rejected")
	}
	if !strings.Contains(err.Error(), "x64") {
		t.Fatalf("error should mention x64: %v", err)
	}

	if err := RequireARM64ISO(makeISO(t, "RANDOM_DISC", true, 0)); err == nil {
		t.Fatal("unknown-arch ISO should be rejected")
	}
}
