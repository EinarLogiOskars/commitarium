package orchestration

import (
	"context"
	"log"
	"slices"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/worker"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// AttachmentDelivery copies a project's named documents into a role's
// checkout of a workspace and answers their paths there.
type AttachmentDelivery func(ctx context.Context, projectID string, role worker.Role, workspaceID string, names []string) ([]string, error)

// DeliverAttachments has turns whose instructions name attached files (the
// user's screenshots in an intervention, a session message, or a work
// order's goal) find them in their checkout and look at the images.
func (starter *RemoteLeadStarter) DeliverAttachments(deliver AttachmentDelivery) {
	starter.attachments = deliver
}

// withAttachments copies the files the turn's instructions name into its
// checkout and has it look at the images among them. The images follow from
// the instructions alone, so a rebuilt turn asks for the same ones. A copy
// that fails only costs the images: the instructions still name the files.
func (starter *RemoteLeadStarter) withAttachments(ctx context.Context, request *workerhttp.PutAttemptRequest) {
	names := documents.AttachedIn(request.Instructions)
	if starter.attachments == nil || len(names) == 0 {
		return
	}
	assignment := request.Assignment
	paths, err := starter.attachments(ctx, assignment.ProjectID, worker.Role(assignment.Role), assignment.WorkspaceID, names)
	if err != nil {
		log.Printf("copy the attached files into workspace %q: %v", assignment.WorkspaceID, err)
		return
	}
	request.Images = nil
	for _, path := range paths {
		if documents.IsImage(path) && len(request.Images) < workerhttp.MaxImages {
			request.Images = append(request.Images, path)
		}
	}
}

// withAttachedFrom keeps the files a work order's description names in its
// goal, when the brief written from the description left them out.
func withAttachedFrom(goal, description string) string {
	var missing []string
	present := documents.AttachedIn(goal)
	for _, name := range documents.AttachedIn(description) {
		if !slices.Contains(present, name) {
			missing = append(missing, name)
		}
	}
	return documents.WithAttached(goal, missing)
}
