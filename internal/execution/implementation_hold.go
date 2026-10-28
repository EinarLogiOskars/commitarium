package execution

import (
	"fmt"
	"strings"
)

// Early planning (ADR-037): a work order that builds on unmerged work plans
// while that work is built, then waits with its plan ready before it
// implements. Its run waits at the phase checkpoint with one of these reasons,
// which the work order's page and the assistant's events show as they are.

// implementationHoldPrefix starts the reason of a run that planned early and
// waits to implement.
const implementationHoldPrefix = "Planned; implements "

// ImplementationHoldReason says, in plain words, that a run's plan is ready
// and it implements once the work it builds on has merged.
func ImplementationHoldReason(follows string) string {
	return implementationHoldPrefix + "once “" + follows + "” has merged."
}

// ImplementationSlotReason says that a run's plan is ready and it implements
// once fewer than the most work orders that run at once are working.
func ImplementationSlotReason(running int) string {
	return fmt.Sprintf("%swhen a slot frees: %d work orders already run here, the most at once (set in Settings).",
		implementationHoldPrefix, running)
}

// HeldBeforeImplementation reports whether a run's reason says it planned
// early and waits to implement.
func HeldBeforeImplementation(reason string) bool {
	return strings.HasPrefix(reason, implementationHoldPrefix)
}

// ImplementationHoldWhat is what a held run waits for, from its reason, to
// follow "implements" in a sentence ("once “X” has merged.").
func ImplementationHoldWhat(reason string) (string, bool) {
	return strings.CutPrefix(reason, implementationHoldPrefix)
}
