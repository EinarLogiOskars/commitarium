package worker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
)

var ErrInvalidStructuredOutput = errors.New("invalid structured worker output")

// StructuredOutput is the provider-neutral meaning of one validated final
// response. Provider adapters supply JSON; this package owns what that JSON
// means to Commitarium's workflow.
type StructuredOutput struct {
	Event              Event
	Disposition        Disposition
	Publication        *ImplementationPublication
	Review             *ReviewPublication
	InterventionEffect InterventionEffect
	ToolchainProposal  *ToolchainProposal
	GoalDraft          *featureartifact.GoalDraft
	ImplementationPlan *featureartifact.ImplementationPlan
}

func OutputJSONSchema(contract OutputContract) any {
	switch contract {
	case OutputContractGoalClarification:
		return objectSchema(
			map[string]any{
				"action":         map[string]any{"type": "string", "enum": []string{"ask", "propose"}},
				"message":        map[string]any{"type": "string"},
				"goal":           map[string]any{"type": "string"},
				"open_questions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			[]string{"action", "message", "goal", "open_questions"},
		)
	case OutputContractPlanningLead:
		stepSchema := objectSchema(
			map[string]any{
				"id":               map[string]any{"type": "string"},
				"title":            map[string]any{"type": "string"},
				"subtitle":         map[string]any{"type": "string"},
				"details_markdown": map[string]any{"type": "string"},
				"verification":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"commit_subject":   map[string]any{"type": "string"},
			},
			[]string{"id", "title", "subtitle", "details_markdown", "verification", "commit_subject"},
		)
		return objectSchema(
			map[string]any{
				"action":        map[string]any{"type": "string", "enum": []string{"respond", "submit_plan"}},
				"content":       map[string]any{"type": "string"},
				"plan_title":    map[string]any{"type": "string"},
				"plan_subtitle": map[string]any{"type": "string"},
				"steps":         map[string]any{"type": "array", "items": stepSchema},
			},
			[]string{"action", "content", "plan_title", "plan_subtitle", "steps"},
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
	case OutputContractIntervention:
		return objectSchema(
			map[string]any{
				"effect": map[string]any{"type": "string", "enum": []string{
					string(InterventionEffectGuidanceApplied),
					string(InterventionEffectClarificationRequired),
					string(InterventionEffectReplanningRequired),
				}},
				"response": map[string]any{"type": "string"},
			},
			[]string{"effect", "response"},
		)
	case OutputContractToolchainSetup:
		return objectSchema(
			map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"ask", "propose"}},
				"message": map[string]any{"type": "string"},
				"tools": map[string]any{
					"type": "array",
					"items": objectSchema(
						map[string]any{
							"name":    map[string]any{"type": "string"},
							"version": map[string]any{"type": "string"},
						},
						[]string{"name", "version"},
					),
				},
				"services": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
			[]string{"action", "message", "tools", "services"},
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
	case OutputContractGoalClarification:
		var response struct {
			Action        string   `json:"action"`
			Message       string   `json:"message"`
			Goal          string   `json:"goal"`
			OpenQuestions []string `json:"open_questions"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		response.Goal = strings.TrimSpace(response.Goal)
		for index := range response.OpenQuestions {
			response.OpenQuestions[index] = strings.TrimSpace(response.OpenQuestions[index])
		}
		if response.Message == "" || (response.Action != "ask" && response.Action != "propose") {
			return StructuredOutput{}, invalidStructuredOutput("goal clarification response is incomplete")
		}
		if response.Action == "ask" && len(response.OpenQuestions) == 0 {
			return StructuredOutput{}, invalidStructuredOutput("goal clarification question is missing")
		}
		if response.Action == "propose" && (response.Goal == "" || len(response.OpenQuestions) != 0) {
			return StructuredOutput{}, invalidStructuredOutput("goal proposal must be complete")
		}
		var draft *featureartifact.GoalDraft
		if response.Goal != "" {
			candidate := featureartifact.GoalDraft{Goal: response.Goal, OpenQuestions: response.OpenQuestions}
			if err := candidate.Validate(); err != nil {
				return StructuredOutput{}, invalidStructuredOutput("goal draft: %v", err)
			}
			draft = &candidate
		}
		return StructuredOutput{
			Event:       Event{Type: EventMessage, Text: response.Message},
			Disposition: DispositionSucceeded, GoalDraft: draft,
		}, nil

	case OutputContractPlanningLead:
		var response struct {
			Action       string                                   `json:"action"`
			Content      string                                   `json:"content"`
			PlanTitle    string                                   `json:"plan_title"`
			PlanSubtitle string                                   `json:"plan_subtitle"`
			Steps        []featureartifact.ImplementationPlanStep `json:"steps"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Content = strings.TrimSpace(response.Content)
		response.PlanTitle = strings.TrimSpace(response.PlanTitle)
		response.PlanSubtitle = strings.TrimSpace(response.PlanSubtitle)
		if response.Content == "" || (response.Action != "respond" && response.Action != "submit_plan") {
			return StructuredOutput{}, invalidStructuredOutput("planning lead response is incomplete")
		}
		eventType := EventMessage
		if response.Action == "submit_plan" {
			eventType = EventPlanSubmitted
			plan := featureartifact.ImplementationPlan{
				Title: response.PlanTitle, Subtitle: response.PlanSubtitle, Steps: response.Steps,
			}
			normalized, err := plan.NormalizeInitial(1)
			if err != nil {
				return StructuredOutput{}, invalidStructuredOutput("implementation plan: %v", err)
			}
			normalized.PlanVersion = 0 // the coordinator assigns the run's durable version
			return StructuredOutput{
				Event:       Event{Type: eventType, Text: response.Content},
				Disposition: DispositionSucceeded, ImplementationPlan: &normalized,
			}, nil
		}
		if response.PlanTitle != "" || response.PlanSubtitle != "" || len(response.Steps) != 0 {
			return StructuredOutput{}, invalidStructuredOutput("planning response cannot publish checklist fields")
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

	case OutputContractIntervention:
		var response struct {
			Effect   InterventionEffect `json:"effect"`
			Response string             `json:"response"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Response = strings.TrimSpace(response.Response)
		if response.Response == "" || !response.Effect.IsValid() {
			return StructuredOutput{}, invalidStructuredOutput("intervention response is incomplete")
		}
		return StructuredOutput{
			Event:              Event{Type: EventMessage, Text: response.Response},
			Disposition:        DispositionSucceeded,
			InterventionEffect: response.Effect,
		}, nil

	case OutputContractToolchainSetup:
		var response struct {
			Action  string `json:"action"`
			Message string `json:"message"`
			Tools   []struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"tools"`
			Services []string `json:"services"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		if response.Message == "" || response.Tools == nil || response.Services == nil {
			return StructuredOutput{}, invalidStructuredOutput("toolchain setup response is incomplete")
		}
		if response.Action == "ask" {
			if len(response.Tools) != 0 || len(response.Services) != 0 {
				return StructuredOutput{}, invalidStructuredOutput("toolchain setup question cannot include a proposal")
			}
			return StructuredOutput{
				Event: Event{Type: EventInputRequired, Text: response.Message}, Disposition: DispositionInputRequired,
			}, nil
		}
		if response.Action != "propose" || len(response.Tools) == 0 {
			return StructuredOutput{}, invalidStructuredOutput("toolchain setup proposal has no tools")
		}
		tools := make(map[string]string, len(response.Tools))
		for _, configured := range response.Tools {
			name := strings.TrimSpace(configured.Name)
			version := strings.TrimSpace(configured.Version)
			if name == "" || version == "" {
				return StructuredOutput{}, invalidStructuredOutput("toolchain setup proposal has an incomplete tool")
			}
			if _, exists := tools[name]; exists {
				return StructuredOutput{}, invalidStructuredOutput("toolchain setup proposal repeats tool %q", name)
			}
			tools[name] = version
		}
		proposal := &ToolchainProposal{Tools: tools, Services: response.Services}
		return StructuredOutput{
			Event: Event{Type: EventMessage, Text: response.Message}, Disposition: DispositionSucceeded,
			ToolchainProposal: proposal,
		}, nil
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
