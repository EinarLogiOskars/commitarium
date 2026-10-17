// Package modelguide holds the official model guide that ships with
// Commitarium and the user's per-model overrides of it (ADR-031).
//
// The guide rates each exact model ID with a tier. Nothing is inferred from a
// model's family name or version: a model the guide does not list, and the
// user has not rated, is unrated.
package modelguide

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Tier is how a model is rated.
type Tier string

const (
	TierPremium  Tier = "premium"
	TierFrontier Tier = "frontier"
	TierStrong   Tier = "strong"
	TierLight    Tier = "light"
	TierAvoid    Tier = "avoid"
)

// Valid reports whether the tier is one a model can be rated with.
func (tier Tier) Valid() bool {
	switch tier {
	case TierPremium, TierFrontier, TierStrong, TierLight, TierAvoid:
		return true
	}
	return false
}

// Source says where an entry's tier comes from.
type Source string

const (
	SourceOfficial Source = "official"
	SourceUser     Source = "user"
	SourceUnrated  Source = "unrated"
)

var ErrInvalid = errors.New("invalid model guide request")

// Model is one model of the official guide.
type Model struct {
	ID   string `json:"id"`
	Tier Tier   `json:"tier"`
	Note string `json:"note"`
	// Price is its API price, for providers whose CLI does not report a
	// turn's cost (Codex); nil when the guide has none.
	Price *Price `json:"price,omitempty"`
}

// Price is a model's API price in US dollars per million tokens.
type Price struct {
	Input       float64 `json:"input"`
	CachedInput float64 `json:"cached_input"`
	Output      float64 `json:"output"`
}

// Cost prices a turn's tokens. Input excludes cached reads; cache writes are
// priced as input.
func (price Price) Cost(input, cachedInput, cacheWrite, output int64) float64 {
	return (float64(input+cacheWrite)*price.Input + float64(cachedInput)*price.CachedInput +
		float64(output)*price.Output) / 1_000_000
}

// Guide is the official model guide.
type Guide struct {
	AsOf string `json:"as_of"`
	// PricesAsOf dates the prices.
	PricesAsOf string  `json:"prices_as_of"`
	Models     []Model `json:"models"`
}

//go:embed guide.json
var officialJSON []byte

var official = mustParse(officialJSON)

func mustParse(contents []byte) Guide {
	var guide Guide
	if err := json.Unmarshal(contents, &guide); err != nil {
		panic(fmt.Sprintf("parse the official model guide: %v", err))
	}
	seen := make(map[string]bool, len(guide.Models))
	for _, model := range guide.Models {
		if !validModelID(model.ID) || !model.Tier.Valid() || seen[model.ID] {
			panic(fmt.Sprintf("the official model guide has an invalid entry %+v", model))
		}
		seen[model.ID] = true
	}
	return guide
}

// Official returns the official guide.
func Official() Guide {
	return Guide{AsOf: official.AsOf, PricesAsOf: official.PricesAsOf, Models: slices.Clone(official.Models)}
}

// PriceOf returns a model's API price from the official guide.
func (service *Service) PriceOf(id string) (Price, bool) {
	model, ok := service.guide.model(id)
	if !ok || model.Price == nil {
		return Price{}, false
	}
	return *model.Price, true
}

// PricesAsOf dates the guide's prices.
func (service *Service) PricesAsOf() string { return service.guide.PricesAsOf }

func (guide Guide) model(id string) (Model, bool) {
	for _, model := range guide.Models {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

// Override is the user's tier for one model. OfficialTier is the official
// tier the override replaced (empty when the model was unrated), so a release
// that changes it can be shown to the user.
type Override struct {
	ModelID      string
	Tier         Tier
	OfficialTier Tier
	UpdatedAt    time.Time
}

// Entry is one model as the guide rates it once the user's overrides apply.
type Entry struct {
	ID string `json:"id"`
	// Tier is empty for an unrated model.
	Tier         Tier   `json:"tier,omitempty"`
	Note         string `json:"note,omitempty"`
	Source       Source `json:"source"`
	OfficialTier Tier   `json:"official_tier,omitempty"`
	// OfficialChanged marks an override whose official tier has changed
	// since the user set it.
	OfficialChanged bool `json:"official_changed,omitempty"`
}

// NeedsApproval reports whether the assistant must ask the user before using
// the model: Premium models, and unrated ones, which may be priced as a top
// tier.
func (entry Entry) NeedsApproval() bool {
	return entry.Source == SourceUnrated || entry.Tier == TierPremium
}

func entryFor(guide Guide, overrides map[string]Override, id string) Entry {
	model, rated := guide.model(id)
	entry := Entry{ID: id, Source: SourceUnrated}
	if rated {
		entry = Entry{ID: id, Tier: model.Tier, Note: model.Note, Source: SourceOfficial, OfficialTier: model.Tier}
	}
	if override, ok := overrides[id]; ok {
		entry.Tier, entry.Source = override.Tier, SourceUser
		entry.OfficialChanged = override.OfficialTier != entry.OfficialTier
	}
	return entry
}

// Store keeps the user's overrides.
type Store interface {
	ListModelGuideOverrides(ctx context.Context) ([]Override, error)
	SaveModelGuideOverride(ctx context.Context, override Override) error
	DeleteModelGuideOverride(ctx context.Context, modelID string) error
}

// Service merges the official guide with the user's overrides.
type Service struct {
	store Store
	guide Guide
	now   func() time.Time
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("model guide store is required")
	}
	return &Service{store: store, guide: Official(), now: func() time.Time { return time.Now().UTC() }}, nil
}

func (service *Service) overrides(ctx context.Context) (map[string]Override, error) {
	listed, err := service.store.ListModelGuideOverrides(ctx)
	if err != nil {
		return nil, fmt.Errorf("list model guide overrides: %w", err)
	}
	overrides := make(map[string]Override, len(listed))
	for _, override := range listed {
		overrides[override.ModelID] = override
	}
	return overrides, nil
}

// AsOf is the official guide's date.
func (service *Service) AsOf() string { return service.guide.AsOf }

// Entry returns how the guide rates one model.
func (service *Service) Entry(ctx context.Context, id string) (Entry, error) {
	overrides, err := service.overrides(ctx)
	if err != nil {
		return Entry{}, err
	}
	return entryFor(service.guide, overrides, strings.TrimSpace(id)), nil
}

// Entries returns every model the official guide lists, the user rated, or
// extra names (the models connected agents offer), in that order, once each.
func (service *Service) Entries(ctx context.Context, extra []string) ([]Entry, error) {
	overrides, err := service.overrides(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(service.guide.Models)+len(overrides)+len(extra))
	for _, model := range service.guide.Models {
		ids = append(ids, model.ID)
	}
	userRated := make([]string, 0, len(overrides))
	for id := range overrides {
		userRated = append(userRated, id)
	}
	slices.Sort(userRated)
	ids = append(ids, userRated...)
	ids = append(ids, extra...)
	seen := make(map[string]bool, len(ids))
	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id == "" || seen[id] {
			continue
		}
		seen[id] = true
		entries = append(entries, entryFor(service.guide, overrides, id))
	}
	return entries, nil
}

