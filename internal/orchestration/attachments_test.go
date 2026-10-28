package orchestration

import (
	"context"
	"slices"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A turn whose instructions name attached files gets them in the checkout
// of the agent that reads them, and looks at the images among them.
func TestATurnLooksAtTheScreenshotsItsInstructionsName(t *testing.T) {
	var delivered []string
	var reviewer bool
	starter := &RemoteLeadStarter{}
	starter.DeliverAttachments(func(_ context.Context, projectID string, role worker.Role, workspaceID string, names []string) ([]string, error) {
		if projectID != "prj_1" || workspaceID != "ws_1" {
			t.Fatalf("delivered to %s/%s", projectID, workspaceID)
		}
		reviewer = role == worker.RoleReviewer
		delivered = names
		paths := make([]string, 0, len(names))
		for _, name := range names {
			paths = append(paths, documents.Dir+"/"+name)
		}
		return paths, nil
	})
	request := workerhttp.PutAttemptRequest{
		Assignment:   workerhttp.Assignment{ProjectID: "prj_1", WorkspaceID: "ws_1", Role: workerhttp.RoleReviewer},
		Instructions: interventionInstructions("reviewing", documents.WithAttached("Look at this", []string{"shot.png", "spec.pdf"})),
	}
	starter.withAttachments(t.Context(), &request)
	if !reviewer || !slices.Equal(delivered, []string{"shot.png", "spec.pdf"}) ||
		!slices.Equal(request.Images, []string{documents.Dir + "/shot.png"}) {
		t.Fatalf("delivered %v (reviewer %v), images %v", delivered, reviewer, request.Images)
	}
	plain := workerhttp.PutAttemptRequest{Instructions: "No files here."}
	starter.withAttachments(t.Context(), &plain)
	if plain.Images != nil {
		t.Fatalf("images = %v", plain.Images)
	}
}

// A brief written from a work order's description keeps its files.
func TestAWorkOrdersGoalKeepsItsFiles(t *testing.T) {
	description := documents.WithAttached("Match the mockup", []string{"mockup.png"})
	if got := documents.AttachedIn(withAttachedFrom("Rewritten goal", description)); !slices.Equal(got, []string{"mockup.png"}) {
		t.Fatalf("goal names %v", got)
	}
	if got := withAttachedFrom(description, description); got != description {
		t.Fatalf("a goal that names them already = %q", got)
	}
}
