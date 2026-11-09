package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/EinarLogiOskars/commitarium/internal/testaccounts"
	"github.com/EinarLogiOskars/commitarium/internal/testpreviews"
	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

// A helper that tests the app (HELPERS_PLAN.md, H6): the assistant's
// delegate asks for it with test_app. Before its attempt starts, it asks for
// a test preview, a throwaway preview of the project's default branch that
// the host starts with the test accounts and lets the helper's worker reach.
// Once that runs, the helper starts with the preview's address in its brief,
// the test logins in its directory, and a headless browser in its worker.
// When it is done, however it ends, the test preview ends, and the host
// removes it and the worker's reach.

// TestPreviews keeps the throwaway previews helpers test the app in.
type TestPreviews interface {
	Request(ctx context.Context, id, projectID, agent string) (testpreviews.Preview, error)
	Get(ctx context.Context, id string) (testpreviews.Preview, error)
	End(ctx context.Context, id string) error
}

// TestAccountFiles answers the file of a project's test logins.
type TestAccountFiles interface {
	Preview(ctx context.Context, projectID string) (testaccounts.PreviewFile, error)
}

// TestAccountsFile is where a helper that tests the app finds the logins,
// in its working directory.
const TestAccountsFile = "test-accounts.json"

// BrowsersPath and NodeModules are where the worker images keep Playwright
// and its headless Chromium (docker/*-worker.Dockerfile), apart from any
// project's own.
const (
	BrowsersPath = "/opt/commitarium-browser"
	NodeModules  = "/usr/local/lib/node_modules"
)

// requestTestPreview asks for the helper's test preview; the helper starts
// once it runs (awaitTestPreview).
func (service *Service) requestTestPreview(ctx context.Context, scope string, helper Helper, model, brief string) (Helper, error) {
	if scope == TopLevel || !scopePattern.MatchString(scope) {
		return Helper{}, fmt.Errorf("%w: only a project's app can be tested", ErrUnavailable)
	}
	if service.config.TestPreviews == nil {
		return Helper{}, fmt.Errorf("%w: testing the app needs real agents and the host", ErrUnavailable)
	}
	if _, err := service.config.TestPreviews.Request(ctx, helper.ID, projectIDFor(scope), helper.Agent); err != nil {
		if errors.Is(err, testpreviews.ErrTooMany) {
			return Helper{}, fmt.Errorf("%w: other helpers are testing apps; start this one when they are done", ErrUnavailable)
		}
		return Helper{}, fmt.Errorf("%w: ask for a test copy of the app: %v", ErrUnavailable, err)
	}
	helper.TestApp, helper.Model, helper.Brief = true, model, brief
	return helper, nil
}

// awaitTestPreview follows a helper whose attempt has not started: it
// starts once its test preview runs, and fails if the preview does not.
func (service *Service) awaitTestPreview(ctx context.Context, scope string, helper Helper) (HelperState, error) {
	state := HelperState{Turn: 1, Agent: helper.Agent, Status: HelperWorking}
	if service.config.TestPreviews == nil {
		return HelperState{}, fmt.Errorf("helper %s waits for a test preview nobody keeps", helper.ID)
	}
	preview, err := service.config.TestPreviews.Get(ctx, helper.ID)
	switch {
	case errors.Is(err, testpreviews.ErrNotFound):
		return service.testPreviewFailed(ctx, helper, "the test copy of the app is gone"), nil
	case err != nil:
		return HelperState{}, err
	case preview.Status == testpreviews.Failed || preview.Status == testpreviews.Ending:
		return service.testPreviewFailed(ctx, helper, preview.Reason), nil
	case preview.Status != testpreviews.Ready:
		return state, nil
	}
	directory := filepath.Join(service.config.WorkspaceRoot, helper.ID)
	accounts := service.writeTestAccounts(ctx, scope, directory)
	instructions := taskInstructions(helper.Brief, service.deliverDocuments(ctx, scope, directory),
		testingInstructions(preview.Address, accounts))
	if err := service.startTaskAttempt(ctx, scope, helper, helper.Model, instructions); err != nil {
		return HelperState{}, err
	}
	return state, nil
}

