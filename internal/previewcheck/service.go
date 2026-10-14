package previewcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

// The lifecycle follows isolated validation's jobs (ADR-010): the
// coordinator records a check, the desktop lists the pending ones, claims
// one, runs it on the host, and completes it with what it saw. Claiming and
// completing again answer the stored check, so a desktop that retries after
// an uncertain response does not run the app twice.

type Store interface {
	Create(context.Context, Check) (Check, bool, error)
	Get(context.Context, string) (Check, error)
	ListByRun(context.Context, string) ([]Check, error)
	// ListUnfinished lists the pending and running checks, oldest first.
	ListUnfinished(context.Context) ([]Check, error)
	Transition(ctx context.Context, id string, from, to Status, evidence *Evidence, at time.Time) (Check, bool, error)
}

const (
	// ClaimTimeout is how long a check waits for a desktop to pick it up.
	ClaimTimeout = 15 * time.Minute
	// RunTimeout is how long a claimed check may run. The desktop bounds its
	// own build and start well within it.
	RunTimeout = 45 * time.Minute
)

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the service's clock, for tests.
func (service *Service) WithClock(now func() time.Time) *Service {
	service.now = now
	return service
}

// Request asks for a check of a run's commit. It answers the commit's latest
// check unless that one is overdue: then it is expired and a new attempt
// takes its place.
func (service *Service) Request(ctx context.Context, projectID, featureID, runID, commitID string) (Check, bool, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(featureID) == "" ||
		strings.TrimSpace(runID) == "" || !workspace.ValidCommitID(commitID) {
		return Check{}, false, ErrInvalid
	}
	latest, found, err := service.Latest(ctx, runID, commitID)
	if err != nil {
		return Check{}, false, err
	}
	attempt := 1
	if found {
		if !service.Overdue(latest) {
			return latest, false, nil
		}
		if _, _, err := service.store.Transition(ctx, latest.ID, latest.Status, StatusExpired, nil, service.now()); err != nil {
			return Check{}, false, err
		}
		attempt = latest.Attempt + 1
	}
	now := service.now()
	check := Check{
		ID: checkID(runID, commitID, attempt), ProjectID: projectID, FeatureID: featureID, RunID: runID,
		CommitID: commitID, Attempt: attempt, Status: StatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if err := check.Validate(); err != nil {
		return Check{}, false, err
	}
	return service.store.Create(ctx, check)
}

func checkID(runID, commitID string, attempt int) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + commitID + "\x00" + strconv.Itoa(attempt)))
	return "pvc_" + hex.EncodeToString(digest[:16])
}

// Overdue reports whether an unfinished check waited too long for a desktop
// to pick it up or to finish it.
func (service *Service) Overdue(check Check) bool {
	now := service.now()
	switch check.Status {
	case StatusPending:
		return now.Sub(check.CreatedAt) >= ClaimTimeout
	case StatusRunning:
		return check.ClaimedAt == nil || now.Sub(*check.ClaimedAt) >= RunTimeout
	}
	return false
}

func (service *Service) Get(ctx context.Context, id string) (Check, error) {
	return service.store.Get(ctx, id)
}

// ForRun lists a run's checks, newest first.
func (service *Service) ForRun(ctx context.Context, runID string) ([]Check, error) {
	checks, err := service.store.ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(checks, func(i, j int) bool {
		if !checks[i].CreatedAt.Equal(checks[j].CreatedAt) {
			return checks[i].CreatedAt.After(checks[j].CreatedAt)
		}
		return checks[i].Attempt > checks[j].Attempt
	})
	return checks, nil
}

// Latest is the newest check of a run's commit.
func (service *Service) Latest(ctx context.Context, runID, commitID string) (Check, bool, error) {
	checks, err := service.ForRun(ctx, runID)
	if err != nil {
		return Check{}, false, err
	}
	for _, check := range checks {
		if check.CommitID == commitID {
			return check, true, nil
		}
	}
	return Check{}, false, nil
}

// Failures counts a run's checks that failed: each one gave the lead a
// chance to fix the setup, or blocked the work order.
func (service *Service) Failures(ctx context.Context, runID string) (int, error) {
	checks, err := service.store.ListByRun(ctx, runID)
	if err != nil {
		return 0, err
	}
	failed := 0
	for _, check := range checks {
		if check.Status == StatusFailed {
			failed++
		}
	}
	return failed, nil
}

// Pending lists the checks waiting for the desktop, oldest first.
func (service *Service) Pending(ctx context.Context) ([]Check, error) {
	checks, err := service.store.ListUnfinished(ctx)
	if err != nil {
		return nil, err
	}
	pending := make([]Check, 0, len(checks))
	for _, check := range checks {
		if check.Status == StatusPending {
			pending = append(pending, check)
		}
	}
	return pending, nil
}

// Unfinished lists the pending and running checks, oldest first.
func (service *Service) Unfinished(ctx context.Context) ([]Check, error) {
	return service.store.ListUnfinished(ctx)
}

// Claim marks a pending check as running on the desktop. A check already
// claimed or finished is answered as it is.
func (service *Service) Claim(ctx context.Context, id string) (Check, bool, error) {
	check, err := service.store.Get(ctx, id)
	if err != nil {
		return Check{}, false, err
	}
	if check.Status != StatusPending {
		return check, false, nil
	}
	return service.store.Transition(ctx, id, StatusPending, StatusRunning, nil, service.now())
}

// Complete records a claimed check's result. A finished check is answered
// as it is, so a repeated completion changes nothing.
func (service *Service) Complete(ctx context.Context, id string, passed bool, evidence Evidence) (Check, bool, error) {
	check, err := service.store.Get(ctx, id)
	if err != nil {
		return Check{}, false, err
	}
	if check.Status.terminal() {
		return check, false, nil
	}
	if check.Status != StatusRunning {
		return Check{}, false, ErrConflict
	}
	evidence = evidence.Bounded()
	if err := evidence.validate(); err != nil {
		return Check{}, false, err
	}
	status := StatusFailed
	if passed {
		status = StatusPassed
	}
	return service.store.Transition(ctx, id, StatusRunning, status, &evidence, service.now())
}
