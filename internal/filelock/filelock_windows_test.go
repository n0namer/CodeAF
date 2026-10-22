//go:build windows

package filelock

import (
	"bytes"
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
			defer Unlock(file)
			got, err := os.ReadFile(path)
			if err != nil || string(got) != payload {
				t.Fatalf("read payload under exclusive lock: got %q, err %v", got, err)
			}
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() != int64(len(payload)) {
				t.Fatalf("lock changed file size: got %d, want %d", info.Size(), len(payload))
			}
		})
	}
}

func TestWindowsLegacyByteZeroLockExcludesNewLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resident.lock")
	legacy, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	current, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()

	// Builds predating internal/filelock coordinate over byte zero. A current
	// exclusive lock must conflict with that range on a separate file handle.
	if err := windows.LockFileEx(windows.Handle(legacy.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		t.Fatal(err)
	}
	defer windows.UnlockFileEx(windows.Handle(legacy.Fd()), 0, 1, 0, &windows.Overlapped{})

	if err := Lock(current, true, true); !IsBusy(err) {
		t.Fatalf("new lock beside legacy byte-zero holder = %v, want busy", err)
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
