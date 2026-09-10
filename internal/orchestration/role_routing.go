package orchestration

import (
	"context"
	"errors"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
	"github.com/EinarLogiOskars/commitarium/internal/workeringest"
)

// RoleRoutedWorker keeps the workflow code provider-neutral while ensuring
// lead and reviewer attempts reach containers with different credentials and
// durable provider profiles.
type RoleRoutedWorker struct {
	lead     RemoteLeadWorker
	reviewer RemoteLeadWorker
}

func NewRoleRoutedWorker(lead, reviewer RemoteLeadWorker) (*RoleRoutedWorker, error) {
	if lead == nil || reviewer == nil {
		return nil, errors.New("lead and reviewer worker clients are required")
	}
	return &RoleRoutedWorker{lead: lead, reviewer: reviewer}, nil
}

func (router *RoleRoutedWorker) PutAttempt(
	ctx context.Context,
	identity workerhttp.MutationIdentity,
	request workerhttp.PutAttemptRequest,
) (workerhttp.Attempt, bool, error) {
	switch request.Assignment.Role {
	case workerhttp.RoleLead:
		if !strings.HasSuffix(identity.SessionID, ":lead") {
			return workerhttp.Attempt{}, false, errors.New("lead assignment does not use a lead session identity")
		}
		return router.lead.PutAttempt(ctx, identity, request)
	case workerhttp.RoleReviewer:
		if !strings.HasSuffix(identity.SessionID, ":reviewer") {
			return workerhttp.Attempt{}, false, errors.New("reviewer assignment does not use a reviewer session identity")
		}
		return router.reviewer.PutAttempt(ctx, identity, request)
	default:
		return workerhttp.Attempt{}, false, errors.New("remote workflow attempt has an unsupported role")
	}
}

func (router *RoleRoutedWorker) GetAttempt(
	ctx context.Context,
	reference workerhttp.AttemptReference,
) (workerhttp.Attempt, error) {
	workerClient, err := router.forSession(reference.SessionID)
	if err != nil {
		return workerhttp.Attempt{}, err
	}
	return workerClient.GetAttempt(ctx, reference)
}

func (router *RoleRoutedWorker) forSession(sessionID string) (RemoteLeadWorker, error) {
	switch {
	case strings.HasSuffix(sessionID, ":lead"):
		return router.lead, nil
	case strings.HasSuffix(sessionID, ":reviewer"):
		return router.reviewer, nil
	default:
		return nil, errors.New("remote workflow session has an unsupported identity")
	}
}

// RoleRoutedPump consumes the event stream from the same worker that owns the
// session. Session IDs are durable, so recovery makes the identical choice.
type RoleRoutedPump struct {
	lead     RemoteLeadPump
	reviewer RemoteLeadPump
}

func NewRoleRoutedPump(lead, reviewer RemoteLeadPump) (*RoleRoutedPump, error) {
	if lead == nil || reviewer == nil {
		return nil, errors.New("lead and reviewer event pumps are required")
	}
	return &RoleRoutedPump{lead: lead, reviewer: reviewer}, nil
}

func (router *RoleRoutedPump) Run(
	ctx context.Context,
	sessionID string,
) (workeringest.PumpResult, error) {
	switch {
	case strings.HasSuffix(sessionID, ":lead"):
		return router.lead.Run(ctx, sessionID)
	case strings.HasSuffix(sessionID, ":reviewer"):
		return router.reviewer.Run(ctx, sessionID)
	default:
		return workeringest.PumpResult{}, errors.New("remote workflow session has an unsupported identity")
	}
}
