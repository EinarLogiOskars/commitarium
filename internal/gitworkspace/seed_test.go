package gitworkspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// The written plan Commitarium writes into a checkout is its copy, not a
// change the agent left: the clean checks look past it until a commit
// tracks it, and a file already there is never overwritten.
func TestASeededFileKeepsTheCheckoutClean(t *testing.T) {
	manager, root := newTestManager(t, nil)
	spec := initializeCheckout(t, root)
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	target := filepath.Join(root, spec.WorkspaceID)
	if wrote, err := SeedFile(target, "docs/plans/stock.md", []byte("# Stock\n")); err != nil || !wrote {
		t.Fatalf("seed = %v, %v", wrote, err)
	}
	spec.AlreadyReady, spec.RequireCleanBaseline = true, true
	if err := manager.Ensure(t.Context(), spec); err != nil {
		t.Fatalf("a clean baseline with the seeded plan: %v", err)
	}
	if wrote, err := SeedFile(target, "README.md", []byte("replaced\n")); err != nil || wrote {
		t.Fatalf("seeding over a file = %v, %v", wrote, err)
	}
	if contents, _ := os.ReadFile(filepath.Join(target, "README.md")); string(contents) != "test\n" {
		t.Fatalf("an existing file was overwritten: %q", contents)
	}
	if _, err := SeedFile(target, "../outside.md", []byte("x")); err == nil {
		t.Fatal("a path outside the checkout was seeded")
	}
	// Any other untracked file still counts.
	if err := os.WriteFile(filepath.Join(target, "notes.txt"), []byte("left behind\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.Ensure(t.Context(), spec); !errors.Is(err, workspace.ErrCheckoutConflict) {
		t.Fatalf("an agent's untracked file = %v", err)
	}
}
