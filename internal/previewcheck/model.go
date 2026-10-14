// Package previewcheck records Commitarium's own checks that an app's preview
// runs (ADR-028): for the exact commit a preview-setup work order published,
// the desktop builds and starts the project's compose file in a temporary,
// isolated compose project and reports what each container did.
package previewcheck

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/workspace"
)

type Status string

const (
	// StatusPending waits for the desktop to pick the check up.
	StatusPending Status = "pending"
	// StatusRunning is a check the desktop claimed and is running.
	StatusRunning Status = "running"
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	// StatusExpired is a check no desktop finished in time, replaced by a
	// newer attempt for the same commit. It says nothing about the app.
	StatusExpired Status = "expired"
)

func (status Status) terminal() bool {
	return status == StatusPassed || status == StatusFailed || status == StatusExpired
}

var (
	ErrInvalid  = errors.New("invalid preview check")
	ErrNotFound = errors.New("preview check not found")
	ErrConflict = errors.New("preview check state conflict")
)

// The stages a check goes through; a failed check names the one it failed
// in.
const (
	// StageSource fetches the commit's files.
	StageSource = "source"
	// StageSetup validates the compose file against the preview rules.
	StageSetup = "setup"
	// StageBuild builds the images and creates the containers.
	StageBuild = "build"
	// StageStart waits for the containers to stay up, their healthchecks to
	// pass, and the open target to answer HTTP.
	StageStart = "start"
	// StageReady is a check that passed.
	StageReady = "ready"
)

// Bounds on what the desktop reports. Longer text is cut, keeping its end:
// the last lines of a log say why a container stopped.
const (
	maxReasonBytes   = 1000
	maxOutputBytes   = 16 * 1024
	maxLogBytes      = 16 * 1024
	maxServices      = 32
	maxServiceFields = 200
)

// ServiceEvidence is what one compose service did during the check.
type ServiceEvidence struct {
	Service string `json:"service"`
	// State is Docker's container state: running, exited, restarting, ...
	State    string `json:"state"`
	ExitCode int    `json:"exit_code"`
	// Health is the healthcheck status when the service defines one:
	// starting, healthy, or unhealthy.
	Health string `json:"health,omitempty"`
	// Logs are the container's last lines.
	Logs string `json:"logs,omitempty"`
}

// Failing reports whether the service is what failed the check: it stopped
// with an error, keeps restarting, or reports itself unhealthy.
func (service ServiceEvidence) Failing() bool {
	return service.ExitCode != 0 || service.State == "restarting" || service.State == "dead" ||
		service.Health == "unhealthy"
}

// Evidence is what a check saw.
type Evidence struct {
	// Stage is how far the check got.
	Stage string `json:"stage"`
	// Reason says, technically, why the check failed; empty when it passed.
	Reason string `json:"reason,omitempty"`
	// Open is the service and container port the check opened, as
	// "service:port".
	Open string `json:"open,omitempty"`
	// Output is the end of the build and start output, for a check that
	// failed before its containers ran.
	Output   string            `json:"output,omitempty"`
	Services []ServiceEvidence `json:"services"`
}

// Bounded cuts the evidence to the stored limits.
func (evidence Evidence) Bounded() Evidence {
	evidence.Stage = strings.TrimSpace(evidence.Stage)
	evidence.Reason = tail(strings.TrimSpace(evidence.Reason), maxReasonBytes)
	evidence.Open = head(strings.TrimSpace(evidence.Open), maxServiceFields)
	evidence.Output = tail(evidence.Output, maxOutputBytes)
	services := evidence.Services
	if len(services) > maxServices {
		services = services[:maxServices]
	}
	bounded := make([]ServiceEvidence, 0, len(services))
	for _, service := range services {
		service.Service = head(strings.TrimSpace(service.Service), maxServiceFields)
		service.State = head(strings.TrimSpace(service.State), maxServiceFields)
		service.Health = head(strings.TrimSpace(service.Health), maxServiceFields)
		service.Logs = tail(service.Logs, maxLogBytes)
		bounded = append(bounded, service)
	}
	evidence.Services = bounded
	return evidence
}

func (evidence Evidence) validate() error {
	switch evidence.Stage {
	case StageSource, StageSetup, StageBuild, StageStart, StageReady:
	default:
		return fmt.Errorf("%w: unknown stage %q", ErrInvalid, evidence.Stage)
	}
	for _, service := range evidence.Services {
		if service.Service == "" {
			return fmt.Errorf("%w: a service has no name", ErrInvalid)
		}
	}
	return nil
}

