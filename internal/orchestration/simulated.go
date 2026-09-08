package orchestration

import (
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// NewSimulatedAssignment creates the deterministic two-agent workflow used to
// validate coordinator behavior before real provider CLIs are connected.
func NewSimulatedAssignment(stepDelay time.Duration) Assignment {
	codex := worker.NewAutomaticScriptedAdapter(
		worker.NewQueuedScriptedAdapter("fake-codex", map[worker.Role][]worker.Script{
			worker.RoleLead: {
				successfulScript("proposed an accepted implementation plan"),
			},
			worker.RoleCoder: {
				successfulScript("implemented the accepted plan"),
				successfulScript("addressed every in-scope review finding"),
			},
		}),
		stepDelay,
	)
	claude := worker.NewAutomaticScriptedAdapter(
		worker.NewQueuedScriptedAdapter("fake-claude", map[worker.Role][]worker.Script{
			worker.RoleConsultant: {
				successfulScript("accepted the refined implementation plan"),
			},
			worker.RoleReviewer: {
				{
					Events: []worker.Event{{
						Type: worker.EventMessage,
						Text: "review found an in-scope issue",
					}},
					Disposition: worker.DispositionChangesRequested,
					Summary:     "changes_requested: address the review finding",
				},
				successfulScript("approved after the review finding was addressed"),
			},
		}),
		stepDelay,
	)

	return Assignment{
		Lead:       Agent{ID: "agt_fake_codex", Adapter: codex},
		Consultant: Agent{ID: "agt_fake_claude", Adapter: claude},
		Coder:      Agent{ID: "agt_fake_codex", Adapter: codex},
		Reviewer:   Agent{ID: "agt_fake_claude", Adapter: claude},
	}
}

func successfulScript(summary string) worker.Script {
	return worker.Script{
		Events:      []worker.Event{{Type: worker.EventMessage, Text: summary}},
		Disposition: worker.DispositionSucceeded,
		Summary:     summary,
	}
}
