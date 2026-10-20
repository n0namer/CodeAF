//go:build windows

package filelock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// LockFileEx is range based. Every codeaf lock uses the same byte at a large
// offset, keeping the coordination range outside file contents that callers
// need to read while another process holds the lock.
const lockOffset uint64 = 1 << 40

func lockRange() windows.Overlapped {
	return windows.Overlapped{
		Offset:     uint32(lockOffset & 0xffffffff),
		OffsetHigh: uint32(lockOffset >> 32),
	}
}

func Lock(file *os.File, exclusive, nonBlocking bool) error {
	var flags uint32
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if nonBlocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	handle := windows.Handle(file.Fd())
	return lockRanges(handle, flags, windows.LockFileEx, windows.UnlockFileEx)
}

type lockFileExFunc func(windows.Handle, uint32, uint32, uint32, uint32, *windows.Overlapped) error
type unlockFileExFunc func(windows.Handle, uint32, uint32, uint32, *windows.Overlapped) error

func lockRanges(handle windows.Handle, flags uint32, lock lockFileExFunc, unlock unlockFileExFunc) error {
	// Byte zero remains the compatibility range because older codeaf processes
	// lock there. The high range keeps current coordination clear of payload bytes.
	legacy := windows.Overlapped{}
	if err := lock(handle, flags, 0, 1, 0, &legacy); err != nil {
		return err
	}
	current := lockRange()
	if err := lock(handle, flags, 0, 1, 0, &current); err != nil {
		rollback := unlock(handle, 0, 1, 0, &legacy)
		if rollback != nil {
			return &partialLockError{acquire: err, rollback: rollback}
		}
		return err
	}
	return nil
}

type partialLockError struct {
	acquire  error
	rollback error
}

func (e *partialLockError) Error() string {
	return fmt.Sprintf("acquiring compatibility lock range failed: %v; releasing first range failed: %v", e.acquire, e.rollback)
}

func (e *partialLockError) Unwrap() []error {
	return []error{e.acquire, e.rollback}
}

func Unlock(file *os.File) error {
	handle := windows.Handle(file.Fd())
	current := lockRange()
	errCurrent := windows.UnlockFileEx(handle, 0, 1, 0, &current)
	legacy := windows.Overlapped{}
	errLegacy := windows.UnlockFileEx(handle, 0, 1, 0, &legacy)
	return errors.Join(errCurrent, errLegacy)
}

func IsBusy(err error) bool {
	var partial *partialLockError
	if errors.As(err, &partial) {
		return false
	}
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}