// failing is the first service that failed the check, if any.
func (evidence Evidence) failing() (ServiceEvidence, bool) {
	for _, service := range evidence.Services {
		if service.Failing() {
			return service, true
		}
	}
	return ServiceEvidence{}, false
}

// Plain says in a few words, for someone who does not write code, what is
// failing.
func (evidence Evidence) Plain() string {
	if service, ok := evidence.failing(); ok {
		switch {
		case service.Health == "unhealthy":
			return fmt.Sprintf("the %q part of the app starts but reports that it is not working", service.Service)
		case service.State == "restarting":
			return fmt.Sprintf("the %q part of the app keeps crashing and restarting", service.Service)
		default:
			return fmt.Sprintf("the %q part of the app stops right after it starts (exit code %d)",
				service.Service, service.ExitCode)
		}
	}
	switch evidence.Stage {
	case StageSource:
		return "Commitarium could not fetch the app's files"
	case StageSetup:
		return "the app's setup file does not follow the rules Commitarium runs apps by"
	case StageBuild:
		return "the app could not be built"
	case StageStart:
		if service, _, found := strings.Cut(evidence.Open, ":"); found {
			return fmt.Sprintf("the %q part of the app never started answering", service)
		}
		return "the app did not start answering"
	}
	return "the app did not start"
}

// Technical describes the evidence for an agent: why the check failed, and
// each service's state, exit code, health, and last log lines, failing
// services first.
func (evidence Evidence) Technical() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Stage reached: %s.", evidence.Stage)
	if evidence.Reason != "" {
		builder.WriteString(" Reason: " + evidence.Reason)
	}
	if evidence.Open != "" {
		builder.WriteString("\nOpen target: " + evidence.Open)
	}
	ordered := make([]ServiceEvidence, 0, len(evidence.Services))
	for _, service := range evidence.Services {
		if service.Failing() {
			ordered = append(ordered, service)
		}
	}
	for _, service := range evidence.Services {
		if !service.Failing() {
			ordered = append(ordered, service)
		}
	}
	for _, service := range ordered {
		fmt.Fprintf(&builder, "\n\nService %q: state %s, exit code %d", service.Service, orNone(service.State), service.ExitCode)
		if service.Health != "" {
			builder.WriteString(", health " + service.Health)
		}
		if logs := strings.TrimRight(service.Logs, "\n"); logs != "" {
			builder.WriteString(". Last log lines:\n```\n" + logs + "\n```")
		} else {
			builder.WriteString(". No log output.")
		}
	}
	if output := strings.TrimRight(evidence.Output, "\n"); output != "" {
		builder.WriteString("\n\nBuild and start output (end):\n```\n" + output + "\n```")
	}
	return builder.String()
}

// Check is one preview check of one commit.
type Check struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	FeatureID string `json:"feature_id"`
	RunID     string `json:"run_id"`
	CommitID  string `json:"commit_id"`
	// Attempt numbers the checks of one commit; a later attempt replaces one
	// that expired.
	Attempt int    `json:"attempt"`
	Status  Status `json:"status"`
	// Evidence is what the check saw, once it finished.
	Evidence    *Evidence  `json:"evidence,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClaimedAt   *time.Time `json:"claimed_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

func (check Check) Validate() error {
	if strings.TrimSpace(check.ID) == "" || strings.TrimSpace(check.ProjectID) == "" ||
		strings.TrimSpace(check.FeatureID) == "" || strings.TrimSpace(check.RunID) == "" ||
		!workspace.ValidCommitID(check.CommitID) || check.Attempt < 1 ||
		check.CreatedAt.IsZero() || check.UpdatedAt.Before(check.CreatedAt) {
		return ErrInvalid
	}
	switch check.Status {
	case StatusPending, StatusRunning, StatusPassed, StatusFailed, StatusExpired:
	default:
		return ErrInvalid
	}
	if check.Status.terminal() != (check.CompletedAt != nil) {
		return ErrInvalid
	}
	if (check.Status == StatusPassed || check.Status == StatusFailed) != (check.Evidence != nil) {
		return ErrInvalid
	}
	if check.Evidence != nil {
		return check.Evidence.validate()
	}
	return nil
}

func orNone(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

// tail keeps the last limit bytes of value, starting on a whole line when it
// cuts.
func tail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := value[len(value)-limit:]
	if index := strings.IndexByte(cut, '\n'); index >= 0 && index < len(cut)-1 {
		cut = cut[index+1:]
	}
	return strings.ToValidUTF8(cut, "")
}

func head(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return strings.ToValidUTF8(value[:limit], "")
}
