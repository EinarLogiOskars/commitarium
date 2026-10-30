package conversations

import (
	"context"
	"slices"
	"strings"
	"time"
)

const (
	// minSearch is the shortest query, in characters, and maxSearchResults
	// the most chats a search answers.
	minSearch        = 2
	maxSearchResults = 30
	// searchContext is how many characters a snippet shows before a match.
	searchContext = 40
	snippetLength = 140
)

// ChatMatch is a chat that holds a search's words: in its latest message
// that does, or in its title when no message does.
type ChatMatch struct {
	Scope   string `json:"scope"`
	Chat    string `json:"chat"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	// MessageID is empty for a match in the title alone.
	MessageID string    `json:"message_id,omitempty"`
	At        time.Time `json:"at"`
}

// SearchChats looks through what the user and the assistant wrote in every
// open chat, for messages that hold all of the query's words. The chats are
// few and short enough to read each time, and a search is rare.
func (service *Service) SearchChats(ctx context.Context, query string) ([]ChatMatch, error) {
	words := strings.Fields(strings.ToLower(query))
	if len([]rune(strings.Join(words, " "))) < minSearch {
		return []ChatMatch{}, nil
	}
	matches := []ChatMatch{}
	for _, scope := range append(service.projectScopes(), TopLevel) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// One unreadable scope doesn't cost the search the others.
		chats, err := service.chatsOf(scope)
		if err != nil {
			continue
		}
		for _, chat := range chats {
			if chat.Archived {
				continue
			}
			if match, ok := matchChat(chat, words); ok {
				matches = append(matches, match)
			}
		}
	}
	slices.SortStableFunc(matches, func(a, b ChatMatch) int { return b.At.Compare(a.At) })
	return matches[:min(len(matches), maxSearchResults)], nil
}

func matchChat(chat chatState, words []string) (ChatMatch, bool) {
	match := ChatMatch{Scope: chat.Scope, Chat: chat.ID, Title: chat.Title, At: chat.UsedAt}
	for _, message := range slices.Backward(chat.Messages) {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		if holdsAll(message.Text, words) {
			match.Snippet = snippet(message.Text, words[0])
			match.MessageID = message.ID
			match.At = message.OccurredAt
			return match, true
		}
	}
	return match, holdsAll(chat.Title, words)
}

func holdsAll(text string, words []string) bool {
	text = strings.ToLower(text)
	for _, word := range words {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

// snippet is the stretch of text around the word's first appearance, on one
// line. Lower-casing can change a text's length, so it finds the word by
// characters, not bytes.
func snippet(text, word string) string {
	characters := []rune(strings.Join(strings.Fields(text), " "))
	needle := []rune(word)
	at := 0
	for i := 0; i+len(needle) <= len(characters); i++ {
		if strings.EqualFold(string(characters[i:i+len(needle)]), word) {
			at = i
			break
		}
	}
	start := max(at-searchContext, 0)
	end := min(start+snippetLength, len(characters))
	shown := string(characters[start:end])
	if start > 0 {
		shown = "…" + shown
	}
	if end < len(characters) {
		shown += "…"
	}
	return shown
}
