package conversations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/agent"
	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

type FeatureLister interface {
	List(ctx context.Context, projectID string) ([]feature.Feature, error)
}

type VisionReader interface {
	Get(ctx context.Context, projectID string) (vision.Vision, error)
}

type ToolchainReader interface {
	Get(ctx context.Context, projectID string) (toolchain.Manifest, error)
}

// ModelChoice is a model an agent's provider offers.
type ModelChoice struct {
	Provider    project.AgentProvider
	ID, Display string
}

// StateDescriber builds the assistant's picture of its scope from the
// coordinator's own services.
type StateDescriber struct {
	Projects   ProjectReader
	Agents     AgentLister
	Features   FeatureLister
	Visions    VisionReader
	Toolchains ToolchainReader
	Models     func(ctx context.Context) []ModelChoice
	// Runs, when set, adds what an unfinished work order's run is doing or
	// waiting for, and why.
	Runs RunReader
}

// maxWorkOrders bounds the work orders listed for a project.
const maxWorkOrders = 40

func (describer StateDescriber) Describe(ctx context.Context, scope string) (string, error) {
	var builder strings.Builder
	agents, err := describer.Agents.List(ctx)
	if err != nil {
		return "", err
	}
	describer.writeAgents(ctx, &builder, agents)
	projects, err := describer.Projects.List(ctx)
	if err != nil {
		return "", err
	}
	if scope != TopLevel {
		current, err := describer.Projects.GetByID(ctx, scope)
		if err != nil {
			return "", err
		}
		describer.writeProject(ctx, &builder, current)
	}
	builder.WriteString("\n## Projects\n")
	if scope != TopLevel {
		builder.WriteString("Other projects, to read from and reuse; you act only in this one.\n")
	}
	for _, other := range projects {
		if other.ID == scope {
			continue
		}
		statement := ""
		if describer.Visions != nil {
			if stored, err := describer.Visions.Get(ctx, other.ID); err == nil {
				statement = " — " + firstLine(stored.Plan.Statement)
			}
		}
		builder.WriteString(fmt.Sprintf("- %s (%s)%s\n", other.Name, other.ID, statement))
	}
	return strings.TrimSpace(builder.String()), nil
}

func (describer StateDescriber) writeAgents(ctx context.Context, builder *strings.Builder, agents []agent.Agent) {
	builder.WriteString("## Agents and models\n")
	var models []ModelChoice
	if describer.Models != nil {
		models = describer.Models(ctx)
	}
	for _, candidate := range agents {
		offered := make([]string, 0)
		for _, model := range models {
			if model.Provider == candidate.Provider {
				offered = append(offered, model.ID+" ("+model.Display+")")
			}
		}
		builder.WriteString(fmt.Sprintf("- agent %s (%s, %s): %s\n",
			candidate.ID, candidate.Name, candidate.Provider, strings.Join(offered, ", ")))
	}
}

