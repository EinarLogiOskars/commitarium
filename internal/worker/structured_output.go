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
				"finding_answers": map[string]any{"type": "array", "items": objectSchema(
					map[string]any{
						"finding": map[string]any{"type": "string"},
						"answer": map[string]any{"type": "string", "enum": []string{
							FindingFixed, FindingAlreadyFixed, FindingDeclined, FindingOutOfScope,
						}},
					},
					[]string{"finding", "answer"},
				)},
			},
			[]string{"action", "summary", "commit_id", "pull_request_number", "system_packages", "environment_reason", "finding_answers"},
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
		return assistantTurnSchema(false)
	case OutputContractAssistantPlanningTurn:
		return assistantTurnSchema(true)
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
			FindingAnswers    []struct {
				Finding string `json:"finding"`
				Answer  string `json:"answer"`
			} `json:"finding_answers"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Summary = strings.TrimSpace(response.Summary)
		response.CommitID = withoutPlaceholder(response.CommitID)
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
		if response.PullRequestNumber < 1 {
			return StructuredOutput{}, invalidStructuredOutput("implementation lead response is missing its pull_request_number")
		}
		// Only an environment request uses the environment fields. Agents
		// often fill them anyway, with a placeholder or a stray note, which is
		// not worth rejecting finished work over: placeholders count as empty
		// and a real note is kept in the summary.
		for _, systemPackage := range response.SystemPackages {
			if withoutPlaceholder(systemPackage) != "" {
				return StructuredOutput{}, invalidStructuredOutput(
					"implementation lead system_packages must be empty unless action is environment_required",
				)
			}
		}
		if note := withoutPlaceholder(response.EnvironmentReason); note != "" {
			response.Summary += "\n\nNote: " + note
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
		for _, answer := range response.FindingAnswers {
			switch answer.Answer {
			case FindingFixed, FindingAlreadyFixed, FindingDeclined, FindingOutOfScope:
			default:
				return StructuredOutput{}, invalidStructuredOutput(
					"implementation finding_answers answer must be fixed, already_fixed, declined, or out_of_scope")
			}
			publication.FindingAnswers = append(publication.FindingAnswers,
				FindingAnswer{Finding: strings.TrimSpace(answer.Finding), Answer: answer.Answer})
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
		response.TestCommitID = withoutPlaceholder(response.TestCommitID)
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
		response.CommitID = withoutPlaceholder(response.CommitID)
		if response.Summary == "" {
			return StructuredOutput{}, invalidStructuredOutput("implementation review response is incomplete")
		}
		if response.PullRequestNumber < 1 {
			return StructuredOutput{}, invalidStructuredOutput("implementation review response is missing its pull_request_number")
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

	case OutputContractAssistantTurn, OutputContractAssistantPlanningTurn:
		var response struct {
			Message   string                              `json:"message"`
			Actions   []featureartifact.AssistantAction   `json:"actions"`
			Notes     string                              `json:"notes"`
			ChatTitle string                              `json:"chat_title"`
			Questions []featureartifact.AssistantQuestion `json:"questions"`
			Plan      *struct {
				Goal          string                     `json:"goal"`
				Step          string                     `json:"step"`
				Covered       []string                   `json:"covered"`
				Decisions     []string                   `json:"decisions"`
				OpenQuestions []string                   `json:"open_questions"`
				Items         []featureartifact.PlanItem `json:"items"`
				StackPresets  []string                   `json:"stack_presets"`
				Frameworks    string                     `json:"frameworks"`
				Document      string                     `json:"document"`
			} `json:"plan"`
		}
		if err := decodeStructuredOutput(raw, &response); err != nil {
			return StructuredOutput{}, err
		}
		response.Message = strings.TrimSpace(response.Message)
		if response.Message == "" {
			return StructuredOutput{}, invalidStructuredOutput("assistant response has no message")
		}
		turn, err := featureartifact.AssistantTurn{
			Actions: response.Actions, Notes: response.Notes, ChatTitle: response.ChatTitle, Questions: response.Questions,
		}.Normalize()
		if err != nil {
			return StructuredOutput{}, invalidStructuredOutput("assistant turn: %v", err)
		}
		if contract == OutputContractAssistantPlanningTurn {
			// The plan is applied as an update_plan of its own, first, so the
			// turn's other actions see it.
			if response.Plan == nil {
				return StructuredOutput{}, invalidStructuredOutput("planning turn has no plan")
			}
			plan, err := featureartifact.AssistantTurn{Actions: []featureartifact.AssistantAction{{
				Type: featureartifact.AssistantUpdatePlan, Reason: "The plan as it stands", Description: response.Plan.Goal,
				Decisions: response.Plan.Decisions, OpenQuestions: response.Plan.OpenQuestions,
				PlanItems: response.Plan.Items, StackPresets: response.Plan.StackPresets, Frameworks: response.Plan.Frameworks,
				PlanStep: response.Plan.Step, PlanCovered: response.Plan.Covered, PlanDocument: response.Plan.Document,
			}}}.Normalize()
			if err != nil {
				return StructuredOutput{}, invalidStructuredOutput("planning turn: %v", err)
			}
			turn.Actions = append(plan.Actions, turn.Actions...)
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

// assistantTurnSchema is the project assistant's turn. While a project's
// vision is being planned, every turn also carries the plan as it stands,
// which Commitarium applies at once (ONE_CONVERSATION_PLAN.md phase 2).
func assistantTurnSchema(planning bool) map[string]any {
	text := map[string]any{"type": "string"}
	types := []string{"create_work_order", "start_work_order", "change_models", "add_vision_item", "start_autopilot", "continue_work_order", "recover_work_order", "close_work_order", "create_app", "plan_project", "set_up_preview", "write_vision", "set_up_stack", "answer_helper", "write_brief", "design_stack", "research", "build_plan"}
	if !planning {
		// Plan mode is suggested only in a chat that is not planning.
		types = append(types, "start_planning")
	}
	action := objectSchema(
		map[string]any{
			"type":                 map[string]any{"type": "string", "enum": types},
			"reason":               text,
			"title":                text,
			"description":          text,
			"vision_item_id":       text,
			"steps":                map[string]any{"type": "string", "enum": []string{"", "full", "reviewed_plan", "planned", "quick"}},
			"lead_agent":           text,
			"lead_model":           text,
			"reviewer_agent":       text,
			"reviewer_model":       text,
			"lead_plan_model":      map[string]any{"type": "string", "description": "For create_work_order and start_work_order with a documented plan: a different model the lead plans with, offered by the lead agent; empty plans with lead_model."},
			"lead_plan_effort":     map[string]any{"type": "string", "description": "The lead's reasoning effort while planning; empty uses lead_effort."},
			"lead_effort":          map[string]any{"type": "string", "description": "The lead's reasoning effort after the plan, one its model accepts; empty is the model's default."},
			"reviewer_plan_model":  map[string]any{"type": "string", "description": "A different model the reviewer reviews the plan with, offered by the reviewer agent; empty uses reviewer_model."},
			"reviewer_plan_effort": map[string]any{"type": "string", "description": "The reviewer's reasoning effort for plan review; empty uses reviewer_effort."},
			"reviewer_effort":      map[string]any{"type": "string", "description": "The reviewer's reasoning effort for code review; empty is the model's default."},
			"work_order_id":        text,
			"when":                 map[string]any{"type": "string", "enum": []string{"", "now", "next_turn"}},
			"how":                  map[string]any{"type": "string", "enum": []string{"", "recheck", "continue", "message", "fresh_conversation", "back_to_ready"}, "description": "For recover_work_order: how to recover the blocked work order. Otherwise empty."},
			"guidance":             map[string]any{"type": "string", "description": "For continue_work_order: what to tell the work order's agents; empty retries as is. For answer_helper: your answer to the helper's question."},
			"priority":             map[string]any{"type": "integer", "description": "For add_vision_item: the priority level, 0 for P0 through 9 for P9; any level the vision has or a new one. For start_autopilot: the priority to finish. Otherwise 0."},
			"after":                map[string]any{"type": "string", "description": "For create_work_order and start_work_order: the work order ID, or the exact title of another of your actions, that this one must wait for because it needs that one's code or result, or both rewrite the same thing wholesale; it starts once that one merges. Touching the same files is no reason. Otherwise empty."},
			"helper":               map[string]any{"type": "string", "description": "For answer_helper: the helper that asked, as Commitarium named it. Otherwise empty."},
			"brief":                map[string]any{"type": "boolean", "description": "For create_work_order: true has its brief written from description before it starts; it is created as a draft. False for quick changes. Otherwise false."},
			"app_brief":            map[string]any{"type": "string", "description": "For create_app and plan_project: everything you gathered with the user, in full, for whoever plans it next. Otherwise empty."},
			"assumptions":          map[string]any{"type": "array", "items": text, "description": "For create_app and plan_project: each thing you assumed where the user did not say, in a short line. Otherwise empty."},
			"attachments":          map[string]any{"type": "array", "items": text, "description": "For create_work_order: the files the user attached that its agents need, such as screenshots of what to change, by file name (screenshot-2026-10-28-1612.png, as in the user's Attached line); the lead sees them with the work order. Otherwise empty."},
		},
		[]string{
			"type", "reason", "title", "description", "vision_item_id", "steps",
			"lead_agent", "lead_model", "reviewer_agent", "reviewer_model",
			"lead_plan_model", "lead_plan_effort", "lead_effort", "reviewer_plan_model", "reviewer_plan_effort", "reviewer_effort",
			"work_order_id", "when", "how", "priority", "guidance", "after", "helper", "brief", "app_brief", "assumptions",
			"attachments",
		},
	)
	properties := map[string]any{
		"message": text,
		"actions": map[string]any{"type": "array", "items": action},
		"notes":   map[string]any{"type": "string", "description": "Your full notes, replacing the old ones; empty keeps them."},
		"chat_title": map[string]any{"type": "string", "description": "When Commitarium says this chat has no name yet: " +
			"a few words saying what it is about. Otherwise empty."},
	}
	properties["questions"] = map[string]any{
		"type": "array",
		"description": "Up to three questions for the user to answer with a tap, when a choice is theirs; " +
			"do not repeat them in message. Otherwise empty.",
		"items": objectSchema(
			map[string]any{
				"text": text,
				"options": map[string]any{"type": "array", "items": text,
					"description": "Two to four short answers."},
				"suggested": map[string]any{"type": "integer", "description": "The index in options of the answer you would pick."},
			},
			[]string{"text", "options", "suggested"},
		),
	}
	required := []string{"message", "actions", "notes", "chat_title", "questions"}
	if planning {
		// The keys of both planning checklists (vision.PlanningChecklist and
		// RoundChecklist), and review.
		planKeys := []string{"purpose", "round", "features", "data", "look", "details", "priorities", "stack", "review"}
		properties["plan"] = objectSchema(
			map[string]any{
				"goal": map[string]any{"type": "string", "description": "What the project is for, in two or three sentences; empty keeps it."},
				"step": map[string]any{"type": "string", "enum": planKeys,
					"description": "The checklist key the conversation is mostly on now (round only for a next round); review once everything is covered or the user says go."},
				"covered": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": planKeys},
					"description": "Every checklist key the plan now covers well enough to build, in any order; it replaces the plan's list."},
				"decisions":      map[string]any{"type": "array", "items": text, "description": "Every decision made so far, one short line each, starting with its checklist key and a colon (\"look: dark theme\"); it replaces the plan's list."},
				"open_questions": map[string]any{"type": "array", "items": text, "description": "Every question still open, one short line each, starting with its checklist key and a colon (\"data: which repositories\"); it replaces the plan's list."},
				"items": map[string]any{"type": "array", "description": "The whole build plan, in order; empty keeps it.", "items": objectSchema(
					map[string]any{
						"priority":    map[string]any{"type": "integer", "description": "0 for P0 (the first version), 1 for P1 (next), and so on."},
						"title":       text,
						"description": text,
						"items": map[string]any{"type": "array", "description": "The item's slices, when it is one feature built in several commits: each a sub-item that one work order builds. Otherwise empty.",
							"items": objectSchema(map[string]any{"title": text, "description": text}, []string{"title", "description"})},
					},
					[]string{"priority", "title", "description", "items"},
				)},
				"stack_presets": map[string]any{"type": "array", "items": text, "description": "The stack's runtime presets, by ID, once it is decided; empty keeps it."},
				"frameworks":    map[string]any{"type": "string", "description": "With stack_presets: the frameworks to build it with, in a few words. Otherwise empty."},
				"document": map[string]any{"type": "string", "description": "The written plan in markdown, in full: the first draft, or its revision. " +
					"Starts with a # title; then the goal, decisions with their reasons, design notes, and the slices as a numbered checklist " +
					"(\"- [ ] 1. <title> — <what changes>; done when <...>\"), the same slices as items. Empty keeps it; empty until you write the draft."},
			},
			[]string{"goal", "step", "covered", "decisions", "open_questions", "items", "stack_presets", "frameworks", "document"},
		)
		required = append(required, "plan")
	}
	return objectSchema(properties, required)
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

// withoutPlaceholder trims a value and blanks the stand-ins agents write into
// fields that do not apply, such as "none" or "n/a".
func withoutPlaceholder(value string) string {
	value = strings.TrimSpace(value)
	switch strings.ToLower(strings.TrimSuffix(value, ".")) {
	case "none", "n/a", "na", "-", "null", "nil":
		return ""
	}
	return value
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
