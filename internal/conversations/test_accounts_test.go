package conversations

import (
	"context"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/testaccounts"
)

type testAccountsStub []testaccounts.Summary

func (stub testAccountsStub) List(context.Context, string) ([]testaccounts.Summary, error) {
	return stub, nil
}

// The state names the project's test logins, their roles and notes, and
// never a password; a project without any says so.
func TestTheAssistantKnowsTheTestAccountsButNoPasswords(t *testing.T) {
	describer := StateDescriber{Projects: projectsStub{}, Agents: agentsStub{}, TestAccounts: testAccountsStub{
		{Label: "Admin", Username: "admin@commitarium.test", Role: "admin", Note: "Sees every order"},
		{Label: "Customer", Username: "customer@commitarium.test"},
	}}
	state, err := describer.Describe(t.Context(), "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(state, "- Admin: admin@commitarium.test, role admin — Sees every order") ||
		!strings.Contains(state, "- Customer: customer@commitarium.test\n") ||
		!strings.Contains(state, "COMMITARIUM_TEST_ACCOUNTS") || strings.Contains(strings.ToLower(state), "password:") {
		t.Fatalf("state:\n%s", state)
	}

	describer.TestAccounts = testAccountsStub{}
	state, err = describer.Describe(t.Context(), "prj_test")
	if err != nil || !strings.Contains(state, "Test accounts: none") {
		t.Fatalf("state without accounts:\n%s", state)
	}
}

// Mayka is told how test accounts reach the app and to propose seeding once.
func TestThePromptExplainsTestAccountSeeding(t *testing.T) {
	text := rules("prj_test", Settings{})
	if !strings.Contains(text, "Settings → Test accounts") || !strings.Contains(text, "Passwords never pass through the chat") ||
		!strings.Contains(text, "propose one work order") {
		t.Fatal("the rules do not explain test accounts")
	}
}
