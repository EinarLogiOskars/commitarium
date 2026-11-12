// Package outputs keeps the files the assistant's helpers make
// (HELPERS_PLAN.md): reports, decks, drafts, exports. A helper writes them
// into Dir in its working directory; once it finishes, Collect copies them
// into the store, one directory per project or for the top-level chats,
// where the app lists, shows, and exports them.
package outputs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Dir is where a helper puts what it makes, in its working directory.
const Dir = "outputs"

const (
	// MaxBytes is the largest file kept; larger ones are skipped.
	MaxBytes = 20 * 1024 * 1024
	// MaxFiles is how many files an owner keeps; the oldest go first.
	MaxFiles = 200
	// MaxCollected is how many files one helper hands back.
	MaxCollected = 20
)

var (
	ErrInvalid     = errors.New("invalid output")
	ErrNotFound    = errors.New("no such output")
	ErrUnavailable = errors.New("outputs are unavailable")
)

// TopLevel owns the files helpers make for chats outside any project; it is
// the conversation's scope, which no project ID can take.
const TopLevel = "commitarium"

var (
	ownerPattern = regexp.MustCompile(`^(prj_[A-Za-z0-9]{1,64}|` + TopLevel + `)$`)
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()-]{0,99}$`)
	unsafeChars  = regexp.MustCompile(`[^A-Za-z0-9 ._()-]+`)
)

// File is one output, as the app lists it.
type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// MadeAt is when it was collected; Helper and Title name the helper
	// that made it and its task.
	MadeAt time.Time `json:"made_at"`
	Helper string    `json:"helper,omitempty"`
	Title  string    `json:"title,omitempty"`
}

// Store keeps each owner's outputs in a directory of its own, with an index
// of who made them.
type Store struct {
	root string
	mu   sync.Mutex
}

func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create %s: %v", ErrUnavailable, root, err)
	}
	return &Store{root: root}, nil
}

// ValidName reports whether name is a plain file name the store keeps.
func ValidName(name string) bool {
	return namePattern.MatchString(name) && !strings.Contains(name, "..")
}

func (store *Store) directory(owner string) (string, error) {
	if !ownerPattern.MatchString(owner) {
		return "", fmt.Errorf("%w: unknown owner", ErrInvalid)
	}
	return filepath.Join(store.root, owner), nil
}

func (store *Store) readIndex(directory string) ([]File, error) {
	contents, err := os.ReadFile(filepath.Join(directory, "index.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	var files []File
	if err := json.Unmarshal(contents, &files); err != nil {
		return nil, ErrUnavailable
	}
	return files, nil
}

func (store *Store) writeIndex(directory string, files []File) error {
	contents, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return ErrUnavailable
	}
	temporary := filepath.Join(directory, ".index.json.tmp")
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return ErrUnavailable
	}
	if err := os.Rename(temporary, filepath.Join(directory, "index.json")); err != nil {
		return ErrUnavailable
	}
	return nil
}

// List answers the owner's outputs, newest first.
func (store *Store) List(_ context.Context, owner string) ([]File, error) {
	directory, err := store.directory(owner)
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	files, err := store.readIndex(directory)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(files, func(a, b File) int { return b.MadeAt.Compare(a.MadeAt) })
	return files, nil
}

// Path answers where an output is on disk, to serve it.
func (store *Store) Path(_ context.Context, owner, name string) (string, error) {
	directory, err := store.directory(owner)
	if err != nil {
		return "", err
	}
	if !ValidName(name) {
		return "", ErrNotFound
	}
	path := filepath.Join(directory, "files", name)
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return "", ErrNotFound
	}
	return path, nil
}

// Delete removes one output.
func (store *Store) Delete(_ context.Context, owner, name string) error {
	directory, err := store.directory(owner)
	if err != nil {
		return err
	}
	if !ValidName(name) {
		return ErrNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	files, err := store.readIndex(directory)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(files, func(file File) bool { return file.Name == name })
	if index < 0 {
		return ErrNotFound
	}
	if err := os.Remove(filepath.Join(directory, "files", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	return store.writeIndex(directory, slices.Delete(files, index, index+1))
}

// DeleteProject removes every output of a project being deleted.
func (store *Store) DeleteProject(_ context.Context, projectID string) error {
	directory, err := store.directory(projectID)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.RemoveAll(directory); err != nil {
		return ErrUnavailable
	}
	return nil
}

// Collect copies the files a helper left in Dir under from into the owner's
// outputs, renaming a file whose name is taken, and answers their names.
// Files in folders keep their path in the name; links, empty files, and
// files over MaxBytes are skipped, as is everything past MaxCollected.
func (store *Store) Collect(_ context.Context, owner, from, helper, title string) ([]string, error) {
	directory, err := store.directory(owner)
	if err != nil {
		return nil, err
	}
	source := filepath.Join(from, Dir)
	var found []string
	walkErr := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return filepath.SkipAll
			}
			return nil
		}
		if entry.IsDir() {
			if path != source && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > MaxBytes ||
			strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if found = append(found, path); len(found) == MaxCollected {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil || len(found) == 0 {
		return nil, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(directory, "files"), 0o700); err != nil {
		return nil, ErrUnavailable
	}
	files, err := store.readIndex(directory)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var names []string
	for _, path := range found {
		relative, _ := filepath.Rel(source, path)
		name := freeName(files, cleanName(relative))
		contents, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(directory, "files", name), contents, 0o600); err != nil {
			return names, ErrUnavailable
		}
		files = append(files, File{Name: name, Size: int64(len(contents)), MadeAt: now, Helper: helper, Title: title})
		names = append(names, name)
	}
	for len(files) > MaxFiles {
		oldest := 0
		for index, file := range files {
			if file.MadeAt.Before(files[oldest].MadeAt) {
				oldest = index
			}
		}
		_ = os.Remove(filepath.Join(directory, "files", files[oldest].Name))
		files = slices.Delete(files, oldest, oldest+1)
	}
	return names, store.writeIndex(directory, files)
}

// cleanName makes a helper's file name (with any folders it was in) one the
// store keeps.
func cleanName(relative string) string {
	name := strings.ReplaceAll(filepath.ToSlash(relative), "/", "-")
	name = unsafeChars.ReplaceAllString(name, "-")
	name = strings.ReplaceAll(name, "..", ".")
	name = strings.TrimLeft(name, " .-_()")
	if extension := filepath.Ext(name); len(name) > 100 {
		if len(extension) > 10 {
			extension = ""
		}
		name = strings.TrimRight(name[:100-len(extension)], " .") + extension
	}
	if name == "" || !ValidName(name) {
		return "output"
	}
	return name
}

// freeName answers name, or name-2, name-3 … when it is taken.
func freeName(files []File, name string) string {
	taken := func(candidate string) bool {
		return slices.ContainsFunc(files, func(file File) bool { return file.Name == candidate })
	}
	if !taken(name) {
		return name
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for number := 2; ; number++ {
		suffix := fmt.Sprintf("-%d", number)
		trimmed := base
		if len(trimmed)+len(suffix)+len(extension) > 100 {
			trimmed = trimmed[:100-len(suffix)-len(extension)]
		}
		if candidate := trimmed + suffix + extension; !taken(candidate) {
			return candidate
		}
	}
}
