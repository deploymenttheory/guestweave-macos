//go:build darwin

package hvmm

import (
	_ "embed"
	"encoding/binary"
	"math/bits"
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
// directory, and named files (the ACPI blobs + a writable etc/ramfb), over both
// the traditional data-register interface and the DMA interface. edk2 runs the
// etc/table-loader script itself; ramfb's config arrives as a DMA write.
//
// MMIO layout (DTB fw-cfg node, reg = <0x09020000 0x18>):
//   +0x00  data register     (reads return successive bytes of the selected item)
//   +0x08  selector register  (16-bit, big-endian; a write selects an item)
//   +0x10  DMA register       (64-bit big-endian guest address of a DMA descriptor)
type fwcfg struct {
	mem      *Machine // for DMA descriptor + payload access (nil = no DMA)
	fb       *ramfb   // etc/ramfb target
	selector uint16
	offset   int
	dmaHi    uint32 // high half of a split 32-bit DMA address write
	files    []fwCfgFile
	dir      []byte // serialized file directory (item fwCfgFileDir)
}

type fwCfgFile struct {
	name string
	data []byte
}

const (
	fwCfgSignature uint16 = 0x0000
	fwCfgID        uint16 = 0x0001
	fwCfgFileDir   uint16 = 0x0019
	fwCfgFileFirst uint16 = 0x0020

	// DMA descriptor control bits.
	fwCfgDmaError  = 0x01
	fwCfgDmaRead   = 0x02
	fwCfgDmaSkip   = 0x04
	fwCfgDmaSelect = 0x08
	fwCfgDmaWrite  = 0x10
)

func newFwCfg(m *Machine, fb *ramfb) *fwcfg {
	f := &fwcfg{mem: m, fb: fb, files: []fwCfgFile{
		{"etc/acpi/rsdp", acpiRsdp},
		{"etc/acpi/tables", acpiTables},
		{"etc/table-loader", acpiTableLoader},
		{"etc/ramfb", make([]byte, 28)}, // writable RAMFBCfg
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

func (f *fwcfg) write(off uint64, bytes int, val uint64) {
	switch {
	case off == 0x08: // selector register — 16-bit big-endian
		f.selector = uint16(val>>8) | uint16(val<<8)
		f.offset = 0
	case off == 0x10 && bytes == 8: // DMA address — single 64-bit big-endian write
		f.processDMA(bits.ReverseBytes64(val))
	case off == 0x10: // DMA address high half
		f.dmaHi = bits.ReverseBytes32(uint32(val))
	case off == 0x14: // DMA address low half — triggers the transfer
		f.processDMA(uint64(f.dmaHi)<<32 | uint64(bits.ReverseBytes32(uint32(val))))
	}
}

// processDMA executes a fw_cfg DMA descriptor at guest-physical descAddr:
// { be32 control, be32 length, be64 address }. control may select an item, then
// read it into guest memory or write guest memory into a writable item.
func (f *fwcfg) processDMA(descAddr uint64) {
	if f.mem == nil {
		return
	}
	d := f.mem.ReadGuest(descAddr, 16)
	if d == nil {
		return
	}
	control := binary.BigEndian.Uint32(d[0:])
	length := int(binary.BigEndian.Uint32(d[4:]))
	addr := binary.BigEndian.Uint64(d[8:])
	if control&fwCfgDmaSelect != 0 {
		f.selector = uint16(control >> 16)
		f.offset = 0
	}
	switch {
	case control&fwCfgDmaRead != 0:
		data := f.item(f.selector)
		buf := make([]byte, length)
		for i := 0; i < length; i++ {
			if f.offset < len(data) {
				buf[i] = data[f.offset]
			}
			f.offset++
		}
		f.mem.WriteGuest(addr, buf)
	case control&fwCfgDmaWrite != 0:
		if buf := f.mem.ReadGuest(addr, length); buf != nil {
			f.writeItem(f.selector, buf)
		}
	case control&fwCfgDmaSkip != 0:
		f.offset += length
	}
	f.mem.WriteGuest(descAddr, []byte{0, 0, 0, 0}) // clear control: success
}

// writeItem stores a DMA write into a writable item (etc/ramfb) and notifies it.
func (f *fwcfg) writeItem(sel uint16, data []byte) {
	if sel < fwCfgFileFirst || int(sel-fwCfgFileFirst) >= len(f.files) {
		return
	}
	file := &f.files[sel-fwCfgFileFirst]
	if f.offset < len(file.data) {
		f.offset += copy(file.data[f.offset:], data)
	}
	if file.name == "etc/ramfb" && f.fb != nil {
		f.fb.setConfig(file.data)
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
		if f.mem != nil {
			return []byte{0x03, 0, 0, 0} // traditional + DMA interfaces (LE)
		}
		return []byte{0x01, 0, 0, 0} // traditional only
	case fwCfgFileDir:
		return f.dir
	}
	if sel >= fwCfgFileFirst && int(sel-fwCfgFileFirst) < len(f.files) {
		return f.files[sel-fwCfgFileFirst].data
	}
	return nil
}
