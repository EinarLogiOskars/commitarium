package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/EinarLogiOskars/commitarium/internal/execution"
	"github.com/EinarLogiOskars/commitarium/internal/project"
)

// ProjectUsageService totals provider usage across a project.
type ProjectUsageService interface {
	ProjectAttemptUsage(ctx context.Context, projectID string) ([]execution.ProjectAttemptUsage, error)
}

// costResponse says what usage cost (ADR-031): turns on API-key accounts in
// dollars, priced by the provider's own cost or the model guide's price;
// API-key turns with neither, subscription turns, and turns whose account
// did not say how it pays, in tokens.
type costResponse struct {
	APICostUSD   float64            `json:"api_cost_usd"`
	APIUnpriced  tokenUsageResponse `json:"api_unpriced"`
	Subscription tokenUsageResponse `json:"subscription"`
	Unknown      tokenUsageResponse `json:"unknown"`
	PricesAsOf   string             `json:"prices_as_of,omitempty"`
}

// costTally sums usage into a costResponse.
type costTally struct {
	apiCost                         float64
	unpriced, subscription, unknown execution.TokenUsage
}

func (tally *costTally) add(cost float64, priced bool, attempt execution.RoleAttemptUsage) {
	switch {
	case priced:
		tally.apiCost += cost
	case attempt.Billing == "api_key":
		tally.unpriced = tally.unpriced.Add(attempt.Usage)
	case attempt.Billing == "subscription":
		tally.subscription = tally.subscription.Add(attempt.Usage)
	default:
		tally.unknown = tally.unknown.Add(attempt.Usage)
	}
}

func (tally costTally) response(pricesAsOf string) costResponse {
	return costResponse{
		APICostUSD: tally.apiCost, APIUnpriced: newTokenUsageResponse(tally.unpriced),
		Subscription: newTokenUsageResponse(tally.subscription), Unknown: newTokenUsageResponse(tally.unknown),
		PricesAsOf: pricesAsOf,
	}
}

// attemptCost is an API-key attempt's cost in dollars: the provider's own
// when it reported one, else its tokens at the model guide's price. A
// subscription or unknown account, or a model without a price, is not
// priced.
func (api *API) attemptCost(attempt execution.RoleAttemptUsage) (float64, bool) {
	if attempt.Billing != "api_key" {
		return 0, false
	}
	if attempt.CostUSD != nil {
		return *attempt.CostUSD, true
	}
	if api.modelGuide == nil {
		return 0, false
	}
	price, ok := api.modelGuide.PriceOf(attempt.Model)
	if !ok {
		return 0, false
	}
	usage := attempt.Usage
	return price.Cost(usage.InputTokens, usage.CachedInputTokens, usage.CacheWriteTokens, usage.OutputTokens), true
}

func (api *API) pricesAsOf() string {
	if api.modelGuide == nil {
		return ""
	}
	return api.modelGuide.PricesAsOf()
}

// costUSD is a bucket's priced cost, absent when nothing in it was priced.
func costUSD(cost float64, priced bool) *float64 {
	if !priced {
		return nil
	}
	return &cost
}

type projectUsageEntry struct {
	tokenUsageResponse
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

type projectModelUsage struct {
	Model string `json:"model"`
	projectUsageEntry
}

type projectWorkOrderUsage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	projectUsageEntry
}

type projectUsageResponse struct {
	Total tokenUsageResponse `json:"total"`
	Cost  costResponse       `json:"cost"`
	// Models and WorkOrders are ordered by cost, then input tokens.
	Models     []projectModelUsage     `json:"models"`
	WorkOrders []projectWorkOrderUsage `json:"work_orders"`
	// Assistant is the assistant's own conversation turns in the project.
	Assistant projectUsageEntry `json:"assistant"`
}

type usageBucket struct {
	usage  execution.TokenUsage
	cost   float64
	priced bool
}

func (bucket *usageBucket) add(usage execution.TokenUsage, cost float64, priced bool) {
	bucket.usage = bucket.usage.Add(usage)
	if priced {
		bucket.cost, bucket.priced = bucket.cost+cost, true
	}
}

