package testaccounts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "test-accounts")
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, root
}

func TestAddMakesAUniqueUsernameAndAStrongPassword(t *testing.T) {
	store, root := newStore(t)
	ctx := t.Context()
	first, err := store.Add(ctx, "prj_one", " Admin (Anna) ", "admin", "Sees everything")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if first.Username != "admin-anna@commitarium.test" || first.Label != "Admin (Anna)" || first.Role != "admin" {
		t.Fatalf("first = %+v", first)
	}
	second, err := store.Add(ctx, "prj_one", "admin anna", "member", "")
	if err != nil || second.Username != "admin-anna-2@commitarium.test" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	other, err := store.Add(ctx, "prj_one", "Ólöf Þórsdóttir", "", "")
	if err != nil || other.Username != "olof-thorsdottir@commitarium.test" {
		t.Fatalf("other = %+v, %v", other, err)
	}
	none, err := store.Add(ctx, "prj_one", "管理者", "", "")
	if err != nil || none.Username != "user@commitarium.test" {
		t.Fatalf("none = %+v, %v", none, err)
	}

	password, err := store.Password(ctx, "prj_one", first.Username)
	if err != nil || len(password) != passwordLength {
		t.Fatalf("password length %d, %v", len(password), err)
	}
	again, _ := store.Password(ctx, "prj_one", second.Username)
	if again == password {
		t.Fatal("two logins got the same password")
	}
	info, err := os.Stat(filepath.Join(root, "prj_one.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, %v", info, err)
	}
}

func TestListLeavesPasswordsOut(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()
	added, _ := store.Add(ctx, "prj_one", "Customer", "customer", "")
	password, _ := store.Password(ctx, "prj_one", added.Username)
	listed, err := store.List(ctx, "prj_one")
	if err != nil || len(listed) != 1 || listed[0].Username != added.Username {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	if strings.Contains(strings.ToLower(strings.Join([]string{listed[0].Label, listed[0].Role, listed[0].Note}, " ")), strings.ToLower(password)) {
		t.Fatal("the list carries a password")
	}
	empty, err := store.List(ctx, "prj_two")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
}

func TestUpdateChangesRoleAndNoteOnly(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()
	added, _ := store.Add(ctx, "prj_one", "Staff", "staff", "old")
	password, _ := store.Password(ctx, "prj_one", added.Username)
	role := " manager "
	updated, err := store.Update(ctx, "prj_one", added.Username, &role, nil)
	if err != nil || updated.Role != "manager" || updated.Note != "old" {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	note := "new"
	updated, err = store.Update(ctx, "prj_one", added.Username, nil, &note)
	if err != nil || updated.Role != "manager" || updated.Note != "new" {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	if after, _ := store.Password(ctx, "prj_one", added.Username); after != password {
		t.Fatal("an update changed the password")
	}
	if _, err := store.Update(ctx, "prj_one", "missing@commitarium.test", &role, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	long := strings.Repeat("x", maxRole+1)
	if _, err := store.Update(ctx, "prj_one", added.Username, &long, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long role = %v", err)
	}
}

func TestRemoveAndDeleteProject(t *testing.T) {
	store, root := newStore(t)
	ctx := t.Context()
	first, _ := store.Add(ctx, "prj_one", "First", "", "")
	if _, err := store.Add(ctx, "prj_one", "Second", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, "prj_one", first.Username); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := store.Remove(ctx, "prj_one", first.Username); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove again = %v", err)
	}
	if listed, _ := store.List(ctx, "prj_one"); len(listed) != 1 || listed[0].Label != "Second" {
		t.Fatalf("after remove = %+v", listed)
	}
	if err := store.DeleteProject(ctx, "prj_one"); err != nil {
		t.Fatalf("delete project: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "prj_one.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file left after deletion: %v", err)
	}
	if err := store.DeleteProject(ctx, "prj_one"); err != nil {
		t.Fatalf("delete project again: %v", err)
	}
}

func TestPreviewCarriesPasswordsAndTheDomain(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()
	empty, err := store.Preview(ctx, "prj_one")
	if err != nil || empty.Domain != Domain || empty.Accounts == nil || len(empty.Accounts) != 0 {
		t.Fatalf("empty = %+v, %v", empty, err)
	}
	added, _ := store.Add(ctx, "prj_one", "Admin", "admin", "a note the app never needs")
	password, _ := store.Password(ctx, "prj_one", added.Username)
	file, err := store.Preview(ctx, "prj_one")
	if err != nil || len(file.Accounts) != 1 {
		t.Fatalf("file = %+v, %v", file, err)
	}
	if file.Accounts[0] != (PreviewAccount{Username: added.Username, Password: password, Role: "admin", Label: "Admin"}) {
		t.Fatalf("account = %+v", file.Accounts[0])
	}
}

func TestInvalidInputAndLimits(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()
	for _, test := range []struct{ project, label, role, note string }{
		{"prj_one", "", "", ""},
		{"prj_one", strings.Repeat("x", maxLabel+1), "", ""},
		{"prj_one", "Label", "role\nwith newline", ""},
		{"prj_one", "Label", "", strings.Repeat("x", maxNote+1)},
		{"../escape", "Label", "", ""},
	} {
		if _, err := store.Add(ctx, test.project, test.label, test.role, test.note); !errors.Is(err, ErrInvalid) {
			t.Fatalf("add %+v = %v", test, err)
		}
	}
	for range MaxAccounts {
		if _, err := store.Add(ctx, "prj_full", "User", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Add(ctx, "prj_full", "User", "", ""); !errors.Is(err, ErrTooMany) {
		t.Fatalf("over the limit = %v", err)
	}
}

func TestPasswordsUseTheAlphabetOnly(t *testing.T) {
	for range 20 {
		password, err := newPassword()
		if err != nil || len(password) != passwordLength {
			t.Fatalf("password %q, %v", password, err)
		}
		for _, char := range password {
			if !strings.ContainsRune(passwordAlphabet, char) {
				t.Fatalf("password has %q", char)
			}
		}
	}
}

func TestALabelTypedAsAnEmailNamesTheLoginByItsFirstPart(t *testing.T) {
	for label, want := range map[string]string{
		"einar@einarlogi.is": "einar",
		"Mayka@Mayka.is":     "mayka",
		"@nobody":            "nobody",
		"admin":              "admin",
	} {
		if got := slug(label); got != want {
			t.Errorf("slug(%q) = %q, want %q", label, got, want)
		}
	}
}
