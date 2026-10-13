package release

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/feature"
	"github.com/EinarLogiOskars/commitarium/internal/featureartifact"
	"github.com/EinarLogiOskars/commitarium/internal/marketplace"
	"github.com/EinarLogiOskars/commitarium/internal/project"
	"github.com/EinarLogiOskars/commitarium/internal/toolchain"
	"github.com/EinarLogiOskars/commitarium/internal/vision"
)

type projectsStub struct{ stored project.Project }

func (stub projectsStub) GetByID(context.Context, string) (project.Project, error) {
	return stub.stored, nil
}

type toolchainsStub struct{}

func (toolchainsStub) Get(context.Context, string) (toolchain.Manifest, error) {
	return toolchain.Manifest{Status: toolchain.StatusConfigured, Source: toolchain.SourcePicker,
		Tools: map[string]string{"node": "24.21.0"}, Run: &toolchain.Run{Open: toolchain.RunOpen{Service: "frontend", Port: 5173}}}, nil
}

type visionsStub struct{}

func (visionsStub) Get(context.Context, string) (vision.Vision, error) {
	return vision.Vision{Plan: featureartifact.VisionPlan{Statement: "Log sleep.", Confidence: "high",
		Priorities: []featureartifact.VisionPriority{{Level: 0, Items: []featureartifact.VisionItem{
			{ID: "log", Title: "Log a night", FeatureID: "feat_done"},
		}}}}}, nil
}

type featuresStub struct{}

func (featuresStub) List(context.Context, string) ([]feature.Feature, error) {
	return []feature.Feature{{ID: "feat_done", State: feature.StateCompleted}}, nil
}

type marketplaceStub struct{ root string }

func (stub marketplaceStub) Credentials(context.Context) (marketplace.Credentials, error) {
	return marketplace.Credentials{URL: "file://" + stub.root, Username: "sigga", Token: "secret"}, nil
}

func (stub marketplaceStub) EnsureAppRepository(_ context.Context, name, _ string) (marketplace.AppRepository, error) {
	path := filepath.Join(stub.root, "sigga", name+".git")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if output, err := exec.Command("git", "init", "--bare", "--initial-branch=main", path).CombinedOutput(); err != nil {
			return marketplace.AppRepository{}, errors.New(string(output))
		}
	}
	return marketplace.AppRepository{Owner: "sigga", Name: name, CloneURL: "file://" + path, WebURL: "https://market/sigga/" + name}, nil
}

