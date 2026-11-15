package project

import "errors"

// AssistantMode is the level of Mayka's say in a project (ADR-041): Ask
// first (assists, the default) or Build everything (builds, on for a project
// or app the user asked the top-level assistant to create). It is stored in
// projects.assistant_mode, a name kept from ADR-027; the rest of her say is
// her own words, kept with her chats. The level is a fact she reads when
// choosing to take or ask; it is not a mode the code branches on.
type AssistantMode string

const (
	AssistantModeBuilds  AssistantMode = "builds"
	AssistantModeAssists AssistantMode = "assists"
)

var ErrInvalidAssistantMode = errors.New("assistant mode must be builds or assists")

// DefaultAssistantMode is the level a project starts with: Ask first.
func DefaultAssistantMode() AssistantMode {
	return AssistantModeAssists
}

func (mode AssistantMode) IsValid() bool {
	return mode == AssistantModeBuilds || mode == AssistantModeAssists
}

// Assistant is the level of Mayka's say in the project: the stored one, or
// the default when none is stored.
func (project Project) Assistant() AssistantMode {
	if project.AssistantMode.IsValid() {
		return project.AssistantMode
	}
	return DefaultAssistantMode()
}

// BuildsEverything reports whether Mayka's say in the project is Build
// everything.
func (project Project) BuildsEverything() bool {
	return project.Assistant() == AssistantModeBuilds
}
