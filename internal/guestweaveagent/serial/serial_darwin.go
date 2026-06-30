//go:build darwin

package serial

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// defaultDeviceDarwin is the /dev node a single virtio serial port
// (VZVirtioConsoleDeviceSerialPortConfiguration) presents on a macOS guest —
// the same name VirtualBuddy's guest agent opens.
const defaultDeviceDarwin = "/dev/cu.virtio"

// openDevice opens the virtio serial device (defaulting to /dev/cu.virtio), puts
// it in raw mode and returns it. It retries briefly: the device node exists as
// soon as the VM boots, but the agent may start a touch earlier.
func openDevice(dev string) (*os.File, error) {
	if dev == "" {
		dev = defaultDeviceDarwin
	}
	var (
		f   *os.File
		err error
	)
	for range 30 {
		f, err = os.OpenFile(dev, os.O_RDWR|syscall.O_NOCTTY, 0)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("open serial %s: %w", dev, err)
	}
	if err := makeRaw(int(f.Fd())); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("raw mode %s: %w", dev, err)
	}
	return f, nil
}

// makeRaw applies a cfmakeraw(3)-equivalent termios so the binary proto framing
// is not mangled by the terminal line discipline.
func makeRaw(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
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
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
}
