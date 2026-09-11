package project

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

type recordingRepositoryImporter struct {
	importCalls int
	verifyCalls int
	spec        RepositoryImportSpec
	bundle      string
	repository  ForgejoRepository
	err         error
}

func (importer *recordingRepositoryImporter) Import(
	_ context.Context,
	spec RepositoryImportSpec,
	bundlePath string,
) (ForgejoRepository, error) {
	importer.importCalls++
	importer.spec = spec
	contents, _ := os.ReadFile(bundlePath)
	importer.bundle = string(contents)
	return importer.repository, importer.err
}

func (importer *recordingRepositoryImporter) Verify(
	_ context.Context,
	spec RepositoryImportSpec,
) (ForgejoRepository, error) {
	importer.verifyCalls++
	importer.spec = spec
	return importer.repository, importer.err
}

func TestServiceImportsAndAtomicallyBindsProject(t *testing.T) {
	store := NewMemoryStore()
	importer := &recordingRepositoryImporter{repository: ForgejoRepository{
		Owner: "commitarium", Name: importRepositoryName("Commitarium", "desktop-operation-1"), DefaultBranch: "main",
	}}
	service := NewServiceWithRepositoryVerifierAndImporter(store, nil, importer)
	fixedTime := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedTime }
	spec := ImportSpec{
		ImportID: "desktop-operation-1", Name: " Commitarium ",
		DialogueLimits: DefaultDialogueLimits(), DefaultBranch: "main",
	}

	created, wasCreated, err := service.Import(t.Context(), spec, strings.NewReader("bundle bytes"))
	if err != nil {
		t.Fatalf("import project: %v", err)
	}
	if !wasCreated || importer.importCalls != 1 || importer.verifyCalls != 0 {
		t.Fatalf("unexpected import calls created=%v import=%d verify=%d", wasCreated, importer.importCalls, importer.verifyCalls)
	}
	if importer.bundle != "bundle bytes" || importer.spec.BundleDigest != "sha256:f40a912044d47ea7c32f37340db4fd5eb853ffb8fcd983f605d9eb4f8b02fbc2" {
		t.Fatalf("unexpected bundle or digest %q %q", importer.bundle, importer.spec.BundleDigest)
	}
	if created.ID != projectImportID(spec.ImportID) || created.Name != "Commitarium" ||
		created.RecoveryPolicy != RecoveryPolicyApprovalRequired || created.ForgejoRepository == nil ||
		created.AgentProviders != DefaultAgentProviders() ||
		created.ForgejoRepository.BoundAt != fixedTime {
		t.Fatalf("unexpected imported project %+v", created)
	}
	stored, err := store.GetByID(t.Context(), created.ID)
	if err != nil || stored.ForgejoRepository == nil {
		t.Fatalf("project was not stored already bound: %+v err=%v", stored, err)
	}
}

func TestServiceProjectImportRetryVerifiesWithoutRepushing(t *testing.T) {
	store := NewMemoryStore()
	importer := &recordingRepositoryImporter{repository: ForgejoRepository{
		Owner: "commitarium", Name: importRepositoryName("Commitarium", "operation-1"), DefaultBranch: "main",
	}}
	service := NewServiceWithRepositoryVerifierAndImporter(store, nil, importer)
	spec := ImportSpec{
		ImportID: "operation-1", Name: "Commitarium",
		DialogueLimits: DefaultDialogueLimits(), DefaultBranch: "main",
	}
	first, _, err := service.Import(t.Context(), spec, strings.NewReader("same bundle"))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, created, err := service.Import(t.Context(), spec, strings.NewReader("same bundle"))
	if err != nil {
		t.Fatalf("retry import: %v", err)
	}
	if created || first.ID != second.ID || importer.importCalls != 1 || importer.verifyCalls != 1 {
		t.Fatalf("retry was not idempotent: created=%v import=%d verify=%d", created, importer.importCalls, importer.verifyCalls)
	}
}

func TestServiceProjectImportRejectsChangedRetry(t *testing.T) {
	store := NewMemoryStore()
	importer := &recordingRepositoryImporter{repository: ForgejoRepository{
		Owner: "commitarium", Name: importRepositoryName("Commitarium", "operation-1"), DefaultBranch: "main",
	}}
	service := NewServiceWithRepositoryVerifierAndImporter(store, nil, importer)
	spec := ImportSpec{
		ImportID: "operation-1", Name: "Commitarium",
		DialogueLimits: DefaultDialogueLimits(), DefaultBranch: "main",
	}
	if _, _, err := service.Import(t.Context(), spec, strings.NewReader("same bundle")); err != nil {
		t.Fatalf("first import: %v", err)
	}
	spec.Name = "Different project"
	_, _, err := service.Import(t.Context(), spec, strings.NewReader("same bundle"))
	if !errors.Is(err, ErrImportConflict) {
		t.Fatalf("expected %v, got %v", ErrImportConflict, err)
	}
}

func TestServiceProjectImportValidatesBeforeExternalWork(t *testing.T) {
	importer := &recordingRepositoryImporter{}
	service := NewServiceWithRepositoryVerifierAndImporter(NewMemoryStore(), nil, importer)
	_, _, err := service.Import(t.Context(), ImportSpec{
		ImportID: "unsafe/id", Name: "Commitarium",
		DialogueLimits: DefaultDialogueLimits(), DefaultBranch: "main",
	}, strings.NewReader("bundle"))
	if !errors.Is(err, ErrInvalidImportID) || importer.importCalls != 0 {
		t.Fatalf("expected validation failure before import, got %v calls=%d", err, importer.importCalls)
	}
}
