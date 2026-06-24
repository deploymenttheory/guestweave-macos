//go:build darwin

package hvmm

import (
	"bufio"
	"io"
)

// pl011 is a minimal ARM PL011 UART: enough register behaviour for firmware to
// stream characters out the data register and — when an input source is attached
// — read typed characters back in by polling the flag and data registers. This is
// the device edk2's serial console (SerialDxe / SimpleTextIn) targets.
type pl011 struct {
	out io.Writer
	in  chan byte // RX queue; nil means no input is attached
}

const (
	pl011DR = 0x000 // data register (write = TX byte; read = RX byte)
	pl011FR = 0x018 // flag register
)

func (u *pl011) read(off uint64, _ int) uint64 {
	switch off {
	case pl011FR:
		// TXFE (bit 7) is always set — TX never busy. RXFE (bit 4) is set only when
		// no byte is queued, so a polling console waits until a key arrives.
		fr := uint64(0x90)
		if len(u.in) > 0 {
			fr &^= 0x10 // clear RXFE: a byte is available to read
		}
		return fr
	case pl011DR:
		select {
		case b := <-u.in:
			return uint64(b)
		default:
			return 0
		}
	}
	return 0
}

func (u *pl011) write(off uint64, _ int, val uint64) {
	if off == pl011DR {
		_, _ = u.out.Write([]byte{byte(val)})
	}
}

// pumpInput streams bytes from r into the UART's receive queue until r ends. Run
// it as a goroutine to let a host stdin (or a scripted reader) drive the guest
// console. LF is translated to CR, which edk2's console expects for Enter.
func (u *pl011) pumpInput(r io.Reader) {
	if u.in == nil {
		return
	}
	br := bufio.NewReader(r)
	for {
		b, err := br.ReadByte()
		if err != nil {
			return
		}
		if b == '\n' {
			b = '\r'
		}
		u.in <- b
	}
}
