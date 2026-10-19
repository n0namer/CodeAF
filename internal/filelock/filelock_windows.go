//go:build windows

package filelock

import (
	"errors"
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
	overlapped := lockRange()
	return windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlapped)
}

func Unlock(file *os.File) error {
	overlapped := lockRange()
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped)
}

func IsBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}
