// Pins the run workflow's CLI-spec parsing semantics (moved with the code
// from internal/command's port smoke tests).
//go:build darwin

package run

import "testing"

// TestParseSharedDirectoryShare pins lume's --shared-dir parsing semantics.
func TestParseSharedDirectoryShare(t *testing.T) {
	cases := []struct {
		input    string
		path     string
		readOnly bool
		wantErr  bool
	}{
		{"/tmp/share", "/tmp/share", false, false},
		{"/tmp/share:ro", "/tmp/share", true, false},
		{"/tmp/share:rw", "/tmp/share", false, false},
		{"/tmp/share:bogus", "", false, true},
		{":ro", "", false, true},
	}
	for _, c := range cases {
		share, err := parseSharedDirectoryShare(c.input)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %+v", c.input, share)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.input, err)
			continue
		}
		if share.path != c.path || share.readOnly != c.readOnly {
			t.Errorf("%q: got path=%q readOnly=%v, want path=%q readOnly=%v",
				c.input, share.path, share.readOnly, c.path, c.readOnly)
		}
		if share.name != "" {
			t.Errorf("%q: shared-dir shares must be unnamed, got %q", c.input, share.name)
		}
	}
}
