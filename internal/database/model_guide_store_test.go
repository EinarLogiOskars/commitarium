package database

import (
	"testing"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/modelguide"
)

func TestModelGuideStoreSavesListsAndDeletesOverrides(t *testing.T) {
	db, _ := newTestExecutionStore(t)
	store := NewModelGuideStore(db)
	ctx := t.Context()
	at := time.Date(2026, time.October, 17, 9, 0, 0, 0, time.UTC)

	if err := store.SaveModelGuideOverride(ctx, modelguide.Override{
		ModelID: "claude-opus-5", Tier: modelguide.TierFrontier, OfficialTier: modelguide.TierAvoid, UpdatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveModelGuideOverride(ctx, modelguide.Override{
		ModelID: "new-model", Tier: modelguide.TierStrong, UpdatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	// Saving again replaces the override.
	if err := store.SaveModelGuideOverride(ctx, modelguide.Override{
		ModelID: "claude-opus-5", Tier: modelguide.TierPremium, OfficialTier: modelguide.TierAvoid, UpdatedAt: at.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListModelGuideOverrides(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("overrides = %+v err=%v", listed, err)
	}
	if first := listed[0]; first.ModelID != "claude-opus-5" || first.Tier != modelguide.TierPremium ||
		first.OfficialTier != modelguide.TierAvoid || !first.UpdatedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("first = %+v", first)
	}
	if second := listed[1]; second.ModelID != "new-model" || second.OfficialTier != "" {
		t.Fatalf("second = %+v", second)
	}

	if err := store.DeleteModelGuideOverride(ctx, "claude-opus-5"); err != nil {
		t.Fatal(err)
	}
	if listed, _ := store.ListModelGuideOverrides(ctx); len(listed) != 1 {
		t.Fatalf("after delete = %+v", listed)
	}
	if err := store.SaveModelGuideOverride(ctx, modelguide.Override{
		ModelID: "x", Tier: modelguide.Tier("great"), UpdatedAt: at,
	}); err == nil {
		t.Fatal("an invalid tier was stored")
	}
}
