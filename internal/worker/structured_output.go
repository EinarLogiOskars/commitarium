package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var ErrInvalidStructuredOutput = errors.New("invalid structured worker output")

// StructuredOutput is the provider-neutral meaning of one validated final
// response. Provider adapters supply JSON; this package owns what that JSON
// means to Commitarium's workflow.
type StructuredOutput struct {
	Event       Event
	Disposition Disposition
	Publication *ImplementationPublication
	Review      *ReviewPublication
}

func OutputJSONSchema(contract OutputContract) any {
	switch contract {
	case OutputContractPlanningLead:
		return objectSchema(
			map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"respond", "submit_plan"}},
				"content": map[string]any{"type": "string"},
			},
			[]string{"action", "content"},
		)
	case OutputContractImplementationLead:
		return objectSchema(
			map[string]any{
				"action":              map[string]any{"type": "string", "enum": []string{"published", "blocked"}},
				"summary":             map[string]any{"type": "string"},
				"commit_id":           map[string]any{"type": "string"},
				"pull_request_number": map[string]any{"type": "integer"},
			},
			[]string{"action", "summary", "commit_id", "pull_request_number"},
		)
	case OutputContractImplementationReview:
		return objectSchema(
			map[string]any{
				"action":              map[string]any{"type": "string", "enum": []string{"approved", "changes_requested", "blocked"}},
				"summary":             map[string]any{"type": "string"},
				"commit_id":           map[string]any{"type": "string"},
				"pull_request_number": map[string]any{"type": "integer"},
				"review_id":           map[string]any{"type": "integer"},
			},
			[]string{"action", "summary", "commit_id", "pull_request_number", "review_id"},
		)
	case OutputContractImplementationReadiness:
		return objectSchema(
			map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"ready_to_merge", "concern", "blocked"}},
				"summary": map[string]any{"type": "string"},
			},
			[]string{"action", "summary"},
		)
	default:
		return nil
	}
}

func ResolveStructuredOutput(
	contract OutputContract,
	raw []byte,
) (StructuredOutput, error) {
	switch contract {
	case OutputContractPlanningLead:
		var response struct {
			Action  string `json:"action"`
			Content string `json:"content"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Content = strings.TrimSpace(response.Content)
		if response.Content == "" || (response.Action != "respond" && response.Action != "submit_plan") {
			return StructuredOutput{}, invalidStructuredOutput("planning lead response is incomplete")
		}
		eventType := EventMessage
		if response.Action == "submit_plan" {
			eventType = EventPlanSubmitted
		}
		return StructuredOutput{
			Event:       Event{Type: eventType, Text: response.Content},
			Disposition: DispositionSucceeded,
		}, nil

	case OutputContractImplementationLead:
		var response struct {
			Action            string `json:"action"`
			Summary           string `json:"summary"`
			CommitID          string `json:"commit_id"`
			PullRequestNumber int64  `json:"pull_request_number"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Summary = strings.TrimSpace(response.Summary)
		response.CommitID = strings.TrimSpace(response.CommitID)
		if response.Summary == "" || response.PullRequestNumber < 1 {
			return StructuredOutput{}, invalidStructuredOutput("implementation lead response is incomplete")
		}
		if response.Action == "blocked" {
			if response.CommitID != "" {
				return StructuredOutput{}, invalidStructuredOutput("blocked implementation cannot claim a commit")
			}
			return StructuredOutput{
				Event:       Event{Type: EventInputRequired, Text: response.Summary},
				Disposition: DispositionInputRequired,
			}, nil
		}
		if response.Action != "published" {
			return StructuredOutput{}, invalidStructuredOutput("implementation lead response has an unknown action")
		}
		publication := &ImplementationPublication{
			CommitID: response.CommitID, PullRequestNumber: response.PullRequestNumber,
		}
		if err := publication.Validate(); err != nil {
			return StructuredOutput{}, invalidStructuredOutput("implementation publication: %v", err)
		}
		return StructuredOutput{
			Event:       Event{Type: EventMessage, Text: response.Summary},
			Disposition: DispositionSucceeded,
			Publication: publication,
		}, nil

	case OutputContractImplementationReview:
		var response struct {
			Action            string `json:"action"`
			Summary           string `json:"summary"`
			CommitID          string `json:"commit_id"`
			PullRequestNumber int64  `json:"pull_request_number"`
			ReviewID          int64  `json:"review_id"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Summary = strings.TrimSpace(response.Summary)
		response.CommitID = strings.TrimSpace(response.CommitID)
		if response.Summary == "" || response.PullRequestNumber < 1 {
			return StructuredOutput{}, invalidStructuredOutput("implementation review response is incomplete")
		}
		if response.Action == "blocked" {
			if response.CommitID != "" || response.ReviewID != 0 {
				return StructuredOutput{}, invalidStructuredOutput("blocked review cannot claim a commit or review")
			}
			return StructuredOutput{
				Event:       Event{Type: EventInputRequired, Text: response.Summary},
				Disposition: DispositionInputRequired,
			}, nil
		}
		if response.Action != "approved" && response.Action != "changes_requested" {
			return StructuredOutput{}, invalidStructuredOutput("implementation review response has an unknown action")
		}
		review := &ReviewPublication{
			CommitID: response.CommitID, PullRequestNumber: response.PullRequestNumber,
			ReviewID: response.ReviewID,
		}
		if err := review.Validate(); err != nil {
			return StructuredOutput{}, invalidStructuredOutput("implementation review publication: %v", err)
		}
		disposition := DispositionSucceeded
		if response.Action == "changes_requested" {
			disposition = DispositionChangesRequested
		}
		return StructuredOutput{
			Event:       Event{Type: EventMessage, Text: response.Summary},
			Disposition: disposition,
			Review:      review,
		}, nil

	case OutputContractImplementationReadiness:
		var response struct {
			Action  string `json:"action"`
			Summary string `json:"summary"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Summary = strings.TrimSpace(response.Summary)
		if response.Summary == "" {
			return StructuredOutput{}, invalidStructuredOutput("implementation readiness response is incomplete")
		}
		resolved := StructuredOutput{Event: Event{Text: response.Summary}}
		switch response.Action {
		case "ready_to_merge":
			resolved.Event.Type = EventMessage
			resolved.Disposition = DispositionSucceeded
		case "concern":
			resolved.Event.Type = EventMessage
			resolved.Disposition = DispositionChangesRequested
		case "blocked":
			resolved.Event.Type = EventInputRequired
			resolved.Disposition = DispositionInputRequired
		default:
			return StructuredOutput{}, invalidStructuredOutput("implementation readiness response has an unknown action")
		}
		return resolved, nil
	default:
		return StructuredOutput{}, invalidStructuredOutput("output contract %q is unsupported", contract)
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
		"required":             required,
	}
}

func decodeStructuredOutput(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return invalidStructuredOutput("decode response: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return invalidStructuredOutput("response contains trailing JSON")
	}
	return nil
}

func invalidStructuredOutput(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidStructuredOutput, fmt.Sprintf(format, arguments...))
}
