package conversations

import (
	"errors"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A reply's questions are stored on its message; answering one records the
// answer and sends it as the user's message, which the next turn hears. A
// question is answered once.
func TestAQuestionIsAnsweredOnceAsTheUsersMessage(t *testing.T) {
	service, worker := newChatsService(t)
	ctx := t.Context()
	chat, err := service.CreateChat(ctx, "prj_test", "Look")
	if err != nil {
		t.Fatal(err)
	}
	in := InChat(ctx, chat.Chat)
	sent, err := service.Send(in, "prj_test", "Let's settle the look", "msg-1")
	if err != nil {
		t.Fatal(err)
	}
	worker.mu.Lock()
	var attempt workerhttp.Attempt
	for _, running := range worker.attempts {
		attempt = running
	}
	attempt.State = workerhttp.AttemptStateTerminal
	attempt.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Two things on the look.",
		AssistantTurn: &featureartifact.AssistantTurn{Questions: []featureartifact.AssistantQuestion{
			{Text: "Light or dark?", Options: []string{"Light", "Dark"}, Suggested: 1},
			{Text: "Dense rows?", Options: []string{"Compact", "Roomy", "Compact"}},
			{Text: "Broken", Options: []string{"Only one"}},
		}},
	}
	worker.attempts[attempt.AttemptID] = attempt
	worker.mu.Unlock()
	answered, err := service.Get(in, "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	reply := answered.Messages[len(answered.Messages)-1]
	if reply.Role != "assistant" || len(reply.Questions) != 2 || reply.Questions[0].ID != "q1" ||
		reply.Questions[1].ID != "q2" || strings.Join(reply.Questions[1].Options, "|") != "Compact|Roomy" ||
		reply.Questions[0].Suggested != 1 || reply.Questions[0].Answer != nil {
		t.Fatalf("reply = %+v", reply)
	}

	if _, err := service.AnswerQuestion(in, "prj_test", reply.ID, "q1", ptr(5), ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an answer that is not an option = %v", err)
	}
	if _, err := service.AnswerQuestion(in, "prj_test", reply.ID, "q9", ptr(0), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such question = %v", err)
	}
	puts := len(worker.instructions())
	// Without its chat, the message's chat is found.
	first, err := service.AnswerQuestion(ctx, "prj_test", reply.ID, "q1", ptr(1), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Chat != chat.Chat || first.Status != StatusRunning {
		t.Fatalf("answered in %q, status %s", first.Chat, first.Status)
	}
	last := first.Messages[len(first.Messages)-1]
	question := first.Messages[len(first.Messages)-2].Questions[0]
	if last.Role != "user" || last.Text != "Light or dark? → Dark" || question.Answer == nil ||
		question.Answer.Text != "Dark" || question.Answer.Option == nil || *question.Answer.Option != 1 {
		t.Fatalf("answered = %+v, %+v", last, question)
	}
	instructions := worker.instructions()
	if len(instructions) != puts+1 || !strings.Contains(instructions[puts], "The user says:\nLight or dark? → Dark") {
		t.Fatalf("the turn heard %v", instructions[puts:])
	}
	// Answering again changes nothing.
	again, err := service.AnswerQuestion(in, "prj_test", reply.ID, "q1", nil, "Light after all")
	if err != nil || len(again.Messages) != len(first.Messages) || len(worker.instructions()) != puts+1 ||
		again.Messages[len(again.Messages)-2].Questions[0].Answer.Text != "Dark" {
		t.Fatalf("answered again = %+v, %v", again.Messages, err)
	}
	// The other question, answered in the user's own words while the
	// assistant answers, waits for its next turn.
	own, err := service.AnswerQuestion(in, "prj_test", reply.ID, "q2", nil, "  Whatever fits ten rows ")
	if err != nil || len(own.Queued) != 1 || own.Queued[0].Text != "Dense rows? → Whatever fits ten rows" {
		t.Fatalf("own words = %+v, %v", own.Queued, err)
	}
	if answer := own.Messages[len(own.Messages)-2].Questions[1].Answer; answer == nil || answer.Option != nil ||
		answer.Text != "Whatever fits ten rows" {
		t.Fatalf("own answer = %+v", answer)
	}
	if sent.Messages[0].Questions != nil {
		t.Fatal("the user's message has questions")
	}
}

// Several questions answered together are one message, which one turn hears.
func TestQuestionsAnsweredTogetherAreOneMessage(t *testing.T) {
	service, worker := newChatsService(t)
	ctx := t.Context()
	chat, err := service.CreateChat(ctx, "prj_test", "Look")
	if err != nil {
		t.Fatal(err)
	}
	in := InChat(ctx, chat.Chat)
	if _, err := service.Send(in, "prj_test", "Let's settle the look", "msg-1"); err != nil {
		t.Fatal(err)
	}
	worker.mu.Lock()
	var attempt workerhttp.Attempt
	for _, running := range worker.attempts {
		attempt = running
	}
	attempt.State = workerhttp.AttemptStateTerminal
	attempt.Result = &workerhttp.TerminalResult{
		Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded, Summary: "Two things.",
		AssistantTurn: &featureartifact.AssistantTurn{Questions: []featureartifact.AssistantQuestion{
			{Text: "Light or dark?", Options: []string{"Light", "Dark"}, Suggested: 1},
			{Text: "Dense rows?", Options: []string{"Compact", "Roomy"}},
		}},
	}
	worker.attempts[attempt.AttemptID] = attempt
	worker.mu.Unlock()
	asked, err := service.Get(in, "prj_test")
	if err != nil {
		t.Fatal(err)
	}
	reply := asked.Messages[len(asked.Messages)-1]
	if _, err := service.AnswerQuestions(in, "prj_test", reply.ID, []QuestionReply{{Question: "q1"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty answer = %v", err)
	}
	puts := len(worker.instructions())
	answered, err := service.AnswerQuestions(in, "prj_test", reply.ID, []QuestionReply{
		{Question: "q1", Option: ptr(0)}, {Question: "q2", Text: " Ten rows "},
	})
	if err != nil {
		t.Fatal(err)
	}
	last := answered.Messages[len(answered.Messages)-1]
	questions := answered.Messages[len(answered.Messages)-2].Questions
	if last.Text != "- Light or dark? → Light\n- Dense rows? → Ten rows" || questions[0].Answer == nil || questions[1].Answer == nil ||
		questions[1].Answer.Text != "Ten rows" || len(answered.Queued) != 0 {
		t.Fatalf("answered = %+v %+v", last, questions)
	}
	if instructions := worker.instructions(); len(instructions) != puts+1 ||
		!strings.Contains(instructions[puts], "The user says:\n- Light or dark? → Light\n- Dense rows? → Ten rows") {
		t.Fatalf("the turn heard %v", instructions[puts:])
	}
}

// A fresh session reads the questions asked, and what became of them.
func TestTheConversationRecapsItsQuestions(t *testing.T) {
	answer := 0
	stored := record{Conversation: Conversation{Messages: []Message{{Role: "assistant", Text: "Two things.", Questions: []Question{
		{ID: "q1", AssistantQuestion: featureartifact.AssistantQuestion{Text: "Light or dark?", Options: []string{"Light", "Dark"}, Suggested: 1},
			Answer: &QuestionAnswer{Option: &answer, Text: "Light"}},
		{ID: "q2", AssistantQuestion: featureartifact.AssistantQuestion{Text: "Dense?", Options: []string{"Yes", "No"}}},
	}}}}}
	recent := recentConversation(stored, "user")
	for _, want := range []string{"(asked: Light or dark? [Light / Dark; you suggested Dark] answered “Light”)",
		"(asked: Dense? [Yes / No; you suggested Yes] not answered)"} {
		if !strings.Contains(recent, want) {
			t.Fatalf("the recap lacks %q:\n%s", want, recent)
		}
	}
}

func ptr(value int) *int { return &value }
