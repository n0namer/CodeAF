//go:build windows

package filelock

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsLockedPayloadRemainsReadable(t *testing.T) {
	for _, payload := range []string{"", `{"pid":4242,"store":"graph.db"}`, string(bytes.Repeat([]byte("x"), 4096))} {
		t.Run(stringSizeName(payload), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "resident.lock")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(path, os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if err := Lock(file, true, true); err != nil {
				t.Fatal(err)
			}
			got := make([]byte, len(payload))
			n, err := file.ReadAt(got, 0)
			if err != nil || n != len(payload) || string(got) != payload {
				t.Fatalf("read payload through lock handle: got %q, n %d, err %v", got, n, err)
			}
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != int64(len(payload)) {
				t.Fatalf("lock changed file size: got %d, want %d", info.Size(), len(payload))
			}
			if err := Unlock(file); err != nil {
				t.Fatal(err)
			}
			got, err = os.ReadFile(path)
			if err != nil || string(got) != payload {
				t.Fatalf("payload after unlock: got %q, err %v", got, err)
			}
		})
	}
}

func stringSizeName(payload string) string {
	if len(payload) == 0 {
		return "empty"
	}
	if len(payload) == 4096 {
		return "page"
	}
	return "holder"
}

func TestWindowsSharedLocksExcludeWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	var handles []*os.File
	for range 3 {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		handles = append(handles, file)
	}
	for _, file := range handles[:2] {
		if err := Lock(file, false, true); err != nil {
			t.Fatalf("shared lock: %v", err)
		}
	}
	if err := Lock(handles[2], true, true); !IsBusy(err) {
		t.Fatalf("writer with two readers = %v, want busy", err)
	}
	for _, file := range handles[:2] {
		if err := Unlock(file); err != nil {
			t.Fatal(err)
		}
	}
	if err := Lock(handles[2], true, true); err != nil {
		t.Fatalf("writer after readers released: %v", err)
	}
	if err := Unlock(handles[2]); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsClosedHandleErrorsAreNotBusy(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{"lock": Lock(file, true, true), "unlock": Unlock(file)} {
		if err == nil || IsBusy(err) {
			t.Fatalf("%s closed handle = %v, want non-busy error", name, err)
		}
	}
	if IsBusy(nil) {
		t.Fatal("nil error classified as busy")
	}
}

func TestWindowsLockRollbackFailureIsNotBusy(t *testing.T) {
	acquireErr := windows.ERROR_LOCK_VIOLATION
	rollbackErr := windows.ERROR_ACCESS_DENIED
	var calls []uint32
	lock := func(_ windows.Handle, _, _, _, _ uint32, overlapped *windows.Overlapped) error {
		calls = append(calls, overlapped.OffsetHigh)
		if len(calls) == 2 {
			return acquireErr
		}
		return nil
	}
	unlock := func(_ windows.Handle, _, _, _ uint32, overlapped *windows.Overlapped) error {
		if overlapped.Offset != 0 || overlapped.OffsetHigh != 0 {
			t.Fatalf("rollback range = (%d, %d), want byte zero", overlapped.OffsetHigh, overlapped.Offset)
		}
		return rollbackErr
	}
	err := lockRanges(windows.Handle(1), 0, lock, unlock)
	if !errors.Is(err, acquireErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("rollback error = %v, want both acquisition and rollback causes", err)
	}
	if IsBusy(err) {
		t.Fatalf("rollback error %v classified as busy", err)
	}
	if len(calls) != 2 {
		t.Fatalf("lock calls = %v, want legacy then current range", calls)
	}
}

func TestWindowsLockAcquisitionFailureRollsBackFirstRange(t *testing.T) {
	acquireErr := windows.ERROR_LOCK_VIOLATION
	var ranges []windows.Overlapped
	var unlocked *windows.Overlapped
	lock := func(_ windows.Handle, _, _, _, _ uint32, overlapped *windows.Overlapped) error {
		ranges = append(ranges, *overlapped)
		if len(ranges) == 2 {
			return acquireErr
		}
		return nil
	}
	unlock := func(_ windows.Handle, _, _, _ uint32, overlapped *windows.Overlapped) error {
		copy := *overlapped
		unlocked = &copy
		return nil
	}

	err := lockRanges(windows.Handle(1), 0, lock, unlock)
	if !IsBusy(err) {
		t.Fatalf("second-range acquisition error = %v, want busy", err)
	}
	if len(ranges) != 2 || ranges[0] != (windows.Overlapped{}) || ranges[1] != lockRange() {
		t.Fatalf("acquired ranges = %#v, want byte zero then high compatibility range", ranges)
	}
	if unlocked == nil || *unlocked != (windows.Overlapped{}) {
		t.Fatalf("rollback range = %#v, want byte zero", unlocked)
	}
}
