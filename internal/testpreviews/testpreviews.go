// Package testpreviews keeps the throwaway previews that helpers test the
// app in (HELPERS_PLAN.md, H6).
//
// The coordinator cannot run Docker; the host can. So, like a preview check,
// a test preview is a record here that the host acts on: the assistant's
// helper asks for one, the host claims it, starts a preview of the project's
// default branch seeded with its test accounts, lets the helper's worker
// reach it, and reports its address. When the helper is done (or a test
// preview outlives MaxAge) the record ends, the host removes the preview and
// the worker's reach, and the record goes.
//
// The records live in one 0600 file in the coordinator's own data
// directory, which workers do not mount, so they survive a restart and the
// host tidies up after either side restarts.
package testpreviews

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Status is where a test preview is in its life.
type Status string

const (
	// Pending waits for the host to claim it.
	Pending Status = "pending"
	// Starting is claimed: the host builds and starts it.
	Starting Status = "starting"
	// Ready runs, and the helper's worker can reach it at Address.
	Ready Status = "ready"
	// Failed did not start, or stopped; Reason says why.
	Failed Status = "failed"
	// Ending is no longer wanted: the host removes it and says so.
	Ending Status = "ending"
)

const (
	// MaxAge bounds a test preview's life, so one an abandoned helper left
	// behind is removed anyway.
	MaxAge = 3 * time.Hour
	// MaxActive is how many test previews may exist at once: each builds and
	// runs a whole app.
	MaxActive = 2
	maxReason = 600
)

var (
	ErrInvalid     = errors.New("invalid test preview")
	ErrNotFound    = errors.New("no such test preview")
	ErrConflict    = errors.New("the test preview is not in that state")
	ErrTooMany     = errors.New("too many test previews at once")
	ErrUnavailable = errors.New("test previews are unavailable")

	idPattern      = regexp.MustCompile(`^tsk_[0-9a-f]{24}$`)
	projectPattern = regexp.MustCompile(`^prj_[A-Za-z0-9]{1,64}$`)
	agentPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)
)

