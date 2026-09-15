package project

import (
	"context"
	"slices"
	"testing"
)

type evidenceRepositoryReader struct {
	snapshot RepositoryTreeSnapshot
	blobs    map[string][]byte
	read     []string
}

func (*evidenceRepositoryReader) ReadRepositoryOverview(context.Context, string, string, string) (RepositoryOverview, error) {
	panic("unexpected overview read")
}

func (reader *evidenceRepositoryReader) ReadRepositoryTree(context.Context, string, string, string) (RepositoryTreeSnapshot, error) {
	return reader.snapshot, nil
}

func (reader *evidenceRepositoryReader) ReadRepositoryBlob(_ context.Context, _, _, blobID string, _ int64) ([]byte, error) {
	reader.read = append(reader.read, blobID)
	return reader.blobs[blobID], nil
}

func TestServiceBuildsBoundedToolchainEvidenceFromCommittedTree(t *testing.T) {
	store := &recordingStore{projectResult: Project{ID: "prj_test", ForgejoRepository: &ForgejoRepository{
		Owner: "owner", Name: "repo", DefaultBranch: "main",
	}}}
	reader := &evidenceRepositoryReader{
		snapshot: RepositoryTreeSnapshot{DefaultBranch: "main", Head: RepositoryHead{CommitID: "commit_one"}, Tree: []RepositoryTreeEntry{
			{Path: "README.md", Type: "file", BlobID: "readme", Size: 6},
			{Path: "package.json", Type: "file", BlobID: "package", Size: 16},
			{Path: "backend/pyproject.toml", Type: "file", BlobID: "python", Size: 18},
			{Path: "src/app.ts", Type: "file", BlobID: "typescript", Size: 10},
			{Path: "backend/app.py", Type: "file", BlobID: "python_source", Size: 10},
			{Path: "node_modules/dependency/package.json", Type: "file", BlobID: "ignored", Size: 10},
			{Path: ".env", Type: "file", BlobID: "secret", Size: 10},
		}},
		blobs: map[string][]byte{
			"readme": []byte("# App\n"), "package": []byte(`{"engines":{}}`),
			"python": []byte("[project]\nname='x'\n"),
		},
	}
	service := NewServiceWithRepositoryServices(store, nil, nil, reader)
	evidence, err := service.GetRepositoryToolchainEvidence(t.Context(), "prj_test")
	if err != nil {
		t.Fatalf("get evidence: %v", err)
	}
	if evidence.CommitID != "commit_one" || evidence.DefaultBranch != "main" ||
		evidence.LanguageCounts["TypeScript"] != 1 || evidence.LanguageCounts["Python"] != 1 {
		t.Fatalf("unexpected evidence %+v", evidence)
	}
	paths := make([]string, 0, len(evidence.Files))
	for _, file := range evidence.Files {
		paths = append(paths, file.Path)
	}
	if !slices.Equal(paths, []string{"README.md", "package.json", "backend/pyproject.toml"}) {
		t.Fatalf("unexpected evidence paths %v", paths)
	}
	if slices.Contains(reader.read, "secret") || slices.Contains(reader.read, "ignored") {
		t.Fatalf("unsafe files were read: %v", reader.read)
	}
}
