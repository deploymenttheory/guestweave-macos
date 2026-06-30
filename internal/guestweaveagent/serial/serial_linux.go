//go:build linux

package serial

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// linuxCandidates are the /dev nodes a virtio serial port can present on a Linux
// guest: the hvc console and the generic virtio-serial ports.
var linuxCandidates = []string{"/dev/hvc0", "/dev/vport0p1", "/dev/vport1p1"}

// openDevice opens the virtio serial device (discovering it when dev is empty),
// puts it in raw mode and returns it.
func openDevice(dev string) (*os.File, error) {
	var (
		f   *os.File
		err error
	)
	for range 30 {
		target := dev
		if target == "" {
			target = discoverLinux()
		}
		if target != "" {
			f, err = os.OpenFile(target, os.O_RDWR|syscall.O_NOCTTY, 0)
			if err == nil {
				if rerr := makeRaw(int(f.Fd())); rerr != nil {
					_ = f.Close()
					return nil, fmt.Errorf("raw mode %s: %w", target, rerr)
				}
				return f, nil
			}
		} else {
			err = fmt.Errorf("no virtio serial device found")
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("open serial device: %w", err)
}

// discoverLinux returns the first present virtio serial node, preferring any
// named virtio-ports symlink, then the well-known candidates.
func discoverLinux() string {
	if matches, _ := filepath.Glob("/dev/virtio-ports/*"); len(matches) > 0 {
		return matches[0]
	}
	for _, cand := range linuxCandidates {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// makeRaw applies a cfmakeraw(3)-equivalent termios so the binary proto framing
// is not mangled by the terminal line discipline.
func makeRaw(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB
	t.Cflag |= unix.CS8
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return unix.IoctlSetTermios(fd, unix.TCSETS, t)
}
