package conversations

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// MaxAttachments bounds the files one message carries.
const MaxAttachments = 10

// Attachments holds the files attached to messages: the scope's documents
// (a project's, or the top level's), copied into a turn's working directory
// for it to read.
type Attachments interface {
	Read(ctx context.Context, owner, name string) ([]byte, error)
	Deliver(ctx context.Context, owner string, names []string, directory string) ([]string, error)
}

// checkAttachments answers the message's attachments once each is one of
// the scope's documents.
func (service *Service) checkAttachments(ctx context.Context, scope string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if service.config.Attachments == nil || len(names) > MaxAttachments {
		return nil, fmt.Errorf("%w: a message carries at most %d files", ErrInvalid, MaxAttachments)
	}
	checked := make([]string, 0, len(names))
	for _, name := range names {
		if slices.Contains(checked, name) {
			continue
		}
		if _, err := service.config.Attachments.Read(ctx, scope, name); err != nil {
			return nil, fmt.Errorf("%w: the attached file %q is not there any more; attach it again", ErrInvalid, name)
		}
		checked = append(checked, name)
	}
	return checked, nil
}

// withAttachments is a user's message as the assistant reads it: the text,
// then where its files are.
func withAttachments(text string, names []string) string {
	return documents.WithAttached(text, names)
}

// turnAttachments are the files the turn on promptRole carries: those of
// the trailing user messages its prompt holds, and those of a turn kept for
// its agent's login, which it carries along (sign_in.go).
func turnAttachments(stored record, promptRole string) []string {
	var names []string
	if stored.SignIn != nil && stored.LastTurn != nil {
		names = append(names, stored.LastTurn.Attachments...)
	}
	if promptRole == "user" {
		start := len(stored.Messages)
		for start > 0 && stored.Messages[start-1].Role == "user" {
			start--
		}
		for _, message := range stored.Messages[start:] {
			names = append(names, message.Attachments...)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// deliverAttachments copies the turn's files into its working directory and
// answers the images among them, for the turn to look at.
func (service *Service) deliverAttachments(ctx context.Context, scope, workspaceID string, names []string) ([]string, error) {
	if len(names) == 0 || service.config.Attachments == nil {
		return nil, nil
	}
	paths, err := service.config.Attachments.Deliver(ctx, scope, names,
		filepath.Join(service.config.WorkspaceRoot, workspaceID))
	if err != nil {
		return nil, fmt.Errorf("%w: copy the attached files: %v", ErrUnavailable, err)
	}
	var images []string
	for _, path := range paths {
		if documents.IsImage(path) && len(images) < workerhttp.MaxImages {
			images = append(images, path)
		}
	}
	return images, nil
}
