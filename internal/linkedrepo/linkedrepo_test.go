package linkedrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

type fakeProjects struct{}

func (fakeProjects) GetByID(context.Context, string) (project.Project, error) {
	return project.Project{ForgejoRepository: &project.ForgejoRepository{
		Owner: "commitarium", Name: "todo", DefaultBranch: "main",
	}}, nil
}

// fakeLanding fast-forwards like Forgejo's merge, in the bare repository.
type fakeLanding struct{ forgejo string }

func (landing fakeLanding) FastForwardBranch(_ context.Context, _, _, base, head, commit, _ string) error {
	if out, err := exec.Command("git", "-C", landing.forgejo, "merge-base", "--is-ancestor",
		"refs/heads/"+base, commit).CombinedOutput(); err != nil {
		return errors.New("not a fast-forward: " + string(out))
	}
	if out, err := exec.Command("git", "-C", landing.forgejo, "update-ref", "refs/heads/"+base, commit).CombinedOutput(); err != nil {
		return errors.New(string(out))
	}
	return exec.Command("git", "-C", landing.forgejo, "update-ref", "-d", "refs/heads/"+head).Run()
}

type world struct {
	t       *testing.T
	service *Service
	forgejo string
	remote  string
	work    string
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_NOSYSTEM=1")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newWorld is a project's Forgejo repository and a linked one, both with
// the same first commit.
func newWorld(t *testing.T) world {
	t.Helper()
	root := t.TempDir()
	forgejoRoot := filepath.Join(root, "forgejo")
	forgejo := filepath.Join(forgejoRoot, "commitarium", "todo.git")
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	for _, bare := range []string{forgejo, remote} {
		if err := os.MkdirAll(bare, 0o755); err != nil {
			t.Fatal(err)
		}
		git(t, bare, "init", "--bare", "--quiet", "--initial-branch", "main")
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, work, "init", "--quiet", "--initial-branch", "main")
	git(t, work, "commit", "--quiet", "--allow-empty", "-m", "first")
	git(t, work, "push", "--quiet", forgejo, "main")
	git(t, work, "push", "--quiet", remote, "main")
	credentials := filepath.Join(root, "credentials")
	if err := os.MkdirAll(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(root, "forgejo-token")
	if err := os.WriteFile(token, []byte("forgejo-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Config{
		CredentialsRoot: credentials, ForgejoURL: forgejoRoot, ForgejoTokenFile: token,
		CacheRoot: filepath.Join(root, "cache"), Projects: fakeProjects{}, Landing: fakeLanding{forgejo},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A local path stands in for an https address, which Link insists on.
	if err := service.update(func(links map[string]Link) {
		links["p1"] = Link{URL: remote, Branch: "main", Token: "ghp_token"}
	}); err != nil {
		t.Fatal(err)
	}
	return world{t: t, service: service, forgejo: forgejo, remote: remote, work: work}
}

// commit adds a commit on top of where bare's main is, and pushes it there.
func (w world) commit(bare, message string) {
	w.t.Helper()
	git(w.t, w.work, "fetch", "--quiet", bare, "+main:refs/remotes/x/main")
	git(w.t, w.work, "checkout", "--quiet", "-B", "main", "refs/remotes/x/main")
	git(w.t, w.work, "commit", "--quiet", "--allow-empty", "-m", message)
	git(w.t, w.work, "push", "--quiet", bare, "main")
}

func (w world) main(bare string) string {
	return git(w.t, bare, "rev-parse", "refs/heads/main")
}

func TestPullFastForwardsMainWhenItHasNothingOfItsOwn(t *testing.T) {
	w := newWorld(t)
	view, err := w.service.Get("p1")
	if err != nil || !view.HasToken || view.LocalBranch != "linked/main" {
		t.Fatalf("view = %+v, %v", view, err)
	}
	if pulled, err := w.service.Pull(t.Context(), "p1"); err != nil || pulled.Outcome != UpToDate {
		t.Fatalf("pull with nothing new = %+v, %v", pulled, err)
	}
	w.commit(w.remote, "made on GitHub")
	pulled, err := w.service.Pull(t.Context(), "p1")
	if err != nil || pulled.Outcome != PulledIn || pulled.Commits != 1 {
		t.Fatalf("pull = %+v, %v", pulled, err)
	}
	if w.main(w.forgejo) != w.main(w.remote) {
		t.Fatal("main did not move to the linked repository's commit")
	}
	if link, _ := w.service.link("p1"); link.PulledAt.IsZero() {
		t.Fatal("the pull was not noted")
	}
}

func TestWhenMainHasItsOwnCommitsAPullWaitsOnALinkedBranch(t *testing.T) {
	w := newWorld(t)
	w.commit(w.remote, "made on GitHub")
	w.commit(w.forgejo, "merged work order")
	ours := w.main(w.forgejo)
	pulled, err := w.service.Pull(t.Context(), "p1")
	if err != nil || pulled.Outcome != NeedsMerge || pulled.Commits != 1 {
		t.Fatalf("pull = %+v, %v", pulled, err)
	}
	if w.main(w.forgejo) != ours {
		t.Fatal("main moved although it had commits of its own")
	}
	if git(t, w.forgejo, "rev-parse", "refs/heads/linked/main") != w.main(w.remote) {
		t.Fatal("their commits are not on linked/main")
	}
}

func TestPushGoesToTheBranchTheUserNamesAndNeverToMain(t *testing.T) {
	w := newWorld(t)
	if view, _ := w.service.Get("p1"); view.PushBranch != DefaultPushBranch {
		t.Fatalf("offered branch = %q", view.PushBranch)
	}
	if pushed, err := w.service.Push(t.Context(), "p1", "feature"); err != nil || pushed.Commits != 0 {
		t.Fatalf("push with nothing new = %+v, %v", pushed, err)
	}
	for _, bad := range []string{"main", "", "-x", "a..b", "with space"} {
		if _, err := w.service.Push(t.Context(), "p1", bad); !errors.Is(err, ErrInvalidBranch) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	w.commit(w.remote, "made on GitHub")
	w.commit(w.forgejo, "merged work order")
	theirs := w.main(w.remote)
	pushed, err := w.service.Push(t.Context(), "p1", "from-server")
	if err != nil || pushed.Commits != 1 || pushed.Branch != "from-server" {
		t.Fatalf("push = %+v, %v", pushed, err)
	}
	if w.main(w.remote) != theirs {
		t.Fatal("the linked repository's main moved")
	}
	if git(t, w.remote, "rev-parse", "refs/heads/from-server") != w.main(w.forgejo) {
		t.Fatal("main is not on the branch")
	}
	if view, _ := w.service.Get("p1"); view.PushBranch != "from-server" {
		t.Fatalf("offered branch = %q", view.PushBranch)
	}
	// The branch moves forward with main's next commit.
	w.commit(w.forgejo, "next work order")
	if _, err := w.service.Push(t.Context(), "p1", "from-server"); err != nil ||
		git(t, w.remote, "rev-parse", "refs/heads/from-server") != w.main(w.forgejo) {
		t.Fatalf("second push: %v", err)
	}
	// A branch there with commits main lacks is never overwritten.
	if _, err := w.service.Push(t.Context(), "p1", "main-copy"); err != nil {
		t.Fatal(err)
	}
	git(t, w.remote, "update-ref", "refs/heads/main-copy", theirs)
	if _, err := w.service.Push(t.Context(), "p1", "main-copy"); !errors.Is(err, ErrInvalidBranch) {
		t.Fatalf("push over their commits: %v", err)
	}
}

func TestPullRequestPagesFollowTheHost(t *testing.T) {
	for remote, want := range map[string]string{
		"https://github.com/you/app.git": "https://github.com/you/app/compare/main...commitarium/abc?expand=1",
		"https://gitlab.com/you/app.git": "https://gitlab.com/you/app/-/merge_requests/new?merge_request%5Bsource_branch%5D=commitarium%2Fabc&merge_request%5Btarget_branch%5D=main",
		"https://codeberg.org/you/app":   "https://codeberg.org/you/app/compare/main...commitarium/abc",
	} {
		if got := pullRequestURL(remote, "main", "commitarium/abc"); got != want {
			t.Errorf("%s: %s", remote, got)
		}
	}
}

func TestPushingNeedsAToken(t *testing.T) {
	w := newWorld(t)
	if err := w.service.update(func(links map[string]Link) {
		links["p1"] = Link{URL: w.remote, Branch: "main"}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Push(t.Context(), "p1", "x"); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestTheLinksFileIsTheCoordinatorsAlone(t *testing.T) {
	w := newWorld(t)
	info, err := os.Stat(w.service.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("links file: %v %v", info, err)
	}
	if err := w.service.Unlink("p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.service.Get("p1"); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("err = %v", err)
	}
}

func TestAddressesAreHTTPSWithoutCredentials(t *testing.T) {
	for _, address := range []string{
		"git@github.com:you/todo.git", "http://github.com/you/todo.git",
		"https://user:pass@github.com/you/todo.git", "https://github.com/",
	} {
		if _, err := validURL(address); !errors.Is(err, ErrInvalidLink) {
			t.Errorf("%s accepted", address)
		}
	}
	if got, err := validURL(" https://github.com/you/todo.git "); err != nil || got != "https://github.com/you/todo.git" {
		t.Fatalf("got %q %v", got, err)
	}
	if branch := defaultBranch("ref: refs/heads/trunk\tHEAD\nabc\tHEAD"); branch != "trunk" {
		t.Fatalf("branch = %q", branch)
	}
}
