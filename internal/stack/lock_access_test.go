//go:build !plan9

package stack

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLockAccessClassifiers pins the classification table for off-flock lock
// file access errors. The classifiers are build-tag-free so this table runs
// on every platform, even though the retry behavior consuming them is
// windows-only.
func TestLockAccessClassifiers(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantDenied  bool
		wantSharing bool
	}{
		{name: "nil", err: nil},
		{name: "errno 5 access denied", err: windowsAccessDenied, wantDenied: true},
		{name: "errno 32 sharing violation", err: windowsSharingViolation, wantSharing: true},
		{name: "os.ErrPermission", err: os.ErrPermission, wantDenied: true},
		{name: "wrapped errno 5", err: fmt.Errorf("open x: %w", syscall.Errno(5)), wantDenied: true},
		{name: "wrapped errno 32", err: fmt.Errorf("open x: %w", syscall.Errno(32)), wantSharing: true},
		{name: "path error permission", err: &os.PathError{Op: "open", Path: "lock.excl", Err: os.ErrPermission}, wantDenied: true},
		{name: "access denied message", err: errors.New("Access is denied."), wantDenied: true},
		{name: "sharing violation message", err: errors.New("The process cannot access the file because it is being used by another process (sharing violation)."), wantSharing: true},
		{name: "unrelated file exists", err: errors.New("file exists")},
		{name: "unrelated not found", err: os.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lockAccessDeniedErr(tt.err); got != tt.wantDenied {
				t.Errorf("lockAccessDeniedErr(%v) = %v, want %v", tt.err, got, tt.wantDenied)
			}
			if got := lockSharingViolationErr(tt.err); got != tt.wantSharing {
				t.Errorf("lockSharingViolationErr(%v) = %v, want %v", tt.err, got, tt.wantSharing)
			}
		})
	}
}

// TestAcquireExclLockSurfacesPermissionOnStaleReclaim pins the owner-gone
// half of the reclaim contract: a stale dead-owner lock in a directory this
// process cannot write must produce a permission-mentioning error, not the
// busy sentinel ("another st command is running") — there is no other command
// to wait for. Runs on unix via the shared os.ErrPermission classification;
// the equivalent windows behavior rides the same code path.
func TestAcquireExclLockSurfacesPermissionOnStaleReclaim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory bit does not block file creation on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod is advisory for root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.excl")
	dead := lockFileContent(999999999, time.Now(), "dead")
	if err := os.WriteFile(path, []byte(dead), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	release, err := acquireExclLock(dir)
	if err == nil {
		release()
		t.Fatal("acquire in a read-only dir with a stale lock succeeded; want permission error")
	}
	if isBusyLockErr(err) {
		t.Fatalf("got the busy sentinel, want a permission error: %v", err)
	}
	if !strings.Contains(err.Error(), "permission") {
		t.Fatalf("error does not mention permissions: %v", err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != dead {
		t.Fatalf("stale lock changed: %q, %v", got, readErr)
	}
}

// TestAcquireExclLockKeepsBusyForLiveOwnerInReadOnlyDir pins the other half:
// when the recorded owner is still alive, a permission problem must NOT be
// surfaced — the truthful answer remains "another st command is running".
func TestAcquireExclLockKeepsBusyForLiveOwnerInReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory bit does not block file creation on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("chmod is advisory for root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "lock.excl")
	live := lockFileContent(os.Getpid(), time.Now(), "live")
	if err := os.WriteFile(path, []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	release, err := acquireExclLock(dir)
	if err == nil {
		release()
		t.Fatal("acquire with a live owner succeeded")
	}
	if !isBusyLockErr(err) {
		t.Fatalf("live owner should stay the busy sentinel, got: %v", err)
	}
}