// testPreviewFailed ends a helper whose test preview did not run, before it
// started.
func (service *Service) testPreviewFailed(ctx context.Context, helper Helper, reason string) HelperState {
	service.endTestPreview(ctx, helper)
	_ = os.RemoveAll(filepath.Join(service.config.WorkspaceRoot, helper.ID))
	text := "The test copy of the app did not start, so nothing was tested."
	if reason = strings.TrimSpace(reason); reason != "" {
		text += " Why, as the host reported it: " + reason
	}
	return HelperState{Turn: 1, Agent: helper.Agent, Status: HelperFailed, Text: text}
}

// endTestPreview says the helper's test preview is no longer wanted; the
// host removes it. If that cannot be said now, the preview ends anyway
// once it outlives testpreviews.MaxAge.
func (service *Service) endTestPreview(ctx context.Context, helper Helper) {
	if service.config.TestPreviews == nil {
		return
	}
	if err := service.config.TestPreviews.End(ctx, helper.ID); err != nil {
		log.Printf("end the test preview of helper %s: %v", helper.ID, err)
	}
}

// writeTestAccounts puts the project's test logins in the helper's
// directory and answers how many there are. Without any, the file lists
// none, so the helper knows.
func (service *Service) writeTestAccounts(ctx context.Context, scope, directory string) int {
	file := testaccounts.PreviewFile{Accounts: []testaccounts.PreviewAccount{}, Domain: testaccounts.Domain}
	if service.config.TestAccounts != nil {
		if read, err := service.config.TestAccounts.Preview(ctx, projectIDFor(scope)); err == nil {
			file = read
		} else if !errors.Is(err, testaccounts.ErrNotFound) {
			log.Printf("hand %s's test accounts to a helper: %v", scope, err)
		}
	}
	if file.Accounts == nil {
		file.Accounts = []testaccounts.PreviewAccount{}
	}
	contents, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(directory, TestAccountsFile), contents, 0o600)
	}
	if err != nil {
		log.Printf("write a helper's test accounts: %v", err)
		return 0
	}
	return len(file.Accounts)
}

// attemptMissing reports whether the worker has no such attempt.
func attemptMissing(err error) bool {
	var remote *workerhttp.RemoteError
	return errors.As(err, &remote) && remote.ProtocolError.Code == workerhttp.ErrorNotFound
}

// testingInstructions tell a helper where the test copy of the app is, how
// to sign in and drive it, and how to report.
func testingInstructions(address string, accounts int) string {
	logins := "The project's test logins are in " + TestAccountsFile + " in your working directory, each with a label, " +
		"username, password and role. Sign in with them. Never copy a password into " + "outputs/ or your summary."
	if accounts == 0 {
		logins = "The project has no test logins yet (" + TestAccountsFile + " lists none): test what works without " +
			"signing in, and say that test accounts would let you test more."
	}
	return `

Test the app. A throwaway test copy of the project's app, built from its main branch with test data, runs for you at ` + address + `. Only you can reach it, and only while you work; it is removed when you finish, so nothing you do there reaches the real app or the user's data. ` + logins + `

How to drive it: Playwright and a headless Chromium are installed for you. Write a short Node script in your working directory that uses require('playwright') and chromium.launch() (headless), and run it with PLAYWRIGHT_BROWSERS_PATH=` + BrowsersPath + ` NODE_PATH=` + NodeModules + ` node <script>. Open ` + address + `, go through what the brief asks as a user would, and save a screenshot of each step that matters with page.screenshot({ path: 'outputs/01-sign-in.png', fullPage: true }), numbered in order. Do not install other browsers or tools. If the app does not answer, try again for a minute, then report that.

Report in outputs/test-report.md: what you tested, what worked, and what did not, each problem with the steps to see it again, what you expected, what happened, and the screenshot that shows it by file name.`
}
