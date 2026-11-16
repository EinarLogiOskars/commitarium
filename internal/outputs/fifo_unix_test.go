//go:build unix

package outputs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A pipe swapped in for a listed file is refused at once, not waited on
// while Collect holds the store.
func TestAPipeIsRefusedWithoutWaiting(t *testing.T) {
	from := t.TempDir()
	if err := os.MkdirAll(filepath.Join(from, Dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(from, Dir, "report.txt"), 0o600); err != nil {
		t.Skip("no pipes here:", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := OpenLive(from, Pending{relative: "report.txt"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a pipe was opened as a file")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("opening a pipe waited")
	}
}
