package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
)

type marketStub struct{ app marketplace.App }

func (stub marketStub) App(context.Context, string, string) (marketplace.App, error) { return stub.app, nil }

func (marketStub) Credentials(context.Context) (marketplace.Credentials, error) {
	return marketplace.Credentials{Username: "sigga", Token: "t"}, nil
}

func run(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.test")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestDownloadBundlesTheReleaseWithItsHistoryAndRecordsInstalls(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	run(t, root, "init", "--initial-branch=main", work)
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(work, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		run(t, work, "add", ".")
		run(t, work, "commit", "-m", name)
	}
	run(t, work, "tag", "-a", "v1", "-m", "Release v1")
	remote := filepath.Join(root, "remote.git")
	run(t, root, "clone", "--bare", work, remote)

	app := marketplace.App{Owner: "sigga", Name: "sleep-log", Title: "Sleep Log", Version: "v1", CloneURL: "file://" + remote}
	service, err := NewService(marketStub{app}, "git", filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	download, cleanup, err := service.Download(t.Context(), "sigga", "sleep-log")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer cleanup()
	heads := run(t, root, "bundle", "list-heads", download.Bundle)
	if !strings.Contains(heads, "refs/heads/main") || !strings.Contains(heads, "refs/tags/v1") {
		t.Fatalf("bundle heads:\n%s", heads)
	}
	verified := filepath.Join(root, "verify")
	run(t, root, "clone", "--quiet", download.Bundle, verified)
	if count := run(t, verified, "rev-list", "--count", "HEAD"); count != "2" {
		t.Fatalf("history has %s commits", count)
	}

	if err := service.Record("prj_installed", app); err != nil {
		t.Fatal(err)
	}
	installed, err := service.Installed()
	if err != nil || len(installed) != 1 || installed[0].Owner != "sigga" || installed[0].Version != "v1" {
		t.Fatalf("installed=%+v err=%v", installed, err)
	}
}