func git(t *testing.T, directory string, arguments ...string) string {
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

type fixture struct {
	service *Service
	work    string
	remote  string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	bare := filepath.Join(local, "admin", "sleep-log.git")
	work := filepath.Join(root, "work")
	git(t, root, "init", "--bare", "--initial-branch=main", bare)
	git(t, root, "init", "--initial-branch=main", work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("sleep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "Initial")
	git(t, work, "push", bare, "main")
	token := filepath.Join(root, "token")
	if err := os.WriteFile(token, []byte("local-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(root, "market")
	stored := project.Project{ID: "prj_test", Name: "Sleep Log", App: true,
		ForgejoRepository: &project.ForgejoRepository{Owner: "admin", Name: "sleep-log", DefaultBranch: "main"}}
	heads := func(_ context.Context, owner, name, branch string) (string, error) {
		return git(t, root, "--git-dir", filepath.Join(local, owner, name+".git"), "rev-parse", branch), nil
	}
	service, err := NewService(Config{LocalBaseURL: "file://" + local, LocalOwner: "admin", LocalTokenFile: token,
		StateRoot: filepath.Join(root, "state")}, projectsStub{stored}, toolchainsStub{}, visionsStub{}, featuresStub{},
		marketplaceStub{root: remote}, heads)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{service: service, work: work, remote: filepath.Join(remote, "sigga", "sleep-log.git")}
}

func (f fixture) commit(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.work, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, f.work, "add", ".")
	git(t, f.work, "commit", "-m", name)
	git(t, f.work, "push", filepath.Join(filepath.Dir(f.work), "local", "admin", "sleep-log.git"), "main")
}

func TestReleasesBuildOnEachOtherWithoutTouchingTheApp(t *testing.T) {
	f := newFixture(t)
	first, err := f.service.Release(t.Context(), "prj_test")
	if err != nil || first.Version != "v1" || first.RepositoryURL != "https://market/sigga/sleep-log" {
		t.Fatalf("first release=%+v err=%v", first, err)
	}
	if got := git(t, f.remote, "rev-parse", "main"); got != first.Commit {
		t.Fatalf("marketplace main %s, release %s", got, first.Commit)
	}
	vision := git(t, f.remote, "show", "v1:.commitarium/vision.json")
	if !strings.Contains(vision, `"done": true`) || strings.Contains(vision, "feat_done") {
		t.Fatalf("released vision:\n%s", vision)
	}
	if app := git(t, f.remote, "show", "v1:.commitarium/app.json"); !strings.Contains(app, `"name": "Sleep Log"`) {
		t.Fatalf("released app manifest:\n%s", app)
	}
	if stack := git(t, f.remote, "show", "v1:.commitarium/stack.json"); !strings.Contains(stack, `"port": 5173`) {
		t.Fatalf("released stack:\n%s", stack)
	}
	if strings.Contains(git(t, f.work, "ls-tree", "-r", "--name-only", "HEAD"), ".commitarium") {
		t.Fatal("the app's own branch got the release files")
	}
	if status, err := f.service.Status(t.Context(), "prj_test"); err != nil || status.Unreleased || status.Latest.Version != "v1" {
		t.Fatalf("status after release=%+v err=%v", status, err)
	}
	if _, err := f.service.Release(t.Context(), "prj_test"); !errors.Is(err, ErrNothingToRelease) {
		t.Fatalf("unchanged release error=%v", err)
	}

	f.commit(t, "naps.txt")
	if status, _ := f.service.Status(t.Context(), "prj_test"); !status.Unreleased {
		t.Fatalf("status after a change=%+v", status)
	}
	second, err := f.service.Release(t.Context(), "prj_test")
	if err != nil || second.Version != "v2" {
		t.Fatalf("second release=%+v err=%v", second, err)
	}
	parents := strings.Fields(git(t, f.remote, "rev-list", "--parents", "-n", "1", "main"))
	if len(parents) != 3 || parents[1] != first.Commit || parents[2] != second.SourceCommit {
		t.Fatalf("second release parents %v", parents)
	}
}

func TestAReleaseContinuesAHandPushedCopyAndItsVersions(t *testing.T) {
	f := newFixture(t)
	git(t, filepath.Dir(f.work), "init", "--bare", "--initial-branch=main", f.remote)
	git(t, f.work, "push", f.remote, "main")
	git(t, f.work, "tag", "-a", "v1", "-m", "by hand")
	git(t, f.work, "push", f.remote, "v1")
	f.commit(t, "naps.txt")

	released, err := f.service.Release(t.Context(), "prj_test")
	if err != nil || released.Version != "v2" {
		t.Fatalf("release=%+v err=%v", released, err)
	}
	if parents := strings.Fields(git(t, f.remote, "rev-list", "--parents", "-n", "1", "main")); len(parents) != 2 {
		t.Fatalf("a copy already in the app's history needs no extra parent: %v", parents)
	}
}

func TestAReleaseNeverOverwritesSomeoneElsesWork(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(filepath.Dir(f.work), "other")
	git(t, filepath.Dir(f.work), "init", "--initial-branch=main", other)
	if err := os.WriteFile(filepath.Join(other, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, other, "add", ".")
	git(t, other, "commit", "-m", "Unrelated")
	git(t, filepath.Dir(f.work), "init", "--bare", "--initial-branch=main", f.remote)
	git(t, other, "push", f.remote, "main")

	if _, err := f.service.Release(t.Context(), "prj_test"); !errors.Is(err, ErrDiverged) {
		t.Fatalf("release onto unrelated work error=%v", err)
	}
}

func TestOnlyAppsWithAStackAreReleased(t *testing.T) {
	f := newFixture(t)
	f.service.projects = projectsStub{project.Project{ID: "prj_test", Name: "Tool"}}
	if _, err := f.service.Release(t.Context(), "prj_test"); !errors.Is(err, ErrNotAnApp) {
		t.Fatalf("non-app error=%v", err)
	}
	if repositoryName("Sleep Log!  2") != "sleep-log-2" || repositoryName("???") != "app" {
		t.Fatal("repository names")
	}
}

func TestAReleaseRefusesSecretsAnywhereInHistory(t *testing.T) {
	f := newFixture(t)
	f.commit(t, ".env")
	git(t, f.work, "rm", "-q", ".env")
	git(t, f.work, "commit", "-q", "-m", "Remove env")
	git(t, f.work, "push", filepath.Join(filepath.Dir(f.work), "local", "admin", "sleep-log.git"), "main")

	_, err := f.service.Release(t.Context(), "prj_test")
	var secret *SecretError
	if !errors.As(err, &secret) || len(secret.Findings) != 1 || secret.Findings[0] != ".env" {
		t.Fatalf("release error=%v", err)
	}
	if _, statErr := os.Stat(f.remote); !errors.Is(statErr, os.ErrNotExist) {
		if refs := git(t, f.remote, "for-each-ref"); refs != "" {
			t.Fatalf("something was pushed: %s", refs)
		}
	}
}

func TestSecretScanFindsTokensInFiles(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.work, "config.js"),
		[]byte("const key = \"sk-abcdefghijklmnopqrstuvwxyz123456\";\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, f.work, "add", ".")
	git(t, f.work, "commit", "-q", "-m", "Key")
	git(t, f.work, "push", filepath.Join(filepath.Dir(f.work), "local", "admin", "sleep-log.git"), "main")

	_, err := f.service.Release(t.Context(), "prj_test")
	var secret *SecretError
	if !errors.As(err, &secret) || secret.Findings[0] != "config.js" {
		t.Fatalf("release error=%v", err)
	}
}
