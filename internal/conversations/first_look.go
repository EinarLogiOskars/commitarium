package conversations

import (
	"context"
	"strings"
)

// FirstLookNote is the line the user sees as Mayka starts her first look at
// an imported project; under Ask first the app offers, under her reply to
// it, to let her build everything (SERVER_COMPLETE_PLAN.md, S7).
const FirstLookNote = "Mayka is taking a first look at the imported project."

// FirstLook wakes the assistant of a project just imported for one turn:
// it reads the code and tells the user what they have and what to do next.
// The brief rides with that turn alone, so no other turn carries it.
// settings are the project's settings in plain sentences.
func (service *Service) FirstLook(ctx context.Context, scope string, settings []string) error {
	return service.Wake(ctx, scope, FirstLookNote, firstLookBrief(settings))
}

func firstLookBrief(settings []string) string {
	var brief strings.Builder
	brief.WriteString("The user just imported this project from an existing repository and has not seen it in " +
		"Commitarium yet. Take a first look at the code and write them one message that orients them without " +
		"burying them: what they have and what could come next.\n" +
		"  1. What the app is, what it is built with, and how the repository is laid out, in a few lines.\n" +
		"  2. Whether the project's stack matches the code. Only if it is missing or wrong, propose design_stack, " +
		"which checks it against the code. Do not propose set_up_preview: Commitarium sets the preview up by " +
		"itself.\n" +
		"  3. What likely needs doing, most important first, as a short numbered list: such as missing tests, a " +
		"missing or broken build, outdated dependencies, missing docs, or loose ends in the code. A real fork " +
		"the user should decide, such as moving to another framework, can be a question.\n" +
		"  4. Your view of the project in a sentence or two.\n" +
		"  5. How the project is set up, in a line or two drawn from these settings, saying that each can be " +
		"changed in Project settings:\n")
	for _, setting := range settings {
		brief.WriteString("     - " + setting + "\n")
	}
	brief.WriteString("  6. Close with how much you do here, from your say in the state. Under Build everything you " +
		"start on the list without asking: take work orders for it, and say that Ask first, in Project settings " +
		"or by telling you, has you ask before each step. Under Ask first they choose: ask which of the list to " +
		"start (\"for example, do 1 and 2\") and draft those work orders once they say, proposing none now; say " +
		"that you can build everything yourself so they need not think about the settings above, and that a " +
		"card under your message, or telling you, lets you.")
	return brief.String()
}
