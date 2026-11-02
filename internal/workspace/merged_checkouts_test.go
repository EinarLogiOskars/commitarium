package workspace

import (
	"context"
	"slices"
	"testing"
	"time"
)

type mergedStore struct {
	Store
	cutoff time.Time
}

func (store *mergedStore) MergedBefore(_ context.Context, cutoff time.Time) ([]string, error) {
	store.cutoff = cutoff
	return []string{"wsp_old"}, nil
}

type removingCheckouts struct {
	CheckoutManager
	removed []string
}

func (checkouts *removingCheckouts) Remove(_ context.Context, id string) error {
	checkouts.removed = append(checkouts.removed, id)
	return nil
}

func TestMergedCheckoutsGoADayAfterTheMerge(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	store := &mergedStore{}
	lead, reviewer := &removingCheckouts{}, &removingCheckouts{}
	service := &Service{store: store, checkouts: lead, reviewerCheckouts: reviewer, now: func() time.Time { return now }}
	if err := service.RemoveMergedCheckouts(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !store.cutoff.Equal(now.Add(-24 * time.Hour)) {
		t.Fatalf("cutoff = %v", store.cutoff)
	}
	if !slices.Equal(lead.removed, []string{"wsp_old"}) || !slices.Equal(reviewer.removed, []string{"wsp_old"}) {
		t.Fatalf("removed lead %v, reviewer %v", lead.removed, reviewer.removed)
	}
}
