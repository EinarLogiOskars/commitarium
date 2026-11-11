package outputs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

// The same helper's file of the same name is a new version of it; unchanged
// files stay as they are, older versions can be read and restored, and
// deleting the output removes them all.
func TestCollectVersionsAHelpersFiles(t *testing.T) {
	store, _ := NewStore(t.TempDir())
	from := t.TempDir()
	write(t, filepath.Join(from, Dir, "deck.pdf"), []byte("v1"))
	write(t, filepath.Join(from, Dir, "deck.html"), []byte("<h1>v1</h1>"))
	// Another helper's deck.pdf came first: this one's is renamed, and still
	// versioned by its own name.
	other := t.TempDir()
	write(t, filepath.Join(other, Dir, "deck.pdf"), []byte("someone else's"))
	if _, err := store.Collect(t.Context(), "prj_a", other, "tsk_other", "Other"); err != nil {
		t.Fatal(err)
	}
	first, err := store.Collect(t.Context(), "prj_a", from, "tsk_1", "Deck")
	if err != nil || strings.Join(first, ",") != "deck.html,deck-2.pdf" {
		t.Fatalf("first = %v, %v", first, err)
	}
	if again, _ := store.Collect(t.Context(), "prj_a", from, "tsk_1", "Deck"); len(again) != 0 {
		t.Fatalf("collecting the same files again made %v", again)
	}
	write(t, filepath.Join(from, Dir, "deck.pdf"), []byte("v2"))
	second, err := store.Collect(t.Context(), "prj_a", from, "tsk_1", "Deck")
	if err != nil || strings.Join(second, ",") != "deck-2.pdf" {
		t.Fatalf("second = %v, %v", second, err)
	}
	files, _ := store.List(t.Context(), "prj_a")
	if len(files) != 3 || files[0].Name != "deck-2.pdf" || files[0].Version != 2 || len(files[0].History) != 1 {
		t.Fatalf("files = %+v", files)
	}
	versions, err := store.Versions(t.Context(), "prj_a", "deck-2.pdf")
	if err != nil || len(versions) != 2 || versions[0].Number != 2 || versions[1].Number != 1 {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
	path, err := store.VersionPath(t.Context(), "prj_a", "deck-2.pdf", 1)
	if contents, _ := os.ReadFile(path); err != nil || string(contents) != "v1" {
		t.Fatalf("version 1 = %q, %v", contents, err)
	}
	if _, err := store.VersionPath(t.Context(), "prj_a", "deck-2.pdf", 7); err == nil {
		t.Fatal("a version that never was was served")
	}
	restored, err := store.Restore(t.Context(), "prj_a", "deck-2.pdf", 1)
	if err != nil || restored.Version != 3 || len(restored.History) != 2 {
		t.Fatalf("restored = %+v, %v", restored, err)
	}
	latest, _ := store.Path(t.Context(), "prj_a", "deck-2.pdf")
	if contents, _ := os.ReadFile(latest); string(contents) != "v1" {
		t.Fatalf("latest after restore = %q", contents)
	}
	into := t.TempDir()
	copied, err := store.CopyLatest(t.Context(), "prj_a", "tsk_1", into)
	if err != nil || len(copied) != 2 {
		t.Fatalf("copied = %v, %v", copied, err)
	}
	if contents, _ := os.ReadFile(filepath.Join(into, Dir, "deck.pdf")); string(contents) != "v1" {
		t.Fatalf("copied deck.pdf = %q", contents)
	}
	if err := store.Delete(t.Context(), "prj_a", "deck-2.pdf"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.VersionPath(t.Context(), "prj_a", "deck-2.pdf", 1); err == nil {
		t.Fatal("a deleted output's version is still served")
	}
}

// Older versions count toward the limit and go before whole files.
func TestTrimDropsOlderVersionsFirst(t *testing.T) {
	directory := t.TempDir()
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	files := []File{{Name: "a.md", MadeAt: base}}
	for number := 1; number < MaxFiles; number++ {
		files[0].History = append(files[0].History, Version{Number: number, MadeAt: base.Add(time.Duration(number) * time.Minute)})
	}
	files = append(files, File{Name: "b.md", MadeAt: base.Add(-time.Hour)})
	trimmed := trim(directory, files)
	if len(trimmed) != 2 || len(trimmed[0].History) != MaxFiles-2 || trimmed[0].History[0].Number != 2 {
		t.Fatalf("trimmed = %d files, %d versions", len(trimmed), len(trimmed[0].History))
	}
}

// Live lists what a helper has in outputs/ now, as Collect would name it.
func TestLiveListsAHelpersFilesNow(t *testing.T) {
	from := t.TempDir()
	write(t, filepath.Join(from, Dir, "cv.html"), []byte("<p>CV</p>"))
	write(t, filepath.Join(from, Dir, "parts", "intro.md"), []byte("# Hi"))
	write(t, filepath.Join(from, Dir, ".tmp"), []byte("x"))
	if err := os.Symlink("/etc/passwd", filepath.Join(from, Dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	live := Live(from)
	if len(live) != 2 || live[0].Name != "cv.html" || live[1].Name != "parts-intro.md" {
		t.Fatalf("live = %+v", live)
	}
	if Live(t.TempDir()) != nil {
		t.Fatal("a helper without outputs/ lists files")
	}
}

func TestCleanNameDropsAFolderItsFilesRepeat(t *testing.T) {
	cases := map[string]string{
		"concept-a/concept-a-app-icon-1024.png": "concept-a-app-icon-1024.png",
		"logos/concept-a.svg":                   "logos-concept-a.svg",
		"deck.pdf":                              "deck.pdf",
	}
	for relative, want := range cases {
		if got := cleanName(relative); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", relative, got, want)
		}
	}
}

func TestWithoutSourcesHidesThePageAPDFWasMadeFrom(t *testing.T) {
	got := WithoutSources([]string{"deck.html", "deck.pdf", "notes.html", "Logo.HTM", "logo.pdf"})
	want := []string{"deck.pdf", "notes.html", "logo.pdf"}
	if !slices.Equal(got, want) {
		t.Fatalf("WithoutSources = %v, want %v", got, want)
	}
}
