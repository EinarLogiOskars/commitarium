package project

import "errors"

// AssistantMode is how the assistant works in a project (ADR-027): it builds
// everything, applying its own actions without cards (apps' default), or it
// assists, proposing cards the user approves (projects' default). Either way
// it acts on its own only on work it created.
type AssistantMode string

const (
	AssistantModeBuilds  AssistantMode = "builds"
	AssistantModeAssists AssistantMode = "assists"
)

var ErrInvalidAssistantMode = errors.New("assistant mode must be builds or assists")

// DefaultAssistantMode is the mode a project starts with: an app's assistant
// builds everything, a project's assists.
func DefaultAssistantMode(app bool) AssistantMode {
	if app {
		return AssistantModeBuilds
	}
	return AssistantModeAssists
}

func (mode AssistantMode) IsValid() bool {
	return mode == AssistantModeBuilds || mode == AssistantModeAssists
}

// Assistant is how the assistant works in the project: its stored mode, or
// the default for its app flag when none is stored.
func (project Project) Assistant() AssistantMode {
	if project.AssistantMode.IsValid() {
		return project.AssistantMode
	}
	return DefaultAssistantMode(project.App)
}

// AssistantBuilds reports whether the assistant builds everything in the
// project.
func (project Project) AssistantBuilds() bool {
	return project.Assistant() == AssistantModeBuilds
}
