//go:build darwin

package hvmm

// cfiFlash emulates an Intel-command-set CFI NOR flash — the QEMU "virt" pflash
// that backs edk2's UEFI variable store. edk2's NorFlashDxe drives it by writing
// a command (Write-to-Buffer / Program / Block-Erase), then polling the same
// address until the status register reads "ready" (SR.7), before issuing Read
// Array to read back data. A plain RAM mapping can't model that handshake, so we
// leave the region unmapped (every access traps) and run the command state
// machine here over a backing byte slice (0xFF = erased).
//
// The device is 2×16-bit (32-bit bus), so commands arrive replicated on both
// half-words (e.g. 0x00700070) and the ready status reads back as 0x00800080.
type cfiFlash struct {
	data  []byte
	state flashState
	// pending write-buffer sequence
	bufAddr  uint64
	bufLeft  int
	bufStart bool
}

type flashState int

const (
	flashArray       flashState = iota // reads return array data
	flashStatus                        // reads return the ready status
	flashProgram                       // next write programs one word
	flashBufCount                      // next write is the buffer word count
	flashBufData                       // subsequent writes are buffered data
	flashEraseSetup                    // next write (0xD0) confirms a block erase
)

const (
	flashReady     uint64 = 0x0080_0080 // SR.7 set on both 16-bit chips
	flashBlockSize uint64 = 0x0004_0000 // 256 KiB erase block (QEMU virt pflash)
)

func newCFIFlash(size int) *cfiFlash {
	d := make([]byte, size)
	for i := range d {
		d[i] = 0xFF // erased
	}
	return &cfiFlash{data: d}
}

func (f *cfiFlash) read(off uint64, bytes int) uint64 {
	if f.state != flashArray {
		return flashReady // any non-array mode polls as ready
	}
	var v uint64
	for i := 0; i < bytes; i++ {
		if int(off)+i < len(f.data) {
			v |= uint64(f.data[off+uint64(i)]) << (8 * i)
		}
	}
	return v
}

func (f *cfiFlash) write(off uint64, bytes int, val uint64) {
	switch f.state {
	case flashProgram: // single-word program
		f.program(off, bytes, val)
		f.state = flashStatus
		return
	case flashBufCount: // word count - 1 (per chip, replicated)
		f.bufLeft = int(val&0xffff) + 1
		f.state = flashBufData
		return
	case flashBufData:
		f.program(off, bytes, val)
		if f.bufLeft--; f.bufLeft <= 0 {
			f.state = flashStatus // awaiting the 0xD0 confirm, then ready
		}
		return
	case flashEraseSetup:
		if val&0xff == 0xD0 { // confirm erase
			base := (off / flashBlockSize) * flashBlockSize
			for i := base; i < base+flashBlockSize && int(i) < len(f.data); i++ {
				f.data[i] = 0xFF
			}
		}
		f.state = flashStatus
		return
	}

	// Otherwise interpret a command (low byte; replicated across both chips).
	switch val & 0xff {
	case 0xFF: // Read Array
		f.state = flashArray
	case 0x70: // Read Status Register
		f.state = flashStatus
	case 0x50: // Clear Status Register
		f.state = flashArray
	case 0x40, 0x10: // Word Program setup
		f.state = flashProgram
	case 0xE8: // Write to Buffer
		f.state = flashBufCount
	case 0x20: // Block Erase setup
		f.state = flashEraseSetup
	case 0xD0: // stray confirm — treat as done
		f.state = flashStatus
	default:
		f.state = flashStatus
	}
}

// program clears bits per NOR semantics (data &= val) within the backing store.
func (f *cfiFlash) program(off uint64, bytes int, val uint64) {
	for i := 0; i < bytes; i++ {
		if int(off)+i < len(f.data) {
			f.data[off+uint64(i)] &= byte(val >> (8 * i))
		}
	}
}
