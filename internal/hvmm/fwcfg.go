//go:build darwin

package hvmm

import (
	_ "embed"
	"encoding/binary"
)

// QEMU's ACPI tables for the virt machine weave models, extracted from QEMU's
// fw_cfg (see .github/workflows/extract-qemu-acpi.yml). edk2 ArmVirtQemu builds
// and installs ACPI ONLY from these three fw_cfg files — Windows ARM64 needs ACPI
// (it does not consume the device tree). A ground-up Go ACPI generator will
// replace these blobs later, as buildDTB replaced the embedded virt.dtb.
var (
	//go:embed acpi/etc-acpi-rsdp.bin
	acpiRsdp []byte
	//go:embed acpi/etc-acpi-tables.bin
	acpiTables []byte
	//go:embed acpi/etc-table-loader.bin
	acpiTableLoader []byte
)

// fwcfg is a minimal QEMU fw_cfg device — the MMIO/device-tree variant the edk2
// ArmVirtQemu firmware probes at 0x09020000. It serves the signature, a file
// directory, and the named files (the ACPI blobs). It advertises only the
// traditional (non-DMA) interface, so firmware reads item content byte-by-byte
// from the data register; edk2 itself runs the etc/table-loader script to
// allocate, relocate and install the tables.
//
// MMIO layout (DTB fw-cfg node, reg = <0x09020000 0x18>):
//   +0x00  data register     (reads return successive bytes of the selected item)
//   +0x08  selector register  (16-bit, big-endian; a write selects an item)
//   +0x10  DMA register       (unimplemented — DMA is not advertised)
type fwcfg struct {
	selector uint16
	offset   int
	files    []fwCfgFile // file i is addressable at selector fwCfgFileFirst+i
	dir      []byte      // serialized file directory (item fwCfgFileDir)
}

type fwCfgFile struct {
	name string
	data []byte
}

// fw_cfg selector keys.
const (
	fwCfgSignature uint16 = 0x0000 // "QEMU"
	fwCfgID        uint16 = 0x0001 // interface feature bitmap
	fwCfgFileDir   uint16 = 0x0019 // file directory
	fwCfgFileFirst uint16 = 0x0020 // first dynamically-assigned file key
)

func newFwCfg() *fwcfg {
	f := &fwcfg{files: []fwCfgFile{
		{"etc/acpi/rsdp", acpiRsdp},
		{"etc/acpi/tables", acpiTables},
		{"etc/table-loader", acpiTableLoader},
	}}
	f.dir = f.buildDir()
	return f
}

// buildDir serializes the FWCfgFiles directory: a big-endian count followed by
// one 64-byte FWCfgFile entry per file (size, select, reserved, 56-byte name).
func (f *fwcfg) buildDir() []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(f.files)))
	for i, file := range f.files {
		b = binary.BigEndian.AppendUint32(b, uint32(len(file.data)))
		b = binary.BigEndian.AppendUint16(b, fwCfgFileFirst+uint16(i))
		b = binary.BigEndian.AppendUint16(b, 0) // reserved
		var name [56]byte
		copy(name[:], file.name)
		b = append(b, name[:]...)
	}
	return b
}

func (f *fwcfg) write(off uint64, _ int, val uint64) {
	if off == 0x08 { // selector register — 16-bit big-endian
		f.selector = uint16(val>>8) | uint16(val<<8) // byte-swap from BE
		f.offset = 0
	}
}

func (f *fwcfg) read(off uint64, bytes int) uint64 {
	if off != 0x00 { // only the data register returns item content
		return 0
	}
	data := f.item(f.selector)
	var v uint64
	for i := 0; i < bytes; i++ {
		var b byte
		if f.offset < len(data) {
			b = data[f.offset]
		}
		v |= uint64(b) << (8 * i)
		f.offset++
	}
	return v
}

func (f *fwcfg) item(sel uint16) []byte {
	switch sel {
	case fwCfgSignature:
		return []byte("QEMU")
	case fwCfgID:
		return []byte{0x01, 0, 0, 0} // bit0: traditional interface; no DMA (LE)
	case fwCfgFileDir:
		return f.dir
	}
	if sel >= fwCfgFileFirst && int(sel-fwCfgFileFirst) < len(f.files) {
		return f.files[sel-fwCfgFileFirst].data
	}
	return nil
}
