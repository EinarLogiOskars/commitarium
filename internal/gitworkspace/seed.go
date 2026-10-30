package gitworkspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Files Commitarium writes into a managed checkout for its agents, such as
// a written plan that its first slice commits (PLAN_MODE_PLAN.md PM6). Until
// a commit tracks one, it is Commitarium's copy, not a change the agent left
// behind, so the clean-checkout checks look past it.

// seededList names, inside a checkout's .git directory, the paths
// Commitarium wrote into it, one per line.
const seededList = "info/commitarium-seeded"

// SeedFile writes contents at path (relative, with slashes) in checkout,
// unless a file is already there, as when the branch already has it: an
// existing file is never overwritten. It reports whether it wrote the file.
func SeedFile(checkout, path string, contents []byte) (bool, error) {
	local := filepath.FromSlash(path)
	if !filepath.IsLocal(local) || strings.HasPrefix(path, ".git/") || path == ".git" {
		return false, fmt.Errorf("seed %q: not a path inside the checkout", path)
	}
	target := filepath.Join(checkout, local)
	if _, err := os.Lstat(target); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(target, contents, 0o644); err != nil {
		return false, err
	}
	seeded := seededPaths(checkout)
	if slices.Contains(seeded, path) {
		return true, nil
	}
	list := filepath.Join(checkout, ".git", filepath.FromSlash(seededList))
	if err := os.MkdirAll(filepath.Dir(list), 0o755); err != nil {
		return true, err
	}
	file, err := os.OpenFile(list, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return true, err
	}
	defer func() { _ = file.Close() }()
	_, err = file.WriteString(path + "\n")
	return true, err
}

// seededPaths are the paths Commitarium wrote into checkout.
func seededPaths(checkout string) []string {
	contents, err := os.ReadFile(filepath.Join(checkout, ".git", filepath.FromSlash(seededList)))
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(contents), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// withoutSeeded drops from git status --porcelain output the untracked
// files Commitarium wrote into checkout.
func withoutSeeded(checkout, status string) string {
	seeded := seededPaths(checkout)
	if len(seeded) == 0 || status == "" {
		return status
	}
	kept := make([]string, 0)
	for _, line := range strings.Split(status, "\n") {
		if path, untracked := strings.CutPrefix(line, "?? "); untracked && slices.Contains(seeded, path) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
