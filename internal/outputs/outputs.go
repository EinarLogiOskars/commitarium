// Package outputs keeps the files the assistant's helpers make
// (HELPERS_PLAN.md): reports, decks, drafts, exports. A helper writes them
// into Dir in its working directory; each time it finishes a turn, Collect
// copies them into the store, one directory per project or for the
// top-level chats, where the app lists, shows, and exports them.
//
// A file the same helper makes again under the same name is a new version
// of it (H8): the latest is in files/, the older ones in
// versions/<name>/<number>, and any of them can be restored as the latest.
package outputs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dir is where a helper puts what it makes, in its working directory.
const Dir = "outputs"

const (
	// MaxBytes is the largest file kept; larger ones are skipped.
	MaxBytes = 20 * 1024 * 1024
	// MaxFiles is how many files an owner keeps, older versions included:
	// the oldest older versions go first, then the oldest files.
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

// File is one output, as the app lists it: its latest version.
type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// MadeAt is when its latest version was collected; Helper and Title
	// name the helper that made it and its task.
	MadeAt time.Time `json:"made_at"`
	Helper string    `json:"helper,omitempty"`
	Title  string    `json:"title,omitempty"`
	// Source is the helper's own name for it, which Name differs from when
	// another helper's file had that name first: the helper's later file of
	// that name is its next version.
	Source string `json:"source,omitempty"`
	// Version numbers the latest version, from 1; History lists the older
	// ones, oldest first.
	Version int       `json:"version,omitempty"`
	History []Version `json:"history,omitempty"`
}

// Version is one version of an output.
type Version struct {
	Number int       `json:"number"`
	Size   int64     `json:"size"`
	MadeAt time.Time `json:"made_at"`
}

// Latest is the file's latest version number.
func (file File) Latest() int {
	return max(file.Version, 1)
}

// SourceName is the helper's own name for the file.
func (file File) SourceName() string {
	if file.Source != "" {
		return file.Source
	}
	return file.Name
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

// Path answers where an output's latest version is on disk, to serve it.
func (store *Store) Path(_ context.Context, owner, name string) (string, error) {
	directory, err := store.directory(owner)
	if err != nil {
		return "", err
	}
	if !ValidName(name) {
		return "", ErrNotFound
	}
	return regularFile(filepath.Join(directory, "files", name))
}

// find reads the owner's index and the output named name in it.
func (store *Store) find(owner, name string) (string, []File, int, error) {
	directory, err := store.directory(owner)
	if err != nil {
		return "", nil, -1, err
	}
	if !ValidName(name) {
		return "", nil, -1, ErrNotFound
	}
	files, err := store.readIndex(directory)
	if err != nil {
		return "", nil, -1, err
	}
	index := slices.IndexFunc(files, func(file File) bool { return file.Name == name })
	if index < 0 {
		return "", nil, -1, ErrNotFound
	}
	return directory, files, index, nil
}

// Versions answers every version of an output, newest first.
func (store *Store) Versions(_ context.Context, owner, name string) ([]Version, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	_, files, index, err := store.find(owner, name)
	if err != nil {
		return nil, err
	}
	file := files[index]
	versions := []Version{{Number: file.Latest(), Size: file.Size, MadeAt: file.MadeAt}}
	for at := len(file.History) - 1; at >= 0; at-- {
		versions = append(versions, file.History[at])
	}
	return versions, nil
}

// VersionPath answers where one version of an output is on disk.
func (store *Store) VersionPath(_ context.Context, owner, name string, number int) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	directory, files, index, err := store.find(owner, name)
	if err != nil {
		return "", err
	}
	return versionPath(directory, files[index], number)
}

func versionPath(directory string, file File, number int) (string, error) {
	if number == file.Latest() {
		return regularFile(filepath.Join(directory, "files", file.Name))
	}
	if !slices.ContainsFunc(file.History, func(version Version) bool { return version.Number == number }) {
		return "", ErrNotFound
	}
	return regularFile(filepath.Join(directory, "versions", file.Name, strconv.Itoa(number)))
}

