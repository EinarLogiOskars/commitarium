package modelguide

import (
	"context"
	"errors"
	"testing"
)

type memoryStore struct{ overrides map[string]Override }

func (store *memoryStore) ListModelGuideOverrides(context.Context) ([]Override, error) {
	listed := make([]Override, 0, len(store.overrides))
	for _, override := range store.overrides {
		listed = append(listed, override)
	}
	return listed, nil
}

func (store *memoryStore) SaveModelGuideOverride(_ context.Context, override Override) error {
	store.overrides[override.ModelID] = override
	return nil
}

func (store *memoryStore) DeleteModelGuideOverride(_ context.Context, id string) error {
	delete(store.overrides, id)
	return nil
}

func newTestService(t *testing.T, models ...Model) (*Service, *memoryStore) {
	t.Helper()
	store := &memoryStore{overrides: map[string]Override{}}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	service.guide = Guide{AsOf: "2026-10-17", Models: models}
	return service, store
}

func TestOfficialGuideParses(t *testing.T) {
	guide := Official()
	if guide.AsOf == "" || len(guide.Models) == 0 {
		t.Fatalf("official guide = %+v", guide)
	}
	if model, ok := guide.model("claude-fable-5-1"); !ok || model.Tier != TierPremium {
		t.Fatalf("fable = %+v %t", model, ok)
	}
}

func TestEntryMergesOfficialOverridesAndUnrated(t *testing.T) {
	service, _ := newTestService(t,
		Model{ID: "opus", Tier: TierFrontier, Note: "planner"},
		Model{ID: "top", Tier: TierPremium})
	ctx := t.Context()

	entry, err := service.Entry(ctx, "opus")
	if err != nil || entry.Source != SourceOfficial || entry.Tier != TierFrontier || entry.Note != "planner" || entry.NeedsApproval() {
		t.Fatalf("official entry = %+v err=%v", entry, err)
	}
	if entry, _ := service.Entry(ctx, "new-model"); entry.Source != SourceUnrated || entry.Tier != "" || !entry.NeedsApproval() {
		t.Fatalf("unrated entry = %+v", entry)
	}
	if entry, _ := service.Entry(ctx, "top"); !entry.NeedsApproval() {
		t.Fatalf("premium entry = %+v", entry)
	}

	entry, err = service.SetTier(ctx, "opus", TierAvoid)
	if err != nil || entry.Source != SourceUser || entry.Tier != TierAvoid || entry.OfficialTier != TierFrontier || entry.OfficialChanged {
		t.Fatalf("overridden entry = %+v err=%v", entry, err)
	}
	if entry, _ := service.SetTier(ctx, "new-model", TierStrong); entry.NeedsApproval() || entry.Source != SourceUser {
		t.Fatalf("rated unrated entry = %+v", entry)
	}
	if entry, _ := service.Reset(ctx, "opus"); entry.Source != SourceOfficial || entry.Tier != TierFrontier {
		t.Fatalf("reset entry = %+v", entry)
	}
}

func TestEntryMarksOverridesWhoseOfficialTierChanged(t *testing.T) {
	service, store := newTestService(t, Model{ID: "opus", Tier: TierStrong})
	store.overrides["opus"] = Override{ModelID: "opus", Tier: TierFrontier, OfficialTier: TierFrontier}
	store.overrides["fresh"] = Override{ModelID: "fresh", Tier: TierLight}

	if entry, _ := service.Entry(t.Context(), "opus"); !entry.OfficialChanged || entry.Tier != TierFrontier {
		t.Fatalf("changed entry = %+v", entry)
	}
	if entry, _ := service.Entry(t.Context(), "fresh"); entry.OfficialChanged {
		t.Fatalf("still unrated officially = %+v", entry)
	}
	// Keeping the user's tier acknowledges the change.
	if entry, _ := service.SetTier(t.Context(), "opus", TierFrontier); entry.OfficialChanged {
		t.Fatalf("kept entry = %+v", entry)
	}
}

func TestEntriesListOfficialUserAndExtraModelsOnce(t *testing.T) {
	service, store := newTestService(t, Model{ID: "a", Tier: TierStrong}, Model{ID: "b", Tier: TierLight})
	store.overrides["z"] = Override{ModelID: "z", Tier: TierLight}
	entries, err := service.Entries(t.Context(), []string{"b", "c", " ", "c"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	if got := ids; len(got) != 4 || got[0] != "a" || got[1] != "b" || got[2] != "z" || got[3] != "c" {
		t.Fatalf("ids = %v", got)
	}
}

func TestSetTierRejectsInvalidInput(t *testing.T) {
	service, _ := newTestService(t)
	if _, err := service.SetTier(t.Context(), "has space", TierStrong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad id err = %v", err)
	}
	if _, err := service.SetTier(t.Context(), "ok", Tier("great")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad tier err = %v", err)
	}
}

func TestRequiresApproval(t *testing.T) {
	service, _ := newTestService(t, Model{ID: "fable", Tier: TierPremium}, Model{ID: "sonnet", Tier: TierStrong})
	for model, want := range map[string]bool{"": false, "fable": true, "sonnet": false, "brand-new": true} {
		if got, err := service.RequiresApproval(t.Context(), model); err != nil || got != want {
			t.Errorf("RequiresApproval(%q) = %t, %v; want %t", model, got, err, want)
		}
	}
}

func TestMigrateAskBeforeTurnsFamiliesIntoOverrides(t *testing.T) {
	service, store := newTestService(t,
		Model{ID: "claude-fable-5-1", Tier: TierPremium},
		Model{ID: "gpt-6-astra", Tier: TierPremium},
		Model{ID: "claude-opus-5-5", Tier: TierFrontier},
		Model{ID: "claude-sonnet-5-5", Tier: TierStrong})
	store.overrides["claude-sonnet-5-5"] = Override{ModelID: "claude-sonnet-5-5", Tier: TierLight}

	// The user added "opus" and "sonnet", and removed the default "astra".
	err := service.MigrateAskBefore(t.Context(), []string{"fable", "opus", "sonnet"},
		[]string{"claude-fable-5-1", "gpt-6-astra", "claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-9"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Tier{
		"gpt-6-astra":       TierFrontier, // unmarked default
		"claude-opus-5-5":   TierPremium,  // marked by the user
		"claude-opus-9":     TierPremium,  // unrated, marked
		"claude-sonnet-5-5": TierLight,    // already rated by the user
	}
	if len(store.overrides) != len(want) {
		t.Fatalf("overrides = %+v", store.overrides)
	}
	for id, tier := range want {
		if store.overrides[id].Tier != tier {
			t.Errorf("%s = %+v, want %s", id, store.overrides[id], tier)
		}
	}
}
