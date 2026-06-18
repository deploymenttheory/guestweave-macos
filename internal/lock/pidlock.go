// Port of tart's PIDLock.swift: a write lock taken with fcntl(2) record
// locking so the lock holder's PID can be queried via F_GETLK. Swift's
// deinit close becomes an explicit Close method.
//go:build darwin

package lock

import (
	"errors"
	"syscall"

	weaveerrors "github.com/deploymenttheory/weave/internal/errors"
)

// PIDLock ports tart's PIDLock class.
type PIDLock struct {
	Path string
	fd   int
}

// NewPIDLock ports PIDLock.init(lockURL:).
func NewPIDLock(lockPath string) (*PIDLock, error) {
	fd, err := syscall.Open(lockPath, syscall.O_RDWR, 0)
	if err != nil {
		return nil, weaveerrors.ErrPIDLockMissing("failed to open lock file %s: %v", lockPath, err)
	}
	return &PIDLock{Path: lockPath, fd: fd}, nil
}

// Close releases the file descriptor (Swift's deinit).
func (l *PIDLock) Close() error {
	return syscall.Close(l.fd)
}

// Trylock attempts a non-blocking write lock; false means another process
// holds it.
func (l *PIDLock) Trylock() (bool, error) {
	locked, _, err := l.lockWrapper(syscall.F_SETLK, syscall.F_WRLCK, "failed to lock "+l.Path)
	return locked, err
}

// Lock blocks until the write lock is acquired.
func (l *PIDLock) Lock() error {
	_, _, err := l.lockWrapper(syscall.F_SETLKW, syscall.F_WRLCK, "failed to lock "+l.Path)
	return err
}

// Unlock releases the lock.
func (l *PIDLock) Unlock() error {
	_, _, err := l.lockWrapper(syscall.F_SETLK, syscall.F_UNLCK, "failed to unlock "+l.Path)
	return err
}

// PID returns the process ID currently holding the lock.
func (l *PIDLock) PID() (int32, error) {
	_, result, err := l.lockWrapper(syscall.F_GETLK, syscall.F_RDLCK, "failed to get lock "+l.Path+" status")
	if err != nil {
		return 0, err
	}
	return result.Pid, nil
}

func (l *PIDLock) lockWrapper(operation int, lockType int16, message string) (bool, syscall.Flock_t, error) {
	result := syscall.Flock_t{
		Start:  0,
		Len:    0,
		Pid:    0,
		Type:   lockType,
		Whence: 0, // SEEK_SET
	}

	if err := syscall.FcntlFlock(uintptr(l.fd), operation, &result); err != nil {
		if operation == syscall.F_SETLK && errors.Is(err, syscall.EAGAIN) {
			return false, result, nil
		}
		return false, result, weaveerrors.ErrPIDLockFailed("%s: %v", message, err)
	}

	return true, result, nil
}
