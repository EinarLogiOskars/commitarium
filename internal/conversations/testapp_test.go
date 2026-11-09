package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/outputs"
	"github.com/EinarLogiOskars/commitarium/internal/testaccounts"
	"github.com/EinarLogiOskars/commitarium/internal/testpreviews"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// notStartedWorker has no attempt until one is put, as a real worker.
type notStartedWorker struct{ *workerStub }

func (worker notStartedWorker) GetAttempt(ctx context.Context, reference workerhttp.AttemptReference) (workerhttp.Attempt, error) {
	if len(worker.puts) == 0 {
		return workerhttp.Attempt{}, &workerhttp.RemoteError{StatusCode: 404,
			ProtocolError: workerhttp.ProtocolError{Code: workerhttp.ErrorNotFound, Message: "worker attempt was not found"}}
	}
	return worker.workerStub.GetAttempt(ctx, reference)
}

type accountFileStub struct{ file testaccounts.PreviewFile }

func (stub accountFileStub) Preview(context.Context, string) (testaccounts.PreviewFile, error) {
	return stub.file, nil
}

func newTestAppService(t *testing.T) (*Service, *workerStub, *testpreviews.Store) {
	t.Helper()
	service, worker := newTestService(t)
	service.config.Workers = func(string) (workerhttp.Service, error) { return notStartedWorker{worker}, nil }
	store, err := outputs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	previews, err := testpreviews.NewStore(filepath.Join(t.TempDir(), "test-previews.json"))
	if err != nil {
		t.Fatal(err)
	}
	service.config.Outputs, service.config.TestPreviews = store, previews
	service.config.TestAccounts = accountFileStub{file: testaccounts.PreviewFile{Domain: testaccounts.Domain,
		Accounts: []testaccounts.PreviewAccount{{Label: "Admin", Username: "admin@commitarium.test", Password: "s3cret-pass", Role: "admin"}}}}
	return service, worker, previews
}

// A helper that tests the app waits for its test preview, then starts with
// its address and the test logins beside it; when it is done the test
// preview ends, so the host removes it.
func TestAHelperThatTestsTheAppStartsOnceItsPreviewRuns(t *testing.T) {
	service, worker, previews := newTestAppService(t)
	helper, err := service.StartTask(t.Context(), "prj_test", "act_try", "Try sign-in", "Sign in as the admin and add a meal.", true)
	if err != nil {
		t.Fatal(err)
	}
	if !helper.TestApp || helper.Brief == "" || helper.Model != "codex-default" || len(worker.puts) != 0 {
		t.Fatalf("helper = %+v puts = %d; want it waiting, not started", helper, len(worker.puts))
	}
	preview, err := previews.Get(t.Context(), helper.ID)
	if err != nil || preview.Status != testpreviews.Pending || preview.ProjectID != "prj_test" || preview.Agent != "codex" {
		t.Fatalf("test preview = %+v, %v", preview, err)
	}
	startedHelper(t, service, helper)
	watcher := Watcher{Service: service}
	watcher.followHelpers(t.Context(), "prj_test")
	if len(worker.puts) != 0 {
		t.Fatal("it started before its test preview ran")
	}

	if _, err := previews.Claim(t.Context(), helper.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := previews.Ready(t.Context(), helper.ID, "http://172.20.0.4:3000"); err != nil {
		t.Fatal(err)
	}
	watcher.followHelpers(t.Context(), "prj_test")
	if len(worker.puts) != 1 {
		t.Fatalf("puts = %d, want the helper started", len(worker.puts))
	}
	put := worker.puts[0]
	for _, want := range []string{"http://172.20.0.4:3000", "Sign in as the admin", TestAccountsFile,
		"PLAYWRIGHT_BROWSERS_PATH=" + BrowsersPath, "outputs/test-report.md"} {
		if !strings.Contains(put.Instructions, want) {
			t.Fatalf("instructions lack %q:\n%s", want, put.Instructions)
		}
	}
	if strings.Contains(put.Instructions, "s3cret-pass") || put.WorkspaceAccess != workerhttp.WorkspaceAccessReadWrite {
		t.Fatalf("put = %+v", put)
	}
	directory := filepath.Join(service.config.WorkspaceRoot, helper.ID)
	contents, err := os.ReadFile(filepath.Join(directory, TestAccountsFile))
	var file testaccounts.PreviewFile
	if err != nil || json.Unmarshal(contents, &file) != nil || len(file.Accounts) != 1 || file.Accounts[0].Password != "s3cret-pass" {
		t.Fatalf("test accounts file = %s, %v", contents, err)
	}
	if stored, _ := service.read("prj_test"); len(stored.PendingEvents) != 0 {
		t.Fatalf("a working helper woke the assistant: %v", stored.PendingEvents)
	}

	if err := os.WriteFile(filepath.Join(directory, "outputs", "01-sign-in.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker.finish(workerhttp.TerminalResult{Outcome: workerhttp.OutcomeCompleted, Disposition: workerhttp.DispositionSucceeded,
		Summary: "Sign-in works; adding a meal fails."})
	watcher.followHelpers(t.Context(), "prj_test")
	if preview, _ := previews.Get(t.Context(), helper.ID); preview.Status != testpreviews.Ending {
		t.Fatalf("test preview = %+v, want it ending", preview)
	}
	stored, _ := service.read("prj_test")
	if len(stored.PendingEvents) != 1 || !strings.Contains(stored.PendingEvents[0], "01-sign-in.png") {
		t.Fatalf("pending = %v", stored.PendingEvents)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("the helper's directory, with the logins, is still there: %v", err)
	}
}

// A test preview that does not start fails the helper before it starts,
// with the host's reason, and ends.
func TestAHelperWhosePreviewFailsStopsWithTheReason(t *testing.T) {
	service, worker, previews := newTestAppService(t)
	helper, err := service.StartTask(t.Context(), "prj_test", "act_try", "Try sign-in", "Sign in.", true)
	if err != nil {
		t.Fatal(err)
	}
	startedHelper(t, service, helper)
	if _, err := previews.Fail(t.Context(), helper.ID, "the project's compose file is invalid"); err != nil {
		t.Fatal(err)
	}
	Watcher{Service: service}.followHelpers(t.Context(), "prj_test")
	stored, _ := service.read("prj_test")
	if len(worker.puts) != 0 || len(stored.PendingEvents) != 1 ||
		!strings.Contains(stored.PendingEvents[0], "compose file is invalid") || !stored.Helpers[0].Done {
		t.Fatalf("puts = %d pending = %v", len(worker.puts), stored.PendingEvents)
	}
	if preview, _ := previews.Get(t.Context(), helper.ID); preview.Status != testpreviews.Ending {
		t.Fatalf("test preview = %+v, want it ending", preview)
	}
}

// Only a project's app is tested, and only so many at once.
func TestTestingTheAppNeedsAProjectAndRoom(t *testing.T) {
	service, _, _ := newTestAppService(t)
	if _, err := service.StartTask(t.Context(), TopLevel, "act_1", "Try", "Try it.", true); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("at the top level = %v, want ErrUnavailable", err)
	}
	for index := range testpreviews.MaxActive {
		if _, err := service.StartTask(t.Context(), "prj_test", "act_"+string(rune('a'+index)), "Try", "Try it.", true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.StartTask(t.Context(), "prj_test", "act_z", "Try", "Try it.", true); !errors.Is(err, ErrUnavailable) ||
		!strings.Contains(err.Error(), "other helpers are testing") {
		t.Fatalf("one too many = %v", err)
	}
}
