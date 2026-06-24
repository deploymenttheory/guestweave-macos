//go:build darwin

package hvmm

// fwcfg is a minimal QEMU fw_cfg device — the MMIO/device-tree variant that the
// edk2 ArmVirtQemu firmware probes at 0x09020000. It serves the signature so
// firmware detects the device, advertises only the traditional (non-DMA)
// interface, and presents an empty file directory. That is enough for firmware
// to probe it cleanly and move on rather than treating it as a broken device.
//
// MMIO layout (from the DTB's fw-cfg node, reg = <0x09020000 0x18>):
//   +0x00  data register   (reads return successive bytes of the selected item)
//   +0x08  selector register (16-bit, big-endian; a write selects an item)
//   +0x10  DMA register     (unimplemented — we do not advertise DMA)
type fwcfg struct {
	selector uint16
	offset   int
}

// fw_cfg selector keys we answer.
const (
	fwCfgSignature uint16 = 0x0000 // "QEMU"
	fwCfgID        uint16 = 0x0001 // interface feature bitmap
	fwCfgFileDir   uint16 = 0x0019 // file directory
)

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
		return []byte{0, 0, 0, 0} // file count = 0 (big-endian)
	}
	return nil
}
