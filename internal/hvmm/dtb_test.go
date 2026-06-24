//go:build darwin

package hvmm

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestBuildDTB checks the generated device tree is a well-formed FDT whose memory
// node tracks the machine's RAM constants.
func TestBuildDTB(t *testing.T) {
	b := buildDTB()
	if got := binary.BigEndian.Uint32(b[0:]); got != fdtMagic {
		t.Fatalf("magic = %#x, want %#x", got, fdtMagic)
	}
	if got := binary.BigEndian.Uint32(b[4:]); int(got) != len(b) {
		t.Fatalf("totalsize = %d, want %d", got, len(b))
	}
	if want := regCells(bootRAMBase, uint64(bootRAMSize)); !bytes.Contains(b, want) {
		t.Fatalf("memory reg <%#x %#x> not present in DTB", bootRAMBase, bootRAMSize)
	}
}
