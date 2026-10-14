package conversations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

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
	// Usage, when set, reports each agent's account usage as its worker last
	// saw it, leaving out agents whose worker does not answer; nil means the
	// workers cannot be asked. The assistant weighs it when it chooses agents
	// (ADR-027).
	Usage func(ctx context.Context) []agent.Usage
	// Settings, when set, names the agent and model the assistant runs on.
	Settings func(ctx context.Context) (Settings, error)
	Now      func() time.Time
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
	if len(agents) == 0 {
		builder.WriteString("- none: the user has not added an agent yet, so no work can start. Tell the user to add " +
			"one on the Agents page; propose no work until then.\n")
		return
	}
	var models []ModelChoice
	if describer.Models != nil {
		models = describer.Models(ctx)
	}
	var usage []agent.Usage
	if describer.Usage != nil {
		usage = describer.Usage(ctx)
	}
	now := time.Now()
	if describer.Now != nil {
		now = describer.Now()
	}
	providers := map[project.AgentProvider]bool{}
	for _, candidate := range agents {
		providers[candidate.Provider] = true
		offered := make([]string, 0)
		for _, model := range models {
			if model.Provider == candidate.Provider {
				offered = append(offered, model.ID+" ("+model.Display+")")
			}
		}
		list := strings.Join(offered, ", ")
		if list == "" {
			list = "not known yet"
		}
		builder.WriteString(fmt.Sprintf("- agent %s (%s, %s): %s; models: %s\n",
			candidate.ID, candidate.Name, candidate.Provider, usageNote(candidate.ID, usage, now), list))
	}
	if len(providers) > 1 {
		builder.WriteString("Agents of more than one provider are connected: give each full work order a lead and a " +
			"reviewer of different providers.\n")
	}
	if describer.Settings != nil {
		if settings, err := describer.Settings(ctx); err == nil && settings.Agent != "" {
			builder.WriteString(fmt.Sprintf("You, the assistant, run on agent %s with %s; your turns use its usage too.\n",
				settings.Agent, settings.Model))
		}
	}
}

// usageNote says what is known of an agent's usage: not connected, at its
// limit, or how much is left and when its limits reset.
func usageNote(agentID string, usage []agent.Usage, now time.Time) string {
	if usage == nil {
		return "usage unknown"
	}
	index := slices.IndexFunc(usage, func(reported agent.Usage) bool { return reported.Agent == agentID })
	if index < 0 {
		return "not connected right now (its worker does not answer), do not choose it"
	}
	reported := usage[index]
	if limited, until := reported.LimitedUntil(now); limited {
		if until != nil {
			return "at its usage limit until " + until.UTC().Format(usageTime) + ", do not choose it"
		}
		return "at its usage limit, do not choose it"
	}
	if reported.ObservedAt == nil || len(reported.Windows) == 0 {
		return "usage not reported yet (it has not worked since it started)"
	}
	windows := make([]string, 0, len(reported.Windows))
	for _, window := range reported.Windows {
		text := window.Name
		if window.UsedPercent != nil {
			text += fmt.Sprintf(" %.0f%% used", *window.UsedPercent)
		}
		if window.ResetsAt != nil {
			text += ", resets " + window.ResetsAt.UTC().Format(usageTime)
		}
		windows = append(windows, text)
	}
	note := strings.Join(windows, "; ")
	if left, known := reported.Remaining(now); known {
		note = fmt.Sprintf("%.0f%% left (%s)", left, note)
	}
	return "usage " + note + ", as of " + reported.ObservedAt.UTC().Format(usageTime)
}

const usageTime = "Jan 2 15:04 UTC"

func (describer StateDescriber) writeProject(ctx context.Context, builder *strings.Builder, current project.Project) {
	builder.WriteString(fmt.Sprintf("\n## This project: %s (%s)\n", current.Name, current.ID))
	builder.WriteString(fmt.Sprintf("Defaults: merge %s; autonomy %s; independent tests %t.\n",
		current.MergePolicy, current.AutonomyPolicy, current.IndependentAcceptanceTests))
	if current.AssistantBuilds() {
		builder.WriteString("How you work here: you build everything. You own the whole project: write the vision, " +
			"set up the stack, create and start the work orders, and take over work orders the user creates too. " +
			"Your actions apply without cards, except those using a premium model.\n")
	} else {
		builder.WriteString("How you work here: you assist. Each of your actions waits for the user's approval " +
			"as a card.\n")
	}
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
		if item.Background {
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
		if !linked[item.ID] {
			outside = " — outside the vision"
		}
		builder.WriteString(fmt.Sprintf("- %s %q (%s, %s): %s, steps %s, %s%s%s\n",
			kind, item.Title, item.ID, creatorNote(item), item.State, stepsName(item.Steps),
			agentsNote(item), outside, describer.runNote(ctx, item)))
	}
	if listed == 0 {
		builder.WriteString("- none yet\n")
	}
}

// creatorNote says whose a work order is (ADR-027): the assistant acts on
// its own only on its own.
func creatorNote(item feature.Feature) string {
	if item.CreatedBy == feature.CreatedByAssistant {
		return "yours"
	}
	return "the user's"
}

// agentsNote names a work order's agents and models, or says they are not
// chosen yet.
func agentsNote(item feature.Feature) string {
	if item.AgentProviders.IsZero() || item.AgentModels.Lead == "" {
		return "agents not chosen yet"
	}
	if item.Steps.SkipReviewer {
		return fmt.Sprintf("lead %s on %s", item.AgentProviders.LeadAgent, item.AgentModels.Lead)
	}
	return fmt.Sprintf("lead %s on %s, reviewer %s on %s",
		item.AgentProviders.LeadAgent, item.AgentModels.Lead,
		item.AgentProviders.ReviewerAgent, item.AgentModels.Reviewer)
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
