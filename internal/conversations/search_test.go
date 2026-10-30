package conversations

import (
	"strings"
	"testing"
	"time"
)

func TestMatchChatFindsTheLatestMessageWithAllWords(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	chat := chatState{ID: "main", Scope: "prj_a", Title: "Export", Messages: []Message{
		{ID: "m1", Role: "user", Text: "Add CSV export please", OccurredAt: at},
		{ID: "m2", Role: "event", Text: "CSV export merged", OccurredAt: at.Add(time.Hour)},
		{ID: "m3", Role: "assistant", Text: "Only csv here", OccurredAt: at.Add(2 * time.Hour)},
	}}

	match, ok := matchChat(chat, []string{"csv", "export"})
	if !ok || match.MessageID != "m1" || !match.At.Equal(at) {
		t.Fatalf("match = %+v, %v", match, ok)
	}
	if match, ok := matchChat(chat, []string{"export"}); !ok || match.MessageID != "m1" {
		t.Fatalf("events are not searched: %+v, %v", match, ok)
	}
	if match, ok := matchChat(chat, []string{"expo"}); !ok || match.Title != "Export" {
		t.Fatalf("title = %+v, %v", match, ok)
	}
	if _, ok := matchChat(chat, []string{"missing"}); ok {
		t.Fatal("matched words that are not there")
	}
}

func TestMatchChatFallsBackToTheTitle(t *testing.T) {
	chat := chatState{ID: "main", Scope: "prj_a", Title: "Sorting ideas", UsedAt: time.Unix(5, 0)}
	match, ok := matchChat(chat, []string{"sorting"})
	if !ok || match.MessageID != "" || match.Snippet != "" {
		t.Fatalf("match = %+v, %v", match, ok)
	}
}

func TestSnippetShowsTheWordOnOneLine(t *testing.T) {
	text := strings.Repeat("before ", 30) + "\n\nThe Needle is here " + strings.Repeat("after ", 30)
	got := snippet(text, "needle")
	if !strings.Contains(got, "Needle is here") || !strings.HasPrefix(got, "…") ||
		!strings.HasSuffix(got, "…") || strings.Contains(got, "\n") {
		t.Fatalf("snippet = %q", got)
	}
	if got := snippet("Þú ert með ÞETTA hér", "þetta"); !strings.Contains(got, "ÞETTA") {
		t.Fatalf("snippet = %q", got)
	}
}