// Preview is one test preview. ID is the helper's, ProjectID the project
// whose default branch it runs, Agent the agent whose worker reaches it.
type Preview struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Agent     string    `json:"agent"`
	Status    Status    `json:"status"`
	Address   string    `json:"address,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store keeps the test previews in one file.
type Store struct {
	mu   sync.Mutex
	path string
	// now is the store's clock; tests set it.
	now func() time.Time
}

// NewStore keeps the records at path, creating its directory.
func NewStore(path string) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: the test previews file must be an absolute path", ErrInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create the test previews directory: %w", err)
	}
	return &Store{path: path, now: time.Now}, nil
}

// Request asks for a test preview for helper id. Asking again for one that
// exists answers it as it is; one that is ending must go first.
func (store *Store) Request(_ context.Context, id, projectID, agent string) (Preview, error) {
	if !idPattern.MatchString(id) || !projectPattern.MatchString(projectID) || !agentPattern.MatchString(agent) {
		return Preview{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	previews, err := store.read()
	if err != nil {
		return Preview{}, err
	}
	if index := find(previews, id); index >= 0 {
		if previews[index].Status == Ending {
			return Preview{}, ErrConflict
		}
		return previews[index], nil
	}
	active := 0
	for _, preview := range previews {
		if preview.Status != Ending && preview.Status != Failed {
			active++
		}
	}
	if active >= MaxActive {
		return Preview{}, ErrTooMany
	}
	now := store.now().UTC()
	preview := Preview{ID: id, ProjectID: projectID, Agent: agent, Status: Pending, CreatedAt: now, UpdatedAt: now}
	if err := store.write(append(previews, preview)); err != nil {
		return Preview{}, err
	}
	return preview, nil
}

// Get answers one test preview.
func (store *Store) Get(_ context.Context, id string) (Preview, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	previews, err := store.expired()
	if err != nil {
		return Preview{}, err
	}
	if index := find(previews, id); index >= 0 {
		return previews[index], nil
	}
	return Preview{}, ErrNotFound
}

// List answers every test preview, oldest first, for the host. Those older
// than MaxAge end first.
func (store *Store) List(context.Context) ([]Preview, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.expired()
}

// Claim hands a pending test preview to the host, once.
func (store *Store) Claim(_ context.Context, id string) (Preview, error) {
	return store.move(id, func(preview *Preview) error {
		if preview.Status != Pending {
			return ErrConflict
		}
		preview.Status = Starting
		return nil
	})
}

// Ready records that a test preview runs and the helper's worker reaches
// it at address: http:// with a private IP address and a port, nothing
// else.
func (store *Store) Ready(_ context.Context, id, address string) (Preview, error) {
	if !ValidAddress(address) {
		return Preview{}, ErrInvalid
	}
	return store.move(id, func(preview *Preview) error {
		if preview.Status != Starting {
			return ErrConflict
		}
		preview.Status, preview.Address = Ready, address
		return nil
	})
}

// Fail records why a test preview did not start or stopped.
func (store *Store) Fail(_ context.Context, id, reason string) (Preview, error) {
	reason = bounded(strings.TrimSpace(reason))
	if reason == "" {
		reason = "the test copy of the app stopped"
	}
	return store.move(id, func(preview *Preview) error {
		switch preview.Status {
		case Pending, Starting, Ready:
			preview.Status, preview.Reason, preview.Address = Failed, reason, ""
			return nil
		case Failed:
			return nil
		default:
			return ErrConflict
		}
	})
}

// End says a test preview is no longer wanted. Ending one that is gone is
// done already.
func (store *Store) End(_ context.Context, id string) error {
	_, err := store.move(id, func(preview *Preview) error {
		preview.Status = Ending
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Removed forgets a test preview the host removed. Only an ending one goes:
// the host never removes what is still wanted by saying so.
func (store *Store) Removed(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	previews, err := store.read()
	if err != nil {
		return err
	}
	index := find(previews, id)
	if index < 0 {
		return nil
	}
	if previews[index].Status != Ending {
		return ErrConflict
	}
	return store.write(slices.Delete(previews, index, index+1))
}

// DeleteProject ends a deleted project's test previews.
func (store *Store) DeleteProject(_ context.Context, projectID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	previews, err := store.read()
	if err != nil {
		return err
	}
	changed := false
	now := store.now().UTC()
	for index := range previews {
		if previews[index].ProjectID == projectID && previews[index].Status != Ending {
			previews[index].Status, previews[index].UpdatedAt, changed = Ending, now, true
		}
	}
	if !changed {
		return nil
	}
	return store.write(previews)
}

// ValidAddress reports whether address is how a worker reaches a test
// preview: http://, a private IPv4 address (Docker's networks), and a port.
func ValidAddress(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return false
	}
	ip, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || !ip.Is4() || !ip.IsPrivate() {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port < 65536
}

func (store *Store) move(id string, change func(*Preview) error) (Preview, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	previews, err := store.read()
	if err != nil {
		return Preview{}, err
	}
	index := find(previews, id)
	if index < 0 {
		return Preview{}, ErrNotFound
	}
	if err := change(&previews[index]); err != nil {
		return Preview{}, err
	}
	previews[index].UpdatedAt = store.now().UTC()
	if err := store.write(previews); err != nil {
		return Preview{}, err
	}
	return previews[index], nil
}

// expired ends the test previews older than MaxAge and answers them all.
// Callers hold mu.
func (store *Store) expired() ([]Preview, error) {
	previews, err := store.read()
	if err != nil {
		return nil, err
	}
	now := store.now().UTC()
	changed := false
	for index := range previews {
		preview := &previews[index]
		if preview.Status != Ending && now.Sub(preview.CreatedAt) > MaxAge {
			preview.Status, preview.UpdatedAt, changed = Ending, now, true
			if preview.Reason == "" {
				preview.Reason = "the test copy of the app ran out of time"
			}
		}
	}
	if changed {
		if err := store.write(previews); err != nil {
			return nil, err
		}
	}
	return previews, nil
}

// read answers the records. Callers hold mu.
func (store *Store) read() ([]Preview, error) {
	contents, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Preview{}, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	var previews []Preview
	if err := json.Unmarshal(contents, &previews); err != nil {
		return nil, ErrUnavailable
	}
	return previews, nil
}

// write replaces the records atomically, 0600. Callers hold mu.
func (store *Store) write(previews []Preview) error {
	contents, err := json.MarshalIndent(previews, "", "  ")
	if err != nil {
		return ErrUnavailable
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".test-previews-*.tmp")
	if err != nil {
		return ErrUnavailable
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return ErrUnavailable
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return ErrUnavailable
	}
	if err := temporary.Close(); err != nil {
		return ErrUnavailable
	}
	if err := os.Rename(temporary.Name(), store.path); err != nil {
		return ErrUnavailable
	}
	return nil
}

func find(previews []Preview, id string) int {
	return slices.IndexFunc(previews, func(preview Preview) bool { return preview.ID == id })
}

func bounded(text string) string {
	if len(text) <= maxReason {
		return text
	}
	cut := maxReason
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