func (bucket usageBucket) entry() projectUsageEntry {
	return projectUsageEntry{tokenUsageResponse: newTokenUsageResponse(bucket.usage), CostUSD: costUSD(bucket.cost, bucket.priced)}
}

// costlier orders entries by cost, then by input tokens read.
func costlier(left, right projectUsageEntry) bool {
	leftCost, rightCost := 0.0, 0.0
	if left.CostUSD != nil {
		leftCost = *left.CostUSD
	}
	if right.CostUSD != nil {
		rightCost = *right.CostUSD
	}
	if leftCost != rightCost {
		return leftCost > rightCost
	}
	read := func(entry projectUsageEntry) int64 {
		return entry.InputTokens + entry.CachedInputTokens + entry.CacheWriteTokens
	}
	return read(left) > read(right)
}

// getProjectUsageHandler totals what a project's work and its assistant have
// used, by model and by work order (ADR-031).
func (api *API) getProjectUsageHandler(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectID")
	if _, err := api.projects.GetByID(r.Context(), projectID); err != nil {
		if errors.Is(err, project.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project_not_found", "project not found")
			return
		}
		log.Printf("verify project %q for token usage: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	attempts, err := api.projectUsage.ProjectAttemptUsage(r.Context(), projectID)
	if err != nil {
		log.Printf("list token usage for project %q: %v", projectID, err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
		return
	}
	titles := map[string]string{}
	if features, err := api.features.List(r.Context(), projectID); err == nil {
		for _, stored := range features {
			titles[stored.ID] = stored.Title
		}
	}
	var total execution.TokenUsage
	var tally costTally
	models := map[string]*usageBucket{}
	orders := map[string]*usageBucket{}
	var assistant usageBucket
	for _, attempt := range attempts {
		cost, priced := api.attemptCost(attempt.RoleAttemptUsage)
		total = total.Add(attempt.Usage)
		tally.add(cost, priced, attempt.RoleAttemptUsage)
		if attempt.Model != "" {
			if models[attempt.Model] == nil {
				models[attempt.Model] = &usageBucket{}
			}
			models[attempt.Model].add(attempt.Usage, cost, priced)
		}
		if attempt.FeatureID == "" {
			assistant.add(attempt.Usage, cost, priced)
			continue
		}
		if orders[attempt.FeatureID] == nil {
			orders[attempt.FeatureID] = &usageBucket{}
		}
		orders[attempt.FeatureID].add(attempt.Usage, cost, priced)
	}
	response := projectUsageResponse{
		Total: newTokenUsageResponse(total), Cost: tally.response(api.pricesAsOf()),
		Models: make([]projectModelUsage, 0, len(models)), WorkOrders: make([]projectWorkOrderUsage, 0, len(orders)),
		Assistant: assistant.entry(),
	}
	for model, bucket := range models {
		response.Models = append(response.Models, projectModelUsage{Model: model, projectUsageEntry: bucket.entry()})
	}
	sort.Slice(response.Models, func(i, j int) bool {
		left, right := response.Models[i], response.Models[j]
		if costlier(left.projectUsageEntry, right.projectUsageEntry) != costlier(right.projectUsageEntry, left.projectUsageEntry) {
			return costlier(left.projectUsageEntry, right.projectUsageEntry)
		}
		return left.Model < right.Model
	})
	for id, bucket := range orders {
		title := titles[id]
		if title == "" {
			title = id
		}
		response.WorkOrders = append(response.WorkOrders, projectWorkOrderUsage{ID: id, Title: title, projectUsageEntry: bucket.entry()})
	}
	sort.Slice(response.WorkOrders, func(i, j int) bool {
		left, right := response.WorkOrders[i], response.WorkOrders[j]
		if costlier(left.projectUsageEntry, right.projectUsageEntry) != costlier(right.projectUsageEntry, left.projectUsageEntry) {
			return costlier(left.projectUsageEntry, right.projectUsageEntry)
		}
		return left.ID < right.ID
	})
	writeJSON(w, http.StatusOK, response, "project token usage")
}