func regularFile(path string) (string, error) {
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return "", ErrNotFound
	}
	return path, nil
}

// Restore makes an older version of an output its latest, as a new version
// with that version's contents, so nothing is lost.
func (store *Store) Restore(_ context.Context, owner, name string, number int) (File, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	directory, files, index, err := store.find(owner, name)
	if err != nil {
		return File{}, err
	}
	if number == files[index].Latest() {
		return files[index], nil
	}
	path, err := versionPath(directory, files[index], number)
	if err != nil {
		return File{}, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return File{}, ErrUnavailable
	}
	if err := newVersion(directory, &files[index], contents, time.Now().UTC()); err != nil {
		return File{}, err
	}
	restored := files[index]
	return restored, store.writeIndex(directory, trim(directory, files))
}

// Delete removes one output with all its versions.
func (store *Store) Delete(_ context.Context, owner, name string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	directory, files, index, err := store.find(owner, name)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, "files", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if err := os.RemoveAll(filepath.Join(directory, "versions", name)); err != nil {
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

// Pending is a file a helper has in Dir now, before it is collected.
type Pending struct {
	// Name is the name the store would take it under from this helper;
	// Path is where it is.
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	MadeAt time.Time `json:"made_at"`
	Path   string    `json:"-"`
}

// Live lists the files a helper has in Dir under from right now, as Collect
// would take them: links, empty and hidden files, and files over MaxBytes
// are left out, as is everything past MaxCollected.
func Live(from string) []Pending {
	source := filepath.Join(from, Dir)
	if info, err := os.Lstat(source); err != nil || !info.IsDir() {
		return nil
	}
	var found []Pending
	_ = filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
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
		relative, _ := filepath.Rel(source, path)
		name := cleanName(relative)
		if slices.ContainsFunc(found, func(other Pending) bool { return other.Name == name }) {
			return nil
		}
		found = append(found, Pending{Name: name, Size: info.Size(), MadeAt: info.ModTime().UTC(), Path: path})
		if len(found) == MaxCollected {
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// Collect copies the files a helper left in Dir under from into the owner's
// outputs and answers the names of those that are new or changed. A file
// the helper made before under the same name becomes its next version, and
// one whose contents are the same is left as it is; a name another helper's
// file has is renamed. Files in folders keep their path in the name; links,
// empty files, and files over MaxBytes are skipped, as is everything past
// MaxCollected. Collecting the same files again changes nothing.
func (store *Store) Collect(_ context.Context, owner, from, helper, title string) ([]string, error) {
	directory, err := store.directory(owner)
	if err != nil {
		return nil, err
	}
	found := Live(from)
	if len(found) == 0 {
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
	for _, pending := range found {
		contents, err := os.ReadFile(pending.Path)
		if err != nil {
			continue
		}
		index := slices.IndexFunc(files, func(file File) bool {
			return helper != "" && file.Helper == helper && file.SourceName() == pending.Name
		})
		if index >= 0 {
			current, err := os.ReadFile(filepath.Join(directory, "files", files[index].Name))
			if err == nil && bytes.Equal(current, contents) {
				continue
			}
			if err := newVersion(directory, &files[index], contents, now); err != nil {
				return names, err
			}
			files[index].Title = title
			names = append(names, files[index].Name)
			continue
		}
		name := freeName(files, pending.Name)
		if err := os.WriteFile(filepath.Join(directory, "files", name), contents, 0o600); err != nil {
			return names, ErrUnavailable
		}
		file := File{Name: name, Size: int64(len(contents)), MadeAt: now, Helper: helper, Title: title, Version: 1}
		if name != pending.Name {
			file.Source = pending.Name
		}
		files = append(files, file)
		names = append(names, name)
	}
	return names, store.writeIndex(directory, trim(directory, files))
}

// CopyLatest copies the latest version of each file a helper made into Dir
// under to, by the helper's own names, so a new helper continues from them,
// and answers those names.
func (store *Store) CopyLatest(_ context.Context, owner, helper, to string) ([]string, error) {
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
	if err := os.MkdirAll(filepath.Join(to, Dir), 0o700); err != nil {
		return nil, ErrUnavailable
	}
	var names []string
	for _, file := range files {
		if helper == "" || file.Helper != helper {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(directory, "files", file.Name))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(to, Dir, file.SourceName()), contents, 0o600); err != nil {
			return names, ErrUnavailable
		}
		names = append(names, file.SourceName())
	}
	return names, nil
}

// newVersion moves a file's latest version into its history and writes
// contents as the next one.
func newVersion(directory string, file *File, contents []byte, now time.Time) error {
	older := filepath.Join(directory, "versions", file.Name)
	if err := os.MkdirAll(older, 0o700); err != nil {
		return ErrUnavailable
	}
	latest := filepath.Join(directory, "files", file.Name)
	number := file.Latest()
	if err := os.Rename(latest, filepath.Join(older, strconv.Itoa(number))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if err := os.WriteFile(latest, contents, 0o600); err != nil {
		return ErrUnavailable
	}
	file.History = append(file.History, Version{Number: number, Size: file.Size, MadeAt: file.MadeAt})
	file.Version, file.Size, file.MadeAt = number+1, int64(len(contents)), now
	return nil
}

// trim keeps at most MaxFiles files and older versions: the oldest older
// version goes first, and a whole file only when none is left.
func trim(directory string, files []File) []File {
	count := len(files)
	for _, file := range files {
		count += len(file.History)
	}
	for ; count > MaxFiles; count-- {
		oldestFile, oldestVersion := -1, -1
		for index, file := range files {
			for at, version := range file.History {
				if oldestFile < 0 || version.MadeAt.Before(files[oldestFile].History[oldestVersion].MadeAt) {
					oldestFile, oldestVersion = index, at
				}
			}
		}
		if oldestFile >= 0 {
			file := &files[oldestFile]
			_ = os.Remove(filepath.Join(directory, "versions", file.Name, strconv.Itoa(file.History[oldestVersion].Number)))
			file.History = slices.Delete(file.History, oldestVersion, oldestVersion+1)
			continue
		}
		oldest := 0
		for index, file := range files {
			if file.MadeAt.Before(files[oldest].MadeAt) {
				oldest = index
			}
		}
		_ = os.Remove(filepath.Join(directory, "files", files[oldest].Name))
		_ = os.RemoveAll(filepath.Join(directory, "versions", files[oldest].Name))
		files = slices.Delete(files, oldest, oldest+1)
	}
	return files
}

// cleanName makes a helper's file name (with any folders it was in) one the
// store keeps.
func cleanName(relative string) string {
	// A folder whose files already start with its name adds nothing:
	// concept-a/concept-a-icon.png is concept-a-icon.png, not
	// concept-a-concept-a-icon.png.
	parts := strings.Split(filepath.ToSlash(relative), "/")
	kept := make([]string, 0, len(parts))
	for index, part := range parts {
		if index+1 < len(parts) && part != "" &&
			strings.HasPrefix(strings.ToLower(parts[index+1]), strings.ToLower(part)) {
			continue
		}
		kept = append(kept, part)
	}
	name := strings.Join(kept, "-")
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

// IsSourceOf says whether name is the page a PDF among names was made
// from: deck.html beside deck.pdf. Commitarium keeps it, so a later change
// can edit it, and shows the user the PDF.
func IsSourceOf(name string, names []string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	if extension != ".html" && extension != ".htm" {
		return false
	}
	pdf := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) + ".pdf"
	return slices.ContainsFunc(names, func(other string) bool { return strings.ToLower(other) == pdf })
}

// WithoutSources answers names without the pages their PDFs were made from.
func WithoutSources(names []string) []string {
	shown := make([]string, 0, len(names))
	for _, name := range names {
		if !IsSourceOf(name, names) {
			shown = append(shown, name)
		}
	}
	return shown
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
