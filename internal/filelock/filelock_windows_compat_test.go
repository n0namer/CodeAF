//go:build windows

package filelock

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

const legacyLockPathEnv = "CODEAF_FILELOCK_LEGACY_TEST_PATH"

func TestWindowsExclusiveLockExcludesLegacyByteZeroProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.lock")
	if err := os.WriteFile(path, []byte(`{"pid":4242}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsLegacyByteZeroLockHelper$")
	cmd.Env = append(os.Environ(), legacyLockPathEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "legacy-locked\n" {
		t.Fatalf("legacy helper startup = %q, %v", line, err)
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := Lock(file, true, true); !IsBusy(err) {
		t.Fatalf("exclusive lock against legacy byte-zero owner = %v, want busy", err)
	}
	if _, err := fmt.Fprintln(stdin, "release"); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
	if err := Lock(file, true, true); err != nil {
		t.Fatalf("exclusive lock after legacy owner exited = %v", err)
	}
	if err := Unlock(file); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsLegacyByteZeroLockHelper(t *testing.T) {
	path := os.Getenv(legacyLockPathEnv)
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var legacy windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &legacy); err != nil {
		t.Fatal(err)
	}
	defer windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &legacy)
	if _, err := fmt.Fprintln(os.Stdout, "legacy-locked"); err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
