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
	AcceptanceTests    *featureartifact.AcceptanceTests
	EnvironmentRequest *EnvironmentRequest
	HandoffBrief       *featureartifact.HandoffBrief
	SessionTitle       string
	VisionPlan         *featureartifact.VisionPlan
	AssistantTurn      *featureartifact.AssistantTurn
}

// MaxSessionTitleRunes bounds a session title; longer titles are shortened.
const MaxSessionTitleRunes = 80

// idSchema describes checklist IDs. Invalid IDs are normalized rather than
// rejected, but stating the format keeps the agent's IDs and the stored ones
// identical.
var idSchema = map[string]any{
	"type":        "string",
	"description": "Stable ID: lowercase letters, digits, '.', '_' or '-', starting with a letter or digit, at most 64 characters.",
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
				"id":               idSchema,
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
				"action":              map[string]any{"type": "string", "enum": []string{"published", "blocked", "environment_required"}},
				"summary":             map[string]any{"type": "string"},
				"commit_id":           map[string]any{"type": "string"},
				"pull_request_number": map[string]any{"type": "integer"},
				"system_packages":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"environment_reason":  map[string]any{"type": "string"},
			},
			[]string{"action", "summary", "commit_id", "pull_request_number", "system_packages", "environment_reason"},
		)
	case OutputContractAcceptanceTests:
		testSchema := objectSchema(
			map[string]any{
				"id":    idSchema,
				"title": map[string]any{"type": "string"},
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command, run from the repository root, that runs only this test and exits non-zero when it fails.",
				},
			},
			[]string{"id", "title", "command"},
		)
		return objectSchema(
			map[string]any{
				"action":         map[string]any{"type": "string", "enum": []string{"authored", "blocked"}},
				"summary":        map[string]any{"type": "string"},
				"test_commit_id": map[string]any{"type": "string"},
				"tests":          map[string]any{"type": "array", "items": testSchema},
			},
			[]string{"action", "summary", "test_commit_id", "tests"},
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
	case OutputContractWorkOrderBrief:
		list := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		return objectSchema(
			map[string]any{
				"action":         map[string]any{"type": "string", "enum": []string{"ask", "propose"}},
				"message":        map[string]any{"type": "string"},
				"goal":           map[string]any{"type": "string"},
				"areas":          list,
				"considerations": list,
				"open_questions": list,
			},
			[]string{"action", "message", "goal", "areas", "considerations", "open_questions"},
		)
	case OutputContractAssistantTurn:
		text := map[string]any{"type": "string"}
		action := objectSchema(
			map[string]any{
				"type": map[string]any{
					"type": "string", "enum": []string{"create_work_order", "start_work_order", "change_models", "add_vision_item", "start_autopilot", "continue_work_order"},
				},
				"reason":            text,
				"title":             text,
				"description":       text,
				"vision_item_id":    text,
				"steps":             map[string]any{"type": "string", "enum": []string{"", "full", "planned", "quick"}},
				"independent_tests": map[string]any{"type": "boolean"},
				"lead_agent":        text,
				"lead_model":        text,
				"reviewer_agent":    text,
				"reviewer_model":    text,
				"work_order_id":     text,
				"when":              map[string]any{"type": "string", "enum": []string{"", "now", "next_turn"}},
				"guidance":          map[string]any{"type": "string", "description": "For continue_work_order: what to tell the work order's agents; empty retries as is."},
				"priority":          map[string]any{"type": "integer", "description": "For add_vision_item: the priority level, 0 for P0 through 9 for P9; any level the vision has or a new one. For start_autopilot: the priority to finish. Otherwise 0."},
			},
			[]string{
				"type", "reason", "title", "description", "vision_item_id", "steps", "independent_tests",
				"lead_agent", "lead_model", "reviewer_agent", "reviewer_model", "work_order_id", "when", "priority", "guidance",
			},
		)
		return objectSchema(
			map[string]any{
				"message": text,
				"actions": map[string]any{"type": "array", "items": action},
				"notes":   map[string]any{"type": "string", "description": "Your full notes, replacing the old ones; empty keeps them."},
			},
			[]string{"message", "actions", "notes"},
		)
	case OutputContractVisionPlan:
		text := map[string]any{"type": "string"}
		done := map[string]any{
			"type":        "boolean",
			"description": "True when the project already delivers this item; false for an item with sub-items.",
		}
		leaf := objectSchema(
			map[string]any{"title": text, "description": text, "done": done},
			[]string{"title", "description", "done"},
		)
		middle := objectSchema(
			map[string]any{"title": text, "description": text, "done": done, "items": map[string]any{"type": "array", "items": leaf}},
			[]string{"title", "description", "done", "items"},
		)
		top := objectSchema(
			map[string]any{"title": text, "description": text, "done": done, "items": map[string]any{"type": "array", "items": middle}},
			[]string{"title", "description", "done", "items"},
		)
		priority := objectSchema(
			map[string]any{
				"level": map[string]any{"type": "integer", "description": "0 for P0 (needed first), 1 for P1, and so on."},
				"items": map[string]any{"type": "array", "items": top},
			},
			[]string{"level", "items"},
		)
		return objectSchema(
			map[string]any{
				"action":     map[string]any{"type": "string", "enum": []string{"ask", "propose"}},
				"message":    text,
				"statement":  text,
				"confidence": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
				"guesses":    map[string]any{"type": "array", "items": text},
				"priorities": map[string]any{"type": "array", "items": priority},
				"stack": objectSchema(
					map[string]any{
						"presets": map[string]any{
							"type": "array", "items": text,
							"description": "Runtime preset IDs for a project without code yet; empty otherwise.",
						},
						"frameworks": text,
						"reason":     text,
					},
					[]string{"presets", "frameworks", "reason"},
				),
			},
			[]string{"action", "message", "statement", "confidence", "guesses", "priorities", "stack"},
		)
	case OutputContractSessionOpening:
		return objectSchema(
			map[string]any{
				"title":   map[string]any{"type": "string", "description": "A short title, at most 60 characters, for what the user asked for in this session."},
				"message": map[string]any{"type": "string"},
			},
			[]string{"title", "message"},
		)
	case OutputContractPlanApproval:
		return objectSchema(
			map[string]any{
				"action":  map[string]any{"type": "string", "enum": []string{"approve", "request_changes"}},
				"message": map[string]any{"type": "string"},
			},
			[]string{"action", "message"},
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
		runOpenSchema := objectSchema(
			map[string]any{
				"service": map[string]any{"type": "string"},
				"port":    map[string]any{"type": "integer"},
			},
			[]string{"service", "port"},
		)
		runSchema := objectSchema(
			map[string]any{"open": runOpenSchema},
			[]string{"open"},
		)
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
				"run":      map[string]any{"anyOf": []any{runSchema, map[string]any{"type": "null"}}},
			},
			[]string{"action", "message", "tools", "services", "run"},
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
			Action            string   `json:"action"`
			Summary           string   `json:"summary"`
			CommitID          string   `json:"commit_id"`
			PullRequestNumber int64    `json:"pull_request_number"`
			SystemPackages    []string `json:"system_packages"`
			EnvironmentReason string   `json:"environment_reason"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Summary = strings.TrimSpace(response.Summary)
		response.CommitID = strings.TrimSpace(response.CommitID)
		if response.Summary == "" {
			return StructuredOutput{}, invalidStructuredOutput("implementation lead response is incomplete")
		}
		if response.Action == "environment_required" {
			request := &EnvironmentRequest{SystemPackages: response.SystemPackages, Reason: strings.TrimSpace(response.EnvironmentReason)}
			if response.CommitID != "" {
				return StructuredOutput{}, invalidStructuredOutput("implementation environment request cannot claim a commit")
			}
			if response.PullRequestNumber != 0 {
				return StructuredOutput{}, invalidStructuredOutput("implementation environment request cannot claim a pull request")
			}
			if err := request.Validate(); err != nil {
				return StructuredOutput{}, invalidStructuredOutput("implementation environment request: %v", err)
			}
			return StructuredOutput{Event: Event{Type: EventInputRequired, Text: response.Summary}, Disposition: DispositionInputRequired, EnvironmentRequest: request}, nil
		}
		if response.PullRequestNumber < 1 || len(response.SystemPackages) != 0 || strings.TrimSpace(response.EnvironmentReason) != "" {
			return StructuredOutput{}, invalidStructuredOutput("implementation lead response has contradictory environment fields")
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

	case OutputContractAcceptanceTests:
		var response struct {
			Action       string                           `json:"action"`
			Summary      string                           `json:"summary"`
			TestCommitID string                           `json:"test_commit_id"`
			Tests        []featureartifact.AcceptanceTest `json:"tests"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Summary = strings.TrimSpace(response.Summary)
		response.TestCommitID = strings.TrimSpace(response.TestCommitID)
		if response.Summary == "" {
			return StructuredOutput{}, invalidStructuredOutput("acceptance test response is incomplete")
		}
		if response.Action == "blocked" {
			if response.TestCommitID != "" || len(response.Tests) != 0 {
				return StructuredOutput{}, invalidStructuredOutput("blocked acceptance test response cannot claim tests")
			}
			return StructuredOutput{
				Event: Event{Type: EventInputRequired, Text: response.Summary}, Disposition: DispositionInputRequired,
			}, nil
		}
		if response.Action != "authored" {
			return StructuredOutput{}, invalidStructuredOutput("acceptance test response has an unknown action")
		}
		tests := featureartifact.AcceptanceTests{TestCommitID: response.TestCommitID, Tests: response.Tests}
		normalized, err := tests.NormalizeInitial(1)
		if err != nil {
			return StructuredOutput{}, invalidStructuredOutput("acceptance tests: %v", err)
		}
		normalized.PlanVersion = 0
		return StructuredOutput{
			Event:       Event{Type: EventMessage, Text: response.Summary},
			Disposition: DispositionSucceeded, AcceptanceTests: &normalized,
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
		if response.Summary == "" {
			return StructuredOutput{}, invalidStructuredOutput("implementation review response is incomplete")
		}
		if response.PullRequestNumber < 1 {
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

	case OutputContractWorkOrderBrief:
		var response struct {
			Action         string   `json:"action"`
			Message        string   `json:"message"`
			Goal           string   `json:"goal"`
			Areas          []string `json:"areas"`
			Considerations []string `json:"considerations"`
			OpenQuestions  []string `json:"open_questions"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		if response.Message == "" {
			return StructuredOutput{}, invalidStructuredOutput("work order brief response is incomplete")
		}
		switch response.Action {
		case "ask":
			return StructuredOutput{
				Event: Event{Type: EventInputRequired, Text: response.Message}, Disposition: DispositionInputRequired,
			}, nil
		case "propose":
			brief := featureartifact.HandoffBrief{
				Goal: strings.TrimSpace(response.Goal), Areas: trimmedItems(response.Areas),
				Considerations: trimmedItems(response.Considerations), OpenQuestions: trimmedItems(response.OpenQuestions),
			}
			if err := brief.Validate(); err != nil {
				return StructuredOutput{}, invalidStructuredOutput("handoff brief: %v", err)
			}
			return StructuredOutput{
				Event: Event{Type: EventMessage, Text: response.Message}, Disposition: DispositionSucceeded,
				HandoffBrief: &brief,
			}, nil
		default:
			return StructuredOutput{}, invalidStructuredOutput("work order brief response has an unknown action")
		}

	case OutputContractAssistantTurn:
		var response struct {
			Message string                            `json:"message"`
			Actions []featureartifact.AssistantAction `json:"actions"`
			Notes   string                            `json:"notes"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		if response.Message == "" {
			return StructuredOutput{}, invalidStructuredOutput("assistant response has no message")
		}
		turn, err := featureartifact.AssistantTurn{Actions: response.Actions, Notes: response.Notes}.Normalize()
		if err != nil {
			return StructuredOutput{}, invalidStructuredOutput("assistant turn: %v", err)
		}
		return StructuredOutput{
			Event: Event{Type: EventMessage, Text: response.Message}, Disposition: DispositionSucceeded,
			AssistantTurn: &turn,
		}, nil

	case OutputContractVisionPlan:
		var response struct {
			Action     string                           `json:"action"`
			Message    string                           `json:"message"`
			Statement  string                           `json:"statement"`
			Confidence string                           `json:"confidence"`
			Guesses    []string                         `json:"guesses"`
			Priorities []featureartifact.VisionPriority `json:"priorities"`
			Stack      *featureartifact.VisionStack     `json:"stack"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		if response.Message == "" {
			return StructuredOutput{}, invalidStructuredOutput("vision response has no message")
		}
		switch response.Action {
		case "ask":
			return StructuredOutput{
				Event: Event{Type: EventInputRequired, Text: response.Message}, Disposition: DispositionInputRequired,
			}, nil
		case "propose":
			plan, err := featureartifact.VisionPlan{
				Statement: response.Statement, Priorities: response.Priorities,
				Confidence: response.Confidence, Guesses: response.Guesses, Stack: response.Stack,
			}.NormalizeProposed()
			if err != nil {
				return StructuredOutput{}, invalidStructuredOutput("vision plan: %v", err)
			}
			return StructuredOutput{
				Event: Event{Type: EventMessage, Text: response.Message}, Disposition: DispositionSucceeded,
				VisionPlan: &plan,
			}, nil
		default:
			return StructuredOutput{}, invalidStructuredOutput("vision response has an unknown action")
		}

	case OutputContractSessionOpening:
		var response struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		title := strings.Join(strings.Fields(response.Title), " ")
		if runes := []rune(title); len(runes) > MaxSessionTitleRunes {
			title = strings.TrimSpace(string(runes[:MaxSessionTitleRunes-1])) + "…"
		}
		response.Message = strings.TrimSpace(response.Message)
		// A missing title is not worth failing the turn over: the session
		// keeps its untitled name.
		if response.Message == "" {
			return StructuredOutput{}, invalidStructuredOutput("session opening response has no message")
		}
		return StructuredOutput{
			Event: Event{Type: EventMessage, Text: response.Message}, Disposition: DispositionSucceeded,
			SessionTitle: title,
		}, nil

	case OutputContractPlanApproval:
		var response struct {
			Action  string `json:"action"`
			Message string `json:"message"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		if response.Message == "" {
			return StructuredOutput{}, invalidStructuredOutput("plan approval response is incomplete")
		}
		resolved := StructuredOutput{Event: Event{Type: EventMessage, Text: response.Message}}
		switch response.Action {
		case "approve":
			resolved.Disposition = DispositionSucceeded
		case "request_changes":
			resolved.Disposition = DispositionChangesRequested
		default:
			return StructuredOutput{}, invalidStructuredOutput("plan approval response has an unknown action")
		}
		return resolved, nil

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
			Run      *struct {
				Open struct {
					Service string `json:"service"`
					Port    int    `json:"port"`
				} `json:"open"`
			} `json:"run"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		if response.Message == "" || response.Tools == nil || response.Services == nil {
			return StructuredOutput{}, invalidStructuredOutput("toolchain setup response is incomplete")
		}
		if response.Action == "ask" {
			if len(response.Tools) != 0 || len(response.Services) != 0 || response.Run != nil {
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
		if response.Run != nil {
			proposal.Run = &ToolchainRunConfig{Open: ToolchainRunOpen{
				Service: response.Run.Open.Service, Port: response.Run.Open.Port,
			}}
		}
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

// trimmedItems drops blank list entries an agent may leave behind.
func trimmedItems(items []string) []string {
	trimmed := make([]string, 0, len(items))
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			trimmed = append(trimmed, item)
		}
	}
	return trimmed
}
