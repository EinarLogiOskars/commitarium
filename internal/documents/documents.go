// Package documents keeps the files a user attaches to a project in planning
// mode (ONE_CONVERSATION_PLAN.md phase 2): specs, API docs, sample exports.
// They are stored with the project in the coordinator and copied into each
// checkout of its repository, under Dir, which git ignores there, so the
// assistant, its helpers, and the work orders' agents read them where they
// work without the files ever being committed.
package documents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// Dir is where a checkout holds the project's documents.
const Dir = ".commitarium/documents"

const (
	MaxBytes     = 5 * 1024 * 1024
	MaxDocuments = 30
)

var (
	ErrInvalid     = errors.New("invalid document")
	ErrNotFound    = errors.New("no such document")
	ErrTooMany     = fmt.Errorf("a project holds at most %d documents", MaxDocuments)
	ErrUnavailable = errors.New("documents are unavailable")
)

var (
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._()-]{0,99}$`)
	// Documents belong to a project, or to the top-level conversation
	// (TopLevel), whose pasted screenshots and files have no project to go
	// with.
	ownerPattern = regexp.MustCompile(`^(prj_[A-Za-z0-9]{1,64}|` + TopLevel + `)$`)
)

// TopLevel owns the files attached in the Commitarium-wide conversation; it
// is the conversation's scope, which no project ID can take.
const TopLevel = "commitarium"

type Document struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	AddedAt time.Time `json:"added_at"`
}

// Store keeps each project's documents in a directory of its own.
type Store struct {
	root string
}

func NewStore(root string) (*Store, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: the documents' root must be absolute", ErrUnavailable)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create the documents' root", ErrUnavailable)
	}
	return &Store{root: root}, nil
}

// ValidName reports whether name is a plain file name a document may have.
func ValidName(name string) bool {
	return namePattern.MatchString(name) && !strings.Contains(name, "..")
}

func (store *Store) directory(projectID string) (string, error) {
	if !ownerPattern.MatchString(projectID) {
		return "", ErrNotFound
	}
	return filepath.Join(store.root, projectID), nil
}

// List answers the project's documents by name.
func (store *Store) List(_ context.Context, projectID string) ([]Document, error) {
	directory, err := store.directory(projectID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Document{}, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	documents := make([]Document, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !ValidName(entry.Name()) {
			continue
		}
		documents = append(documents, Document{Name: entry.Name(), Size: info.Size(), AddedAt: info.ModTime().UTC()})
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].Name < documents[j].Name })
	return documents, nil
}

// Add stores a document, replacing one of the same name.
func (store *Store) Add(ctx context.Context, projectID, name string, contents []byte) (Document, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) || len(contents) == 0 || len(contents) > MaxBytes {
		return Document{}, fmt.Errorf("%w: a document needs a plain file name and at most %d MB", ErrInvalid, MaxBytes/1024/1024)
	}
	directory, err := store.directory(projectID)
	if err != nil {
		return Document{}, err
	}
	existing, err := store.List(ctx, projectID)
	if err != nil {
		return Document{}, err
	}
	replaces := false
	for _, document := range existing {
		replaces = replaces || document.Name == name
	}
	if !replaces && len(existing) >= MaxDocuments {
		return Document{}, ErrTooMany
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Document{}, ErrUnavailable
	}
	path := filepath.Join(directory, name)
	temporary := filepath.Join(directory, ".upload-"+name)
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return Document{}, ErrUnavailable
	}
	if err := os.Rename(temporary, path); err != nil {
		return Document{}, ErrUnavailable
	}
	return Document{Name: name, Size: int64(len(contents)), AddedAt: time.Now().UTC()}, nil
}

// Attach stores a file attached to a message under a name of its own: when
// name is taken, a number goes before its extension ("shot-2.png"), so a
// message keeps the file it was sent with. The top-level conversation has
// no page to remove files from, so its oldest file makes room there.
func (store *Store) Attach(ctx context.Context, owner, name string, contents []byte) (Document, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return Document{}, fmt.Errorf("%w: a document needs a plain file name and at most %d MB", ErrInvalid, MaxBytes/1024/1024)
	}
	existing, err := store.List(ctx, owner)
	if err != nil {
		return Document{}, err
	}
	taken := make(map[string]bool, len(existing))
	for _, document := range existing {
		taken[document.Name] = true
	}
	extension := filepath.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for number := 2; taken[name]; number++ {
		name = fmt.Sprintf("%s-%d%s", base, number, extension)
	}
	if owner == TopLevel && len(existing) >= MaxDocuments {
		oldest := existing[0]
		for _, document := range existing {
			if document.AddedAt.Before(oldest.AddedAt) {
				oldest = document
			}
		}
		if err := store.Delete(ctx, owner, oldest.Name); err != nil {
			return Document{}, err
		}
	}
	return store.Add(ctx, owner, name, contents)
}

// Read answers one document's contents.
func (store *Store) Read(_ context.Context, projectID, name string) ([]byte, error) {
	directory, err := store.directory(projectID)
	if err != nil || !ValidName(name) {
		return nil, ErrNotFound
	}
	contents, err := os.ReadFile(filepath.Join(directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, ErrUnavailable
	}
	return contents, nil
}

// Delete removes one document.
func (store *Store) Delete(_ context.Context, projectID, name string) error {
	directory, err := store.directory(projectID)
	if err != nil || !ValidName(name) {
		return ErrNotFound
	}
	if err := os.Remove(filepath.Join(directory, name)); errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	} else if err != nil {
		return ErrUnavailable
	}
	return nil
}

// DeleteProject removes every document of a deleted project.
func (store *Store) DeleteProject(_ context.Context, projectID string) error {
	directory, err := store.directory(projectID)
	if err != nil {
		return nil
	}
	if err := os.RemoveAll(directory); err != nil {
		return ErrUnavailable
	}
	return nil
}

// CopyInto puts the project's documents in checkout's Dir, replacing what
// was there, and has git ignore them there.
func (store *Store) CopyInto(ctx context.Context, projectID, checkout string) error {
	documents, err := store.List(ctx, projectID)
	if err != nil {
		return err
	}
	target := filepath.Join(checkout, filepath.FromSlash(Dir))
	if len(documents) == 0 {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if err := ignore(checkout); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return ErrUnavailable
	}
	if len(documents) == 0 {
		return nil
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return ErrUnavailable
	}
	directory, _ := store.directory(projectID)
	for _, document := range documents {
		contents, err := os.ReadFile(filepath.Join(directory, document.Name))
		if err != nil {
			return ErrUnavailable
		}
		if err := os.WriteFile(filepath.Join(target, document.Name), contents, 0o644); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

// IsImage reports whether a document is an image an agent can look at:
// png, jpg, gif, or webp.
func IsImage(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// attachedPrefix starts the line that names a message's files, so whoever
// reads the message, and whatever passes it on, finds them (Attached).
const attachedPrefix = "Attached, in your working directory: "

// Attached is the line naming a message's files where the reader finds
// them. Names hold no comma, so the paths are joined by commas.
func Attached(names []string) string {
	if len(names) == 0 {
		return ""
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, Dir+"/"+name)
	}
	return attachedPrefix + strings.Join(paths, ", ")
}

// WithAttached is text followed by the line naming its files.
func WithAttached(text string, names []string) string {
	switch {
	case len(names) == 0:
		return text
	case strings.TrimSpace(text) == "":
		return Attached(names)
	default:
		return text + "\n\n" + Attached(names)
	}
}

// AttachedIn answers the files the Attached lines in text name.
func AttachedIn(text string) []string {
	var names []string
	for _, line := range strings.Split(text, "\n") {
		listed, ok := strings.CutPrefix(strings.TrimSpace(line), attachedPrefix)
		if !ok {
			continue
		}
		for _, path := range strings.Split(listed, ",") {
			name, ok := strings.CutPrefix(strings.TrimSpace(path), Dir+"/")
			if ok && ValidName(name) && !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// Deliver copies the named documents into directory's Dir, for a turn that
// reads them there, and answers their paths relative to directory. Unlike
// CopyInto it leaves the others alone, so a message's files reach a working
// directory that was set up before they were attached.
func (store *Store) Deliver(ctx context.Context, owner string, names []string, directory string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	target := filepath.Join(directory, filepath.FromSlash(Dir))
	if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
		if err := ignore(directory); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, ErrUnavailable
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		contents, err := store.Read(ctx, owner, name)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(target, name), contents, 0o644); err != nil {
			return nil, ErrUnavailable
		}
		paths = append(paths, Dir+"/"+name)
	}
	return paths, nil
}

// ignore has git ignore Dir in a checkout, through its own exclude file, so
// the documents never show as changes or get committed.
func ignore(checkout string) error {
	exclude := filepath.Join(checkout, ".git", "info", "exclude")
	line := "/" + Dir + "/"
	current, err := os.ReadFile(exclude)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	for _, existing := range strings.Split(string(current), "\n") {
		if strings.TrimSpace(existing) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		return ErrUnavailable
	}
	if len(current) > 0 && !strings.HasSuffix(string(current), "\n") {
		current = append(current, '\n')
	}
	if err := os.WriteFile(exclude, append(current, []byte(line+"\n")...), 0o644); err != nil {
		return ErrUnavailable
	}
	return nil
}

// ProjectLister lists the projects, to find the one a checkout belongs to.
type ProjectLister interface {
	List(context.Context) ([]project.Project, error)
}

// ForCheckouts answers what a managed checkout does once it is ready: copy
// in the documents of the project whose repository it checks out.
func (store *Store) ForCheckouts(projects ProjectLister) func(ctx context.Context, spec workspace.CheckoutSpec, directory string) error {
	return func(ctx context.Context, spec workspace.CheckoutSpec, directory string) error {
		listed, err := projects.List(ctx)
		if err != nil {
			return err
		}
		for _, candidate := range listed {
			repository := candidate.ForgejoRepository
			if repository != nil && repository.Owner == spec.RepositoryOwner && repository.Name == spec.RepositoryName {
				return store.CopyInto(ctx, candidate.ID, directory)
			}
		}
		return nil
	}
}
