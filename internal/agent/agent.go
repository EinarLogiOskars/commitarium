// Package agent defines agents (ADR-016): one provider account, a
// subscription or an API key, that can serve as lead or reviewer on any
// number of work orders.
package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

var (
	ErrNotFound = errors.New("agent not found")
	ErrInvalid  = errors.New("invalid agent")
	ErrConflict = errors.New("agent conflicts with an existing agent")
	ErrInUse    = errors.New("agent is still used by a project or an active work order")
)

// MaxIDLength keeps derived names (worker service, volumes, and Forgejo users
// such as "<id>-reviewer") within Docker and Forgejo limits.
const MaxIDLength = 30

var idPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)

type Agent struct {
	ID        string
	Name      string
	Provider  project.AgentProvider
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (a Agent) Validate() error {
	switch {
	case !idPattern.MatchString(a.ID):
		return fmt.Errorf("%w: id %q must be lowercase letters, digits, and dashes (at most %d)", ErrInvalid, a.ID, MaxIDLength)
	case strings.TrimSpace(a.Name) == "" || len(a.Name) > 80 || strings.ContainsAny(a.Name, "\r\n"):
		return fmt.Errorf("%w: name is required, single-line, and at most 80 characters", ErrInvalid)
	case !a.Provider.IsValid():
		return fmt.Errorf("%w: provider %q is not supported", ErrInvalid, a.Provider)
	case a.CreatedAt.IsZero() || a.UpdatedAt.IsZero():
		return fmt.Errorf("%w: timestamps are required", ErrInvalid)
	}
	return nil
}

// IDFromName derives a readable ID: "Claude Max #2" becomes "claude-max-2".
func IDFromName(name string) string {
	var builder strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			dash = false
		case !dash && builder.Len() > 0:
			builder.WriteByte('-')
			dash = true
		}
	}
	id := strings.Trim(builder.String(), "-")
	if len(id) > MaxIDLength {
		id = strings.Trim(id[:MaxIDLength], "-")
	}
	if id == "" {
		id = "agent"
	}
	return id
}

type Store interface {
	List(ctx context.Context) ([]Agent, error)
	Get(ctx context.Context, id string) (Agent, error)
	// Create stores a new agent; ErrConflict when the ID is taken.
	Create(ctx context.Context, a Agent) error
	Rename(ctx context.Context, id, name string, at time.Time) (Agent, error)
	// Delete removes an agent no project default or active run uses.
	Delete(ctx context.Context, id string) error
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) List(ctx context.Context) ([]Agent, error) { return s.store.List(ctx) }

func (s *Service) Get(ctx context.Context, id string) (Agent, error) { return s.store.Get(ctx, id) }

// Create adds an agent with an ID derived from its name, numbering it when
// the name is already taken ("claude-max-2").
func (s *Service) Create(ctx context.Context, name string, provider project.AgentProvider) (Agent, error) {
	name = strings.TrimSpace(name)
	base := IDFromName(name)
	now := s.now()
	for suffix := 1; suffix <= 99; suffix++ {
		id := base
		if suffix > 1 {
			tail := fmt.Sprintf("-%d", suffix)
			if len(id)+len(tail) > MaxIDLength {
				id = strings.Trim(id[:MaxIDLength-len(tail)], "-")
			}
			id += tail
		}
		created := Agent{ID: id, Name: name, Provider: provider, CreatedAt: now, UpdatedAt: now}
		if err := created.Validate(); err != nil {
			return Agent{}, err
		}
		err := s.store.Create(ctx, created)
		if err == nil {
			return created, nil
		}
		if !errors.Is(err, ErrConflict) {
			return Agent{}, err
		}
	}
	return Agent{}, ErrConflict
}

func (s *Service) Rename(ctx context.Context, id, name string) (Agent, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n") {
		return Agent{}, fmt.Errorf("%w: name is required, single-line, and at most 80 characters", ErrInvalid)
	}
	return s.store.Rename(ctx, id, name, s.now())
}

func (s *Service) Delete(ctx context.Context, id string) error { return s.store.Delete(ctx, id) }
