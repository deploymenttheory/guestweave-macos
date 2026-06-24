//go:build darwin

package hvmm

import "io"

// pl011 is a minimal ARM PL011 UART model: enough register behaviour for
// firmware to poll the flag register and stream characters out the data
// register. This is the device edk2's early (SEC/PEI) serial output targets.
type pl011 struct{ out io.Writer }

const (
	pl011DR = 0x000 // data register (write = TX byte; read = RX byte)
	pl011FR = 0x018 // flag register
)

func (u *pl011) read(off uint64, _ int) uint64 {
	if off == pl011FR {
		// TXFE (bit 7) + RXFE (bit 4): transmit holding empty, receive empty —
		// so firmware never blocks waiting for TX space and sees no input.
		return 0x90
	}
	return 0
}

func (u *pl011) write(off uint64, _ int, val uint64) {
	if off == pl011DR {
		_, _ = u.out.Write([]byte{byte(val)})
	}
}
