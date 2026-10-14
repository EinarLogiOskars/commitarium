package project

import "errors"

// AssistantMode is how the assistant works in a project (ADR-027). It
// assists by default: it proposes cards the user approves and acts on its own
// only on work it created or was handed. Building everything is opt-in, and on
// for a project or app the user asked the top-level assistant to create: it
// owns the whole project, applies its actions without cards, and takes over
// work orders the user creates too.
type AssistantMode string

const (
	AssistantModeBuilds  AssistantMode = "builds"
	AssistantModeAssists AssistantMode = "assists"
)

var ErrInvalidAssistantMode = errors.New("assistant mode must be builds or assists")

// DefaultAssistantMode is the mode a project starts with: the assistant
// assists.
func DefaultAssistantMode() AssistantMode {
	return AssistantModeAssists
}

func (mode AssistantMode) IsValid() bool {
	return mode == AssistantModeBuilds || mode == AssistantModeAssists
}

// Assistant is how the assistant works in the project: its stored mode, or
// the default when none is stored.
func (project Project) Assistant() AssistantMode {
	if project.AssistantMode.IsValid() {
		return project.AssistantMode
	}
	return DefaultAssistantMode()
}

// AssistantBuilds reports whether the assistant builds everything in the
// project.
func (project Project) AssistantBuilds() bool {
	return project.Assistant() == AssistantModeBuilds
}
