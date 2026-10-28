package execution

import (
	"context"
	"fmt"
	"strings"
)

// MergeQueueStore keeps each project's merge queue (ADR-036): the work orders
// whose gates passed and that merge on their own, in the order they joined.
type MergeQueueStore interface {
	// JoinMergeQueue adds the run at the back of its project's queue; a run
	// already in line keeps its place.
	JoinMergeQueue(ctx context.Context, projectID, runID string) error
	// LeaveMergeQueue takes the run out of line, reporting its project when it
	// was in one.
	LeaveMergeQueue(ctx context.Context, runID string) (string, bool, error)
	// MergeQueue lists a project's runs in line, first first.
	MergeQueue(ctx context.Context, projectID string) ([]string, error)
	// MergeQueueProjects lists the projects with a run in line.
	MergeQueueProjects(ctx context.Context) ([]string, error)
}

// JoinMergeQueue adds the run at the back of its project's merge queue.
func (s *Service) JoinMergeQueue(ctx context.Context, projectID, runID string) error {
	if queue, ok := s.store.(MergeQueueStore); ok {
		return queue.JoinMergeQueue(ctx, projectID, runID)
	}
	return nil
}

// LeaveMergeQueue takes the run out of its project's merge queue.
func (s *Service) LeaveMergeQueue(ctx context.Context, runID string) (string, bool, error) {
	if queue, ok := s.store.(MergeQueueStore); ok {
		return queue.LeaveMergeQueue(ctx, runID)
	}
	return "", false, nil
}

// MergeQueue lists a project's runs in line to merge, first first.
func (s *Service) MergeQueue(ctx context.Context, projectID string) ([]string, error) {
	if queue, ok := s.store.(MergeQueueStore); ok {
		return queue.MergeQueue(ctx, projectID)
	}
	return nil, nil
}

// MergeQueueProjects lists the projects with a run in line to merge.
func (s *Service) MergeQueueProjects(ctx context.Context) ([]string, error) {
	if queue, ok := s.store.(MergeQueueStore); ok {
		return queue.MergeQueueProjects(ctx)
	}
	return nil, nil
}

// mergeQueuePrefix starts the reason of a run waiting in its merge queue.
const mergeQueuePrefix = "Waiting to merge: "

// MergeQueueReason says, in plain words, where a run waits in its project's
// merge queue; place 1 is next.
func MergeQueueReason(place int) string {
	line := "next in line"
	if place > 1 {
		line = ordinal(place) + " in line"
	}
	return mergeQueuePrefix + line + ". Work orders merge one at a time; when its turn comes, " +
		"the lead first brings in what merged before it."
}

// InMergeQueue reports whether a run's reason says it waits in its merge
// queue.
func InMergeQueue(reason string) bool {
	return strings.HasPrefix(reason, mergeQueuePrefix)
}

// MergeQueuePlace is a waiting run's place in words ("2nd in line"), from
// its reason.
func MergeQueuePlace(reason string) (string, bool) {
	place, found := strings.CutPrefix(reason, mergeQueuePrefix)
	place, _, _ = strings.Cut(place, ".")
	return place, found
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
