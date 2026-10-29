package conversations

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// Question cards (ADR-039): an assistant's reply can ask the user up to
// three questions, each with the answers it suggests and the one it would
// pick. The user answers each once, with an option or in their own words,
// and the answer is sent as their message, so the turn runs as for one they
// typed; the desktop sends a reply's answers together, as one message, so
// one turn hears them all. A question the user passes over by writing
// something else stays unanswered.

// Question is one the assistant asked, by its ID within its message.
type Question struct {
	ID string `json:"id"`
	featureartifact.AssistantQuestion
	// Answer is set once the user answered it.
	Answer *QuestionAnswer `json:"answer,omitempty"`
}

// QuestionAnswer is the user's answer to a question: Option indexes the
// answer they chose, and is nil for one they wrote themselves.
type QuestionAnswer struct {
	Option     *int      `json:"option,omitempty"`
	Text       string    `json:"text"`
	AnsweredAt time.Time `json:"answered_at"`
}

// asked are the questions of a reply, within their limits, with their IDs.
func asked(questions []featureartifact.AssistantQuestion) []Question {
	kept := featureartifact.NormalizeQuestions(questions)
	if len(kept) == 0 {
		return nil
	}
	asked := make([]Question, 0, len(kept))
	for index, question := range kept {
		asked = append(asked, Question{ID: fmt.Sprintf("q%d", index+1), AssistantQuestion: question})
	}
	return asked
}

// recap is a question as the assistant reads it back in the conversation.
func (question Question) recap() string {
	answer := "not answered"
	if question.Answer != nil {
		answer = "answered “" + bounded(question.Answer.Text, 200) + "”"
	}
	suggested := ""
	if question.Suggested >= 0 && question.Suggested < len(question.Options) {
		suggested = "; you suggested " + question.Options[question.Suggested]
	}
	return fmt.Sprintf("(asked: %s [%s%s] %s)", question.Text, strings.Join(question.Options, " / "), suggested, answer)
}

// AnswerQuestion answers a question of an assistant's message with the
// option the user chose, or, without one, with what they wrote, and sends
// the answer as their message in the message's chat. A question is answered
// once: answering it again changes nothing and answers the chat as it is.
func (service *Service) AnswerQuestion(
	ctx context.Context, scope, messageID, questionID string, option *int, text string,
) (Conversation, error) {
	return service.AnswerQuestions(ctx, scope, messageID, []QuestionReply{{Question: questionID, Option: option, Text: text}})
}

// QuestionReply is the user's answer to one question of a message: the
// option they chose, or, without one, what they wrote.
type QuestionReply struct {
	Question string `json:"question"`
	Option   *int   `json:"option,omitempty"`
	Text     string `json:"text,omitempty"`
}

// AnswerQuestions answers several questions of an assistant's message at
// once, as AnswerQuestion does one, and sends the answers as one message of
// the user's, so the assistant hears them all in one turn. Questions already
// answered are passed over; with none left, it answers the chat as it is.
func (service *Service) AnswerQuestions(
	ctx context.Context, scope, messageID string, replies []QuestionReply,
) (Conversation, error) {
	if len(replies) == 0 || len(replies) > featureartifact.MaxAssistantQuestions {
		return Conversation{}, fmt.Errorf("%w: answer one to %d questions", ErrInvalid, featureartifact.MaxAssistantQuestions)
	}
	if err := service.checkScope(ctx, scope); err != nil {
		return Conversation{}, err
	}
	settings, err := service.Readiness(ctx)
	if err != nil {
		return Conversation{}, err
	}
	chat, err := service.chatOfMessage(ctx, scope, messageID)
	if err != nil {
		return Conversation{}, err
	}
	stored, done, err := service.open(scope, chat)
	if err != nil {
		return Conversation{}, err
	}
	defer done()
	messageIndex := slices.IndexFunc(stored.Messages, func(message Message) bool {
		return message.ID == messageID && message.Role == "assistant"
	})
	if messageIndex < 0 {
		return Conversation{}, ErrNotFound
	}
	questions := slices.Clone(stored.Messages[messageIndex].Questions)
	lines, ids := []string{}, []string{}
	for _, reply := range replies {
		questionIndex := slices.IndexFunc(questions, func(question Question) bool { return question.ID == reply.Question })
		if questionIndex < 0 {
			return Conversation{}, ErrNotFound
		}
		question := questions[questionIndex]
		if question.Answer != nil || slices.Contains(ids, question.ID) {
			continue
		}
		answer := QuestionAnswer{AnsweredAt: service.now()}
		switch {
		case reply.Option != nil:
			if *reply.Option < 0 || *reply.Option >= len(question.Options) {
				return Conversation{}, fmt.Errorf("%w: no such answer", ErrInvalid)
			}
			chosen := *reply.Option
			answer.Option, answer.Text = &chosen, question.Options[chosen]
		default:
			answer.Text = strings.TrimSpace(reply.Text)
		}
		if answer.Text == "" {
			return Conversation{}, fmt.Errorf("%w: an answer is one of the options or a short text", ErrInvalid)
		}
		question.Answer = &answer
		questions[questionIndex] = question
		lines = append(lines, answerLine(question.Text, answer.Text))
		ids = append(ids, question.ID)
	}
	if len(ids) == 0 {
		return stored.Conversation, nil
	}
	message := lines[0]
	if len(lines) > 1 {
		// A line each in markdown, as single newlines run together.
		message = "- " + strings.Join(lines, "\n- ")
	}
	if len(message) > workerhttp.MaxInstructionsBytes/4 {
		return Conversation{}, fmt.Errorf("%w: an answer is one of the options or a short text", ErrInvalid)
	}
	stored.Messages = slices.Clone(stored.Messages)
	stored.Messages[messageIndex].Questions = questions
	return service.send(ctx, stored, settings, message, "answer:"+messageID+":"+strings.Join(ids, "+"), nil)
}

// chatOfMessage names the chat that holds a message, preferring ctx's chat.
func (service *Service) chatOfMessage(ctx context.Context, scope, messageID string) (string, error) {
	chat, err := service.chatFor(ctx, scope)
	if err != nil || chat != "" {
		return chat, err
	}
	chats, err := service.chatsOf(scope)
	if err != nil {
		return "", err
	}
	for _, chat := range chats {
		if slices.ContainsFunc(chat.Messages, func(message Message) bool { return message.ID == messageID }) {
			return chat.ID, nil
		}
	}
	return "", ErrNotFound
}

// answerLine is a question and its answer in a line: "Light or dark? →
// Dark", or "Theme: Dark" for a question without its mark.
func answerLine(question, answer string) string {
	question = strings.TrimSpace(question)
	if strings.HasSuffix(question, "?") {
		return question + " → " + answer
	}
	return strings.TrimRight(question, ":.") + ": " + answer
}
