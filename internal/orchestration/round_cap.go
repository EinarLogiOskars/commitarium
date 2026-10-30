package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// Round limits are the assistant's to handle first (ADR-032): a planning
// discussion that reached its round limit continues with a few more rounds
// and guidance for its agents, which the next planning turn hears.

// RoundCapExtraRounds is how many planning rounds continuing past a round
// limit grants.
const RoundCapExtraRounds = execution.RoundCapExtraRounds

// ErrRoundCapNotContinuable is a run that does not wait on a planning round
// limit, such as one at its review round limit, which goes back to Ready.
var ErrRoundCapNotContinuable = errors.New("the run does not wait on a planning round limit")

type roundCapNote struct {
	guidance      string
	fromAssistant bool
}

// ContinueRoundCap continues a planning run waiting on its round limit:
// RoundCapExtraRounds more rounds, and guidance (when given) for whichever
// agent speaks next. A retried action grants nothing more.
func (starter *RemoteLeadStarter) ContinueRoundCap(
	ctx context.Context, runID, actionID, guidance string, fromAssistant bool,
) (execution.Run, bool, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(actionID) == "" {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	guidance = strings.TrimSpace(guidance)
	if len(guidance) > 16*1024 {
		return execution.Run{}, false, ErrInvalidRunRequest
	}
	run, err := starter.executions.GetRun(ctx, runID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status == execution.RunStatusRunning {
		return run, false, nil
	}
	storedFeature, err := starter.features.GetByID(ctx, run.FeatureID)
	if err != nil {
		return execution.Run{}, false, err
	}
	if run.Status != execution.RunStatusWaitingForUser || run.Paused ||
		run.WaitKind != execution.RunWaitKindRoundCap || storedFeature.State != feature.StatePlanning {
		return execution.Run{}, false, ErrRoundCapNotContinuable
	}
	for extra := 0; extra < RoundCapExtraRounds; extra++ {
		extended, applied, err := starter.executions.ExtendPlanningRoundLimit(
			ctx, planningRoundExtensionID(run.ID, fmt.Sprintf("%s:round-cap:%d", actionID, extra)),
			run.ID, run.PlanningRoundLimit,
		)
		if err != nil {
			return execution.Run{}, false, err
		}
		if !applied {
			// This action already granted its rounds.
			return extended, false, nil
		}
		run = extended
	}
	if guidance != "" {
		starter.activeMu.Lock()
		if starter.roundCapGuidance == nil {
			starter.roundCapGuidance = map[string]roundCapNote{}
		}
		starter.roundCapGuidance[run.ID] = roundCapNote{guidance: guidance, fromAssistant: fromAssistant}
		starter.activeMu.Unlock()
	}
	continued, started, err := starter.StartPlanningRound(ctx, run.ID, actionID+":round-cap")
	if err != nil {
		starter.activeMu.Lock()
		delete(starter.roundCapGuidance, run.ID)
		starter.activeMu.Unlock()
	}
	return continued, started, err
}

// withRoundCapGuidance hands a planning turn the guidance its run was
// continued past its round limit with, once.
func (starter *RemoteLeadStarter) withRoundCapGuidance(request *remoteLeadRequest) {
	if request.planningStage == planningStageNone {
		return
	}
	starter.activeMu.Lock()
	note, found := starter.roundCapGuidance[request.runID]
	delete(starter.roundCapGuidance, request.runID)
	starter.activeMu.Unlock()
	if !found {
		return
	}
	author := "The user"
	if note.fromAssistant {
		author = "Commitarium's assistant"
	}
	instructions := request.request.Instructions + fmt.Sprintf(
		"\n\nThe discussion reached its round limit and continues with %d more rounds. %s read it and adds:\n%s",
		RoundCapExtraRounds, author, note.guidance)
	if len(instructions) <= workerhttp.MaxInstructionsBytes {
		request.request.Instructions = instructions
	}
}
