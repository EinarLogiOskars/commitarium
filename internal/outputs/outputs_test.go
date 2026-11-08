package outputs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Collect keeps a helper's files under names of their own, skipping what
// it should not hand back, and never replaces an earlier file.
func TestCollectKeepsAHelpersFiles(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	from := t.TempDir()
	write(t, filepath.Join(from, Dir, "report.md"), []byte("# Report"))
	write(t, filepath.Join(from, Dir, "data", "sales 2026.csv"), []byte("a,b"))
	write(t, filepath.Join(from, Dir, ".hidden"), []byte("x"))
	write(t, filepath.Join(from, Dir, "big.bin"), make([]byte, MaxBytes+1))
	write(t, filepath.Join(from, "outside.md"), []byte("no"))
	if err := os.Symlink("/etc/passwd", filepath.Join(from, Dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	names, err := store.Collect(t.Context(), "prj_a", from, "tsk_1", "Report")
	if err != nil || strings.Join(names, ",") != "data-sales 2026.csv,report.md" {
		t.Fatalf("names = %v, %v", names, err)
	}
	again, err := store.Collect(t.Context(), "prj_a", from, "tsk_2", "Report again")
	if err != nil || strings.Join(again, ",") != "data-sales 2026-2.csv,report-2.md" {
		t.Fatalf("again = %v, %v", again, err)
	}
	files, err := store.List(t.Context(), "prj_a")
	if err != nil || len(files) != 4 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	path, err := store.Path(t.Context(), "prj_a", "report-2.md")
	if contents, _ := os.ReadFile(path); err != nil || string(contents) != "# Report" {
		t.Fatalf("path = %q, %v", path, err)
	}
	if _, err := store.Path(t.Context(), "prj_a", "../index.json"); err == nil {
		t.Fatal("a path outside the files was served")
	}
	if _, err := store.List(t.Context(), "../etc"); err == nil {
		t.Fatal("an unknown owner was listed")
	}
	if err := store.Delete(t.Context(), "prj_a", "report.md"); err != nil {
		t.Fatal(err)
	}
	if files, _ := store.List(t.Context(), "prj_a"); len(files) != 3 {
		t.Fatalf("after delete = %+v", files)
	}
	if err := store.DeleteProject(t.Context(), "prj_a"); err != nil {
		t.Fatal(err)
	}
	if files, _ := store.List(t.Context(), "prj_a"); len(files) != 0 {
		t.Fatalf("after deleting the project = %+v", files)
	}
}

// A helper with no outputs folder hands nothing back.
func TestCollectWithoutOutputs(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	names, err := store.Collect(t.Context(), TopLevel, t.TempDir(), "tsk_1", "Nothing")
	if err != nil || len(names) != 0 {
		t.Fatalf("names = %v, %v", names, err)
	}
}
