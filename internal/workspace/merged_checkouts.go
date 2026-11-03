package workspace

import (
	"context"
	"log"
	"time"
)

// MergedLister finds the workspaces whose work order merged before a time.
type MergedLister interface {
	MergedBefore(ctx context.Context, cutoff time.Time) ([]string, error)
}

// mergedCheckoutKeep is how long a merged work order's checkouts stay, in
// case anything still reads them just after the merge.
const mergedCheckoutKeep = 24 * time.Hour

// RemoveMergedCheckouts deletes the lead's and reviewer's checkouts of work
// orders merged a day ago or more: the work lives on in main, and a checkout
// with its installed dependencies is a few hundred megabytes that otherwise
// stays for good (SERVER_COMPLETE_PLAN.md, S6). A checkout already gone is
// skipped.
func (service *Service) RemoveMergedCheckouts(ctx context.Context) error {
	lister, ok := service.store.(MergedLister)
	if !ok {
		return nil
	}
	merged, err := lister.MergedBefore(ctx, service.now().Add(-mergedCheckoutKeep))
	if err != nil {
		return err
	}
	for _, id := range merged {
		for _, checkouts := range []CheckoutManager{service.checkouts, service.reviewerCheckouts} {
			if disposable, ok := checkouts.(DisposableCheckoutManager); ok {
				if err := disposable.Remove(ctx, id); err != nil {
					log.Printf("remove merged checkout %s: %v", id, err)
				}
			}
		}
	}
	return nil
}

// KeepRemovingMergedCheckouts runs RemoveMergedCheckouts every hour until
// ctx ends.
func (service *Service) KeepRemovingMergedCheckouts(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := service.RemoveMergedCheckouts(ctx); err != nil {
			log.Printf("remove merged checkouts: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
