package testpreviews

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	helperA = "tsk_0123456789abcdef01234567"
	helperB = "tsk_1123456789abcdef01234567"
	helperC = "tsk_2123456789abcdef01234567"
)

func newStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "data", "test-previews.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 2, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	return store, &now
}

func TestATestPreviewGoesThroughItsLife(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	requested, err := store.Request(ctx, helperA, "prj_abc", "claude")
	if err != nil || requested.Status != Pending {
		t.Fatalf("Request = %+v, %v", requested, err)
	}
	if again, err := store.Request(ctx, helperA, "prj_abc", "claude"); err != nil || again.Status != Pending {
		t.Fatalf("asking again = %+v, %v; want the same record", again, err)
	}
	if _, err := store.Ready(ctx, helperA, "http://172.18.0.3:3000"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Ready before Claim = %v, want ErrConflict", err)
	}
	if claimed, err := store.Claim(ctx, helperA); err != nil || claimed.Status != Starting {
		t.Fatalf("Claim = %+v, %v", claimed, err)
	}
	if _, err := store.Claim(ctx, helperA); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second Claim = %v, want ErrConflict", err)
	}
	ready, err := store.Ready(ctx, helperA, "http://172.18.0.3:3000")
	if err != nil || ready.Status != Ready || ready.Address != "http://172.18.0.3:3000" {
		t.Fatalf("Ready = %+v, %v", ready, err)
	}
	if err := store.Removed(ctx, helperA); !errors.Is(err, ErrConflict) {
		t.Fatalf("Removed while wanted = %v, want ErrConflict", err)
	}
	if err := store.End(ctx, helperA); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(ctx, helperA); got.Status != Ending {
		t.Fatalf("after End = %+v, want ending", got)
	}
	if _, err := store.Request(ctx, helperA, "prj_abc", "claude"); !errors.Is(err, ErrConflict) {
		t.Fatalf("Request while ending = %v, want ErrConflict", err)
	}
	if err := store.Removed(ctx, helperA); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, helperA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Removed = %v, want ErrNotFound", err)
	}
	if err := store.End(ctx, helperA); err != nil {
		t.Fatalf("ending one that is gone = %v, want nil", err)
	}
}

func TestRequestsAreValidatedAndBounded(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	for _, bad := range [][3]string{
		{"tsk_short", "prj_abc", "claude"},
		{helperA, "../prj", "claude"},
		{helperA, "prj_abc", "Claude"},
		{helperA, "prj_abc", "-claude"},
	} {
		if _, err := store.Request(ctx, bad[0], bad[1], bad[2]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Request(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	for _, id := range []string{helperA, helperB} {
		if _, err := store.Request(ctx, id, "prj_abc", "claude"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Request(ctx, helperC, "prj_abc", "claude"); !errors.Is(err, ErrTooMany) {
		t.Fatalf("a third at once = %v, want ErrTooMany", err)
	}
	if _, err := store.Fail(ctx, helperB, "the build failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Request(ctx, helperC, "prj_abc", "claude"); err != nil {
		t.Fatalf("a failed one leaves room: %v", err)
	}
}

func TestOnlyPrivateHTTPAddressesAreRecorded(t *testing.T) {
	for address, want := range map[string]bool{
		"http://172.18.0.3:3000":         true,
		"http://10.0.0.5:8080/":          true,
		"http://192.168.65.2:80":         true,
		"https://172.18.0.3:3000":        false,
		"http://8.8.8.8:80":              false,
		"http://127.0.0.1:3000":          false,
		"http://172.18.0.3":              false,
		"http://web:3000":                false,
		"http://172.18.0.3:3000/x":       false,
		"http://u:p@172.18.0.3:3000":     false,
		"http://172.18.0.3:3000?next=/x": false,
		"http://[fd00::1]:3000":          false,
	} {
		if got := ValidAddress(address); got != want {
			t.Errorf("ValidAddress(%q) = %v, want %v", address, got, want)
		}
	}
}

func TestOldTestPreviewsEndByThemselves(t *testing.T) {
	ctx := context.Background()
	store, now := newStore(t)
	if _, err := store.Request(ctx, helperA, "prj_abc", "claude"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(MaxAge + time.Minute)
	listed, err := store.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].Status != Ending || listed[0].Reason == "" {
		t.Fatalf("List = %+v, %v; want it ending, with a reason", listed, err)
	}
}

func TestFailingRecordsABoundedReasonAndDeletingAProjectEndsItsPreviews(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	if _, err := store.Request(ctx, helperA, "prj_abc", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Request(ctx, helperB, "prj_other", "claude"); err != nil {
		t.Fatal(err)
	}
	long := make([]byte, 2000)
	for index := range long {
		long[index] = 'x'
	}
	failed, err := store.Fail(ctx, helperA, string(long))
	if err != nil || failed.Status != Failed || len(failed.Reason) > maxReason+len("…") {
		t.Fatalf("Fail = %d bytes, %v", len(failed.Reason), err)
	}
	if err := store.DeleteProject(ctx, "prj_abc"); err != nil {
		t.Fatal(err)
	}
	listed, _ := store.List(ctx)
	for _, preview := range listed {
		if want := map[string]Status{helperA: Ending, helperB: Pending}[preview.ID]; preview.Status != want {
			t.Fatalf("%s is %s, want %s", preview.ID, preview.Status, want)
		}
	}
}

func TestRecordsSurviveARestartIn0600(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	if _, err := store.Request(ctx, helperA, "prj_abc", "claude"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	reopened, err := NewStore(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Get(ctx, helperA); err != nil || got.ProjectID != "prj_abc" {
		t.Fatalf("after a restart = %+v, %v", got, err)
	}
}
