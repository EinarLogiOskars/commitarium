package documents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// A project's documents are stored by plain name, replaced by name, bounded,
// and removed with the project.
func TestDocumentsAreKeptWithTheProject(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, name := range []string{"../escape.txt", ".hidden", "a/b.txt", ""} {
		if _, err := store.Add(ctx, "prj_shop", name, []byte("x")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("name %q = %v", name, err)
		}
	}
	if _, err := store.Add(ctx, "prj_shop", "big.csv", make([]byte, MaxBytes+1)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an oversized document = %v", err)
	}
	if _, err := store.Add(ctx, "prj_shop", "Shopify API.md", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(ctx, "prj_shop", "Shopify API.md", []byte("v22")); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx, "prj_shop")
	if err != nil || len(listed) != 1 || listed[0].Name != "Shopify API.md" || listed[0].Size != 3 {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
	for index := range MaxDocuments - 1 {
		if _, err := store.Add(ctx, "prj_shop", fmt.Sprintf("sample-%d.csv", index), []byte("1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Add(ctx, "prj_shop", "one-more.csv", []byte("1")); !errors.Is(err, ErrTooMany) {
		t.Fatalf("one too many = %v", err)
	}
	if err := store.Delete(ctx, "prj_shop", "missing.csv"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing document = %v", err)
	}
	if err := store.DeleteProject(ctx, "prj_shop"); err != nil {
		t.Fatal(err)
	}
	if listed, _ := store.List(ctx, "prj_shop"); len(listed) != 0 {
		t.Fatalf("left after the project = %+v", listed)
	}
}

type projectList []project.Project

func (projects projectList) List(context.Context) ([]project.Project, error) { return projects, nil }

// A checkout of a project's repository gets its documents, ignored by git,
// and loses those since removed.
func TestDocumentsAreCopiedIntoCheckouts(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := store.Add(ctx, "prj_shop", "spec.md", []byte("# Spec")); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, ".git", "info", "exclude"), []byte("# git ls-files"), 0o644); err != nil {
		t.Fatal(err)
	}
	ready := store.ForCheckouts(projectList{
		{ID: "prj_other", ForgejoRepository: &project.ForgejoRepository{Owner: "commitarium", Name: "other"}},
		{ID: "prj_shop", ForgejoRepository: &project.ForgejoRepository{Owner: "commitarium", Name: "shop"}},
	})
	spec := workspace.CheckoutSpec{RepositoryOwner: "commitarium", RepositoryName: "shop"}
	for range 2 {
		if err := ready(ctx, spec, checkout); err != nil {
			t.Fatal(err)
		}
	}
	if contents, err := os.ReadFile(filepath.Join(checkout, Dir, "spec.md")); err != nil || string(contents) != "# Spec" {
		t.Fatalf("copied = %q, %v", contents, err)
	}
	exclude, _ := os.ReadFile(filepath.Join(checkout, ".git", "info", "exclude"))
	if string(exclude) != "# git ls-files\n/"+Dir+"/\n" {
		t.Fatalf("exclude = %q", exclude)
	}
	if err := store.Delete(ctx, "prj_shop", "spec.md"); err != nil {
		t.Fatal(err)
	}
	if err := ready(ctx, spec, checkout); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(checkout, Dir)); !os.IsNotExist(err) {
		t.Fatalf("removed documents stay: %v", err)
	}
	// A checkout of another repository is left alone.
	other := t.TempDir()
	if err := ready(ctx, workspace.CheckoutSpec{RepositoryOwner: "someone", RepositoryName: "shop"}, other); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 || strings.Contains(other, Dir) {
		t.Fatalf("another checkout got %v", entries)
	}
}

// A message's file never replaces another: a taken name gets a number. The
// top-level conversation, which has no page to remove files from, makes
// room by dropping its oldest.
func TestAttachedFilesKeepTheirOwnNames(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for _, want := range []string{"shot.png", "shot-2.png", "shot-3.png"} {
		if added, err := store.Attach(ctx, "prj_shop", "shot.png", []byte("png")); err != nil || added.Name != want {
			t.Fatalf("attached %+v, %v; want %s", added, err, want)
		}
	}
	if _, err := store.Attach(ctx, "prj_shop", "../shot.png", []byte("png")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unsafe name = %v", err)
	}
	if _, err := store.Attach(ctx, "other", "shot.png", []byte("png")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown owner = %v", err)
	}
	for index := range MaxDocuments {
		if _, err := store.Attach(ctx, TopLevel, fmt.Sprintf("shot-%02d.png", index), []byte("png")); err != nil {
			t.Fatal(err)
		}
		// Each file is older than the next.
		at := time.Now().Add(time.Duration(index-MaxDocuments) * time.Minute)
		directory, _ := store.directory(TopLevel)
		_ = os.Chtimes(filepath.Join(directory, fmt.Sprintf("shot-%02d.png", index)), at, at)
	}
	if _, err := store.Attach(ctx, TopLevel, "newest.png", []byte("png")); err != nil {
		t.Fatalf("the top level is full: %v", err)
	}
	listed, _ := store.List(ctx, TopLevel)
	if len(listed) != MaxDocuments || slices.ContainsFunc(listed, func(d Document) bool { return d.Name == "shot-00.png" }) {
		t.Fatalf("the oldest file was not the one dropped: %d files", len(listed))
	}
}

// Delivered files land in a working directory, which git ignores in a
// checkout, and their paths name them there; the line naming them reads back.
func TestDeliveredFilesReachAWorkingDirectory(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := store.Attach(ctx, "prj_shop", "shot (1).png", []byte("png")); err != nil {
		t.Fatal(err)
	}
	checkout := t.TempDir()
	if err := os.MkdirAll(filepath.Join(checkout, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := store.Deliver(ctx, "prj_shop", []string{"shot (1).png"}, checkout)
	if err != nil || !slices.Equal(paths, []string{Dir + "/shot (1).png"}) {
		t.Fatalf("delivered %v, %v", paths, err)
	}
	if contents, err := os.ReadFile(filepath.Join(checkout, filepath.FromSlash(paths[0]))); err != nil || string(contents) != "png" {
		t.Fatalf("the file in the checkout = %q, %v", contents, err)
	}
	if exclude, _ := os.ReadFile(filepath.Join(checkout, ".git", "info", "exclude")); !strings.Contains(string(exclude), "/"+Dir+"/") {
		t.Fatalf("git does not ignore the documents: %q", exclude)
	}
	if _, err := store.Deliver(ctx, "prj_shop", []string{"missing.png"}, checkout); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing file = %v", err)
	}
	text := WithAttached("Fix the header", []string{"shot (1).png", "spec.pdf"})
	if got := AttachedIn(text); !slices.Equal(got, []string{"shot (1).png", "spec.pdf"}) {
		t.Fatalf("read back %v from %q", got, text)
	}
	if !IsImage("a.PNG") || !IsImage("b.webp") || IsImage("c.svg") || IsImage("spec.pdf") {
		t.Fatal("images are png, jpg, gif, and webp")
	}
	if got := AttachedIn("Attached is a list.\n" + Attached([]string{"../x"})); len(got) != 0 {
		t.Fatalf("read back %v", got)
	}
}
