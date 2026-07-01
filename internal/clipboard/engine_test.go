//go:build darwin

package clipboard

import "testing"

func TestFormatByteSize(t *testing.T) {
	cases := map[int64]string{
		0:        "0 B",
		512:      "512 B",
		1024:     "1 KiB",
		50 << 20: "50 MiB", // the default clipboard cap
		1 << 20:  "1 MiB",
		1 << 30:  "1 GiB",
		1 << 40:  "1 TiB",
		1 << 50:  "1 PiB",
		1 << 60:  "1 EiB", // must not panic on the largest bucket
	}
	for n, want := range cases {
		if got := formatByteSize(n); got != want {
			t.Errorf("formatByteSize(%d) = %q, want %q", n, got, want)
		}
	}
}