// RequiresApproval reports whether the assistant must ask before using the
// model. An empty model needs nothing.
func (service *Service) RequiresApproval(ctx context.Context, model string) (bool, error) {
	if strings.TrimSpace(model) == "" {
		return false, nil
	}
	entry, err := service.Entry(ctx, model)
	if err != nil {
		return false, err
	}
	return entry.NeedsApproval(), nil
}

// SetTier rates a model for the user. Setting the tier an override already
// has keeps it and acknowledges a changed official tier.
func (service *Service) SetTier(ctx context.Context, id string, tier Tier) (Entry, error) {
	id = strings.TrimSpace(id)
	if !validModelID(id) {
		return Entry{}, fmt.Errorf("%w: model IDs are 1 to 128 characters without spaces", ErrInvalid)
	}
	if !tier.Valid() {
		return Entry{}, fmt.Errorf("%w: tier must be premium, frontier, strong, light, or avoid", ErrInvalid)
	}
	officialTier := Tier("")
	if model, ok := service.guide.model(id); ok {
		officialTier = model.Tier
	}
	if err := service.store.SaveModelGuideOverride(ctx, Override{
		ModelID: id, Tier: tier, OfficialTier: officialTier, UpdatedAt: service.now(),
	}); err != nil {
		return Entry{}, fmt.Errorf("save model guide override for %q: %w", id, err)
	}
	return service.Entry(ctx, id)
}

// Reset returns a model to the official guide.
func (service *Service) Reset(ctx context.Context, id string) (Entry, error) {
	id = strings.TrimSpace(id)
	if !validModelID(id) {
		return Entry{}, fmt.Errorf("%w: model IDs are 1 to 128 characters without spaces", ErrInvalid)
	}
	if err := service.store.DeleteModelGuideOverride(ctx, id); err != nil {
		return Entry{}, fmt.Errorf("delete model guide override for %q: %w", id, err)
	}
	return service.Entry(ctx, id)
}

// MigrateAskBefore turns the assistant's former "ask before using" model
// families into overrides for the given models (ADR-031): a model the user
// marked that the guide does not rate Premium becomes Premium, and an
// officially Premium model the user unmarked becomes Frontier. Models the
// user already rated are left alone.
func (service *Service) MigrateAskBefore(ctx context.Context, families, models []string) error {
	overrides, err := service.overrides(ctx)
	if err != nil {
		return err
	}
	for _, id := range models {
		id = strings.TrimSpace(id)
		if !validModelID(id) {
			continue
		}
		if _, rated := overrides[id]; rated {
			continue
		}
		marked := matchesFamily(id, families)
		model, listed := service.guide.model(id)
		officiallyPremium := listed && model.Tier == TierPremium
		var tier Tier
		switch {
		case marked && !officiallyPremium:
			tier = TierPremium
		case !marked && officiallyPremium:
			tier = TierFrontier
		default:
			continue
		}
		if _, err := service.SetTier(ctx, id, tier); err != nil {
			return err
		}
		overrides[id] = Override{ModelID: id, Tier: tier}
	}
	return nil
}

func matchesFamily(model string, families []string) bool {
	model = strings.ToLower(model)
	for _, family := range families {
		if family = strings.ToLower(strings.TrimSpace(family)); family != "" && strings.Contains(model, family) {
			return true
		}
	}
	return false
}

func validModelID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, " \t\r\n")
}
