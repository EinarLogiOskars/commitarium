package conversations

import (
	"context"
	"strings"
)

// FirstLookNote is the line the user sees as Mayka starts her first look at
// an imported project; the app draws the mode card under her reply to it
// (SERVER_COMPLETE_PLAN.md, S7).
const FirstLookNote = "Mayka is taking a first look at the imported project."

// FirstLook wakes the assistant of a project just imported for one turn:
// it reads the code and tells the user what they have and what to do next.
// The brief rides with that turn alone, so no other turn carries it.
// settings are the project's settings in plain sentences; builds is whether
// the assistant builds everything there.
func (service *Service) FirstLook(ctx context.Context, scope string, settings []string, builds bool) error {
	return service.Wake(ctx, scope, FirstLookNote, firstLookBrief(settings, builds))
}

func firstLookBrief(settings []string, builds bool) string {
	var brief strings.Builder
	brief.WriteString("The user just imported this project from an existing repository and has not seen it in " +
		"Commitarium yet. Take a first look at the code and write them one message that leaves them knowing what " +
		"they have and what to do next:\n" +
		"  1. What the app is, what it is built with, and how the repository is laid out, in a few lines.\n" +
		"  2. Whether the project's stack matches the code. If it misses a part or gets one wrong, propose " +
		"design_stack: it checks the stack against the code.\n" +
		"  3. What likely needs doing, most important first and kept short, such as missing tests, a missing or " +
		"broken build, outdated dependencies, missing docs, or loose ends in the code. Propose what is worth " +
		"doing as work orders.\n" +
		"  4. Your view of the project in a sentence or two.\n" +
		"  5. How the project is set up, in plain words, saying that each can be changed in Project settings:\n")
	for _, setting := range settings {
		brief.WriteString("     - " + setting + "\n")
	}
	if builds {
		brief.WriteString("  6. Close with how you work here: you build everything, so you start on what you " +
			"recommended without asking, and they can switch to Assists me in Project settings to approve each " +
			"step instead.")
	} else {
		brief.WriteString("  6. Close with how you work here: you assist, so you suggest and they approve each " +
			"step as a card. Say that with Build for me you take the project over and they need not think about " +
			"the settings above, and that a card under your message switches to it.")
	}
	return brief.String()
}
