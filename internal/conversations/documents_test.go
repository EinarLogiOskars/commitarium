package conversations

import (
	"context"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/documents"
)

type documentsStub []documents.Document

func (stub documentsStub) List(context.Context, string) ([]documents.Document, error) {
	return stub, nil
}

// The state lists the documents the user attached and where they are.
func TestTheAssistantKnowsTheProjectsDocuments(t *testing.T) {
	describer := StateDescriber{Projects: projectsStub{}, Agents: agentsStub{},
		Documents: documentsStub{{Name: "Shopify API.md", Size: 2048}}}
	state, err := describer.Describe(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, "in .commitarium/documents/ of your working directory") ||
		!strings.Contains(state, "- Shopify API.md (2 KB)") {
		t.Fatalf("state:\n%s", state)
	}
}
