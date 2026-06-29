// Extended attributes (replacing the XAttr package / NSURL extended-attribute
// access) via the getxattr/setxattr syscalls.
//go:build darwin

package fsutil

import (
	"errors"
	"syscall"
	"unsafe"
)

// GetXattr reads an extended attribute. A missing attribute returns (nil, nil).
func GetXattr(path, name string) ([]byte, error) {
	size, err := xattr(syscall.SYS_GETXATTR, path, name, nil, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOATTR) {
			return nil, nil
		}
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	read, err := xattr(syscall.SYS_GETXATTR, path, name, &buf[0], uintptr(size))
	if err != nil {
		return nil, err
	}
	return buf[:read], nil
}

// SetXattr writes an extended attribute.
func SetXattr(path, name string, value []byte) error {
	var p *byte
	if len(value) > 0 {
		p = &value[0]
	}
	_, err := xattr(syscall.SYS_SETXATTR, path, name, p, uintptr(len(value)))
	return err
}

func xattr(trap uintptr, path, name string, value *byte, size uintptr) (int, error) {
	pathPtr, err := syscall.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	namePtr, err := syscall.BytePtrFromString(name)
	if err != nil {
		return 0, err
	}
	n, _, errno := syscall.Syscall6(trap,
		uintptr(unsafe.Pointer(pathPtr)), uintptr(unsafe.Pointer(namePtr)),
		uintptr(unsafe.Pointer(value)), size, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}
