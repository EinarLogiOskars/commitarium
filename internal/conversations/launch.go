package conversations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

// Launching a new app (APPS_PLAN.md phase 2): the user describes the app in
// a sentence; the vision assistant writes its vision with a recommended
// stack; the stack is set up; and once the setup merges, autopilot builds
// the whole vision. The watcher moves a launch along, so it needs nothing
// from the desktop once started.

type LaunchStage string

const (
	LaunchVision   LaunchStage = "vision"
	LaunchSetup    LaunchStage = "setup"
	LaunchBuilding LaunchStage = "building"
)

type Launch struct {
	Stage     LaunchStage `json:"stage"`
	StartedAt time.Time   `json:"started_at"`
	// VisionRestartedAt is when the launch last woke the assistant to write
	// the vision again, after the vision writer stopped without one.
	VisionRestartedAt *time.Time `json:"vision_restarted_at,omitempty"`
}

// visionGrace is how long a launch waits for its vision writer to begin, and
// visionRestartEvery how long between wake-ups when it stopped short.
const (
	visionGrace        = 2 * time.Minute
	visionRestartEvery = 10 * time.Minute
)

// projectSetupTitle names the work order that scaffolds a new project on its
// stack (httpapi's project setup).
const projectSetupTitle = "Set the project up"

// StartLaunch marks a new app's launch; the vision assistant writes its
// vision separately.
func (service *Service) StartLaunch(ctx context.Context, scope string) (Conversation, error) {
	if scope == TopLevel {
		return Conversation{}, fmt.Errorf("%w: an app is a project", ErrInvalid)
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	chat, err := service.chatFor(ctx, scope)
	if err != nil {
		return Conversation{}, err
	}
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return Conversation{}, err
	}
	defer done()
	if stored.Launch != nil {
		return stored.Conversation, nil
	}
	now := service.now()
	stored.Launch = &Launch{Stage: LaunchVision, StartedAt: now}
	stored.addEvent("Building the app: writing its vision first.", now)
	stored.UpdatedAt = now
	if err := service.write(stored); err != nil {
		return Conversation{}, err
	}
	service.nudgeWatcher()
	return stored.Conversation, nil
}

// launch moves an app's launch along: from its written vision to setting up
// the stack, and from the merged setup to autopilot on the whole vision.
func (watcher Watcher) launch(ctx context.Context, scope string) {
	service := watcher.Service
	stored, err := service.sharedOf(scope)
	if err != nil || stored.Launch == nil || stored.Launch.Stage == LaunchBuilding {
		return
	}
	switch stored.Launch.Stage {
	case LaunchVision:
		if watcher.Visions == nil {
			return
		}
		current, err := watcher.Visions.Get(ctx, scope)
		if err != nil {
			// The vision is still being written, or its writer stopped short.
			if errors.Is(err, vision.ErrNotFound) {
				watcher.restartVision(ctx, scope, *stored.Launch)
			}
			return
		}
		stack := current.Plan.Stack
		if stack == nil || len(stack.Presets) == 0 || watcher.SetUp == nil {
			// Code already there, or nothing to set up: build at once.
			watcher.startBuilding(ctx, scope)
			return
		}
		if err := watcher.SetUp(ctx, scope, stack.Presets, stack.Frameworks); err != nil {
			_ = service.note(scope, "The app's stack could not be set up: "+err.Error())
			return
		}
		service.setLaunchStage(scope, LaunchSetup, fmt.Sprintf(
			"Vision written. Setting the app up on %s.", strings.Join(stack.Presets, " and ")))
	case LaunchSetup:
		features, err := watcher.Features.List(ctx, scope)
		if err != nil {
			return
		}
		for _, item := range features {
			if item.Title == projectSetupTitle && item.State == feature.StateCompleted {
				watcher.startBuilding(ctx, scope)
				return
			}
		}
	}
}

// restartVision wakes the assistant to write the vision again when the
// launch's vision writer stopped without one: its turn failed (an agent's
// login, a provider error) and no session is writing or waiting for the
// user. An assistant whose own agent is signed out waits for the sign-in
// like any turn (sign_in.go).
func (watcher Watcher) restartVision(ctx context.Context, scope string, launch Launch) {
	now := watcher.Service.now()
	if watcher.VisionSessions == nil || now.Sub(launch.StartedAt) < visionGrace ||
		(launch.VisionRestartedAt != nil && now.Sub(*launch.VisionRestartedAt) < visionRestartEvery) {
		return
	}
	if _, err := watcher.VisionSessions.OpenSession(ctx, scope); !errors.Is(err, vision.ErrNotFound) {
		return // Being written, or waiting for the user.
	}
	// Reading the session stores a vision it just finished: then none is
	// missing.
	if _, err := watcher.Visions.Get(ctx, scope); !errors.Is(err, vision.ErrNotFound) {
		return
	}
	service := watcher.Service
	stored, done, err := service.open(scope, "")
	if err != nil {
		return
	}
	defer done()
	if stored.Launch == nil || stored.Launch.Stage != LaunchVision {
		return
	}
	stored.Launch.VisionRestartedAt = &now
	stored.addEvent("Writing the app's plan stopped before it was done; Mayka starts it again.", now)
	stored.PendingEvents = append(stored.PendingEvents,
		"Writing this new app's vision stopped before it was done. Write it again with write_vision.")
	stored.UpdatedAt = now
	if service.write(stored) == nil {
		service.nudgeWatcher()
	}
}

func (watcher Watcher) startBuilding(ctx context.Context, scope string) {
	if _, err := watcher.Service.StartGoal(ctx, scope, AllPriorities); err != nil && !errors.Is(err, ErrGoalActive) {
		_ = watcher.Service.note(scope, "Autopilot could not start building the app: "+err.Error())
		return
	}
	watcher.Service.setLaunchStage(scope, LaunchBuilding, "")
}

func (service *Service) setLaunchStage(scope string, stage LaunchStage, text string) {
	stored, done, err := service.open(scope, "")
	if err != nil {
		return
	}
	defer done()
	if stored.Launch == nil {
		return
	}
	now := service.now()
	stored.Launch.Stage = stage
	if text != "" {
		stored.addEvent(text, now)
	}
	stored.UpdatedAt = now
	_ = service.write(stored)
}

// ActiveLaunches lists the apps still being launched, by project, with their
// stage.
func (service *Service) ActiveLaunches(_ context.Context) (map[string]LaunchStage, error) {
	launches := map[string]LaunchStage{}
	for _, scope := range service.projectScopes() {
		stored, err := service.sharedOf(scope)
		if err != nil {
			return nil, err
		}
		if stored.Launch != nil && stored.Launch.Stage != LaunchBuilding {
			launches[scope] = stored.Launch.Stage
		}
	}
	return launches, nil
}