func (describer StateDescriber) writeProject(ctx context.Context, builder *strings.Builder, current project.Project) {
	builder.WriteString(fmt.Sprintf("\n## This project: %s (%s)\n", current.Name, current.ID))
	builder.WriteString(fmt.Sprintf("Defaults: lead %s on %s, reviewer %s on %s; merge %s; autonomy %s; independent tests %t.\n",
		current.AgentProviders.LeadAgent, current.AgentModels.Lead,
		current.AgentProviders.ReviewerAgent, current.AgentModels.Reviewer,
		current.MergePolicy, current.AutonomyPolicy, current.IndependentAcceptanceTests))
	if current.ForgejoRepository != nil {
		if overview, err := describer.Projects.GetRepositoryOverview(ctx, current.ID); err == nil {
			builder.WriteString(fmt.Sprintf("Repository default branch %s at %s.\n", overview.DefaultBranch, overview.Head.CommitID))
		}
	}
	if describer.Toolchains != nil {
		if manifest, err := describer.Toolchains.Get(ctx, current.ID); err == nil && manifest.Status == toolchain.StatusConfigured {
			tools := make([]string, 0, len(manifest.Tools))
			for tool, version := range manifest.Tools {
				tools = append(tools, tool+" "+version)
			}
			sort.Strings(tools)
			builder.WriteString("Stack: " + strings.Join(tools, ", ") + ".\n")
		} else if err == nil {
			builder.WriteString("Stack: not set up yet.\n")
		}
	}

	var features []feature.Feature
	if describer.Features != nil {
		features, _ = describer.Features.List(ctx, current.ID)
	}
	byID := map[string]feature.Feature{}
	for _, item := range features {
		byID[item.ID] = item
	}
	linked := map[string]bool{}
	if describer.Visions != nil {
		stored, err := describer.Visions.Get(ctx, current.ID)
		switch {
		case errors.Is(err, vision.ErrNotFound):
			builder.WriteString("\nVision: none written yet.\n")
		case err == nil:
			builder.WriteString("\n### Vision\n" + stored.Plan.Statement + "\n")
			for _, priority := range stored.Plan.Priorities {
				writeVisionItems(builder, priority.Items, fmt.Sprintf("P%d", priority.Level), "", byID)
				collectLinked(priority.Items, linked)
			}
		}
	}

	builder.WriteString("\n### Work orders (newest first)\n")
	sort.Slice(features, func(i, j int) bool { return features[i].UpdatedAt.After(features[j].UpdatedAt) })
	listed := 0
	for _, item := range features {
		if item.Background && !item.PreviewCheck {
			continue
		}
		if listed == maxWorkOrders {
			builder.WriteString("- …and older ones.\n")
			break
		}
		listed++
		kind := "work order"
		if item.Kind == feature.KindSession {
			kind = "session"
		}
		outside := ""
		if item.PreviewCheck {
			outside = " — sets the app's preview up; merges once Commitarium's preview check passes"
		} else if !linked[item.ID] {
			outside = " — outside the vision"
		}
		builder.WriteString(fmt.Sprintf("- %s %q (%s): %s, steps %s, lead %s on %s, reviewer %s on %s%s%s\n",
			kind, item.Title, item.ID, item.State, stepsName(item.Steps),
			item.AgentProviders.LeadAgent, item.AgentModels.Lead,
			item.AgentProviders.ReviewerAgent, item.AgentModels.Reviewer, outside, describer.runNote(ctx, item)))
	}
	if listed == 0 {
		builder.WriteString("- none yet\n")
	}
}

func writeVisionItems(builder *strings.Builder, items []featureartifact.VisionItem, prefix, indent string, byID map[string]feature.Feature) {
	for index, item := range items {
		label := fmt.Sprintf("%s.%d", prefix, index+1)
		status := ""
		switch {
		case item.Done:
			status = " [done]"
		case item.FeatureID != "":
			state := "unknown"
			if linked, ok := byID[item.FeatureID]; ok {
				state = string(linked.State)
			}
			status = fmt.Sprintf(" [work order %s: %s]", item.FeatureID, state)
		}
		builder.WriteString(fmt.Sprintf("%s- %s %s (item %s)%s: %s\n",
			indent, label, item.Title, item.ID, status, firstLine(item.Description)))
		writeVisionItems(builder, item.Items, label, indent+"  ", byID)
	}
}

func stepsName(steps project.WorkSteps) string {
	switch {
	case steps.SkipPlan && steps.SkipReviewer:
		return "quick"
	case steps.SkipReviewer:
		return "planned"
	default:
		return "full"
	}
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if line, _, found := strings.Cut(text, "\n"); found {
		text = line
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

func collectLinked(items []featureartifact.VisionItem, linked map[string]bool) {
	for _, item := range items {
		if item.FeatureID != "" {
			linked[item.FeatureID] = true
		}
		collectLinked(item.Items, linked)
	}
}

// runNote says what an unfinished work order's newest run is doing or
// waiting for, and why.
func (describer StateDescriber) runNote(ctx context.Context, item feature.Feature) string {
	if describer.Runs == nil || item.State.IsTerminal() {
		return ""
	}
	runs, err := describer.Runs.RunsForFeature(ctx, item.ID)
	if err != nil {
		return ""
	}
	run, ok := newest(runs)
	if !ok {
		return ""
	}
	note := "; run " + string(run.Status)
	if run.WaitKind != "" && run.Status == execution.RunStatusWaitingForUser {
		note += " (" + string(run.WaitKind) + ")"
	}
	if reason := strings.TrimSpace(run.Reason); reason != "" && run.Status != execution.RunStatusRunning {
		if len(reason) > 600 {
			reason = reason[:600] + "…"
		}
		note += ": " + strings.ReplaceAll(reason, "\n", " ")
	}
	return note
}
