package project

import (
	"context"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const repositoryToolchainEvidenceMaxFiles = 32

var repositoryToolchainEvidenceNames = map[string]struct{}{
	".ruby-version": {}, ".tool-versions": {},
	"astro.config.mjs": {}, "bun.lock": {}, "bun.lockb": {},
	"cargo.lock": {}, "cargo.toml": {}, "composer.json": {}, "composer.lock": {},
	"compose.yaml": {}, "compose.yml": {}, "deno.json": {}, "deno.jsonc": {}, "deno.lock": {},
	"docker-compose.yaml": {}, "docker-compose.yml": {}, "dockerfile": {},
	"gemfile": {}, "gemfile.lock": {}, "go.mod": {}, "go.sum": {}, "go.work": {}, "go.work.sum": {},
	"gradle.properties": {}, "manage.py": {}, "mise.toml": {}, "next.config.js": {},
	"next.config.mjs": {}, "next.config.ts": {}, "package-lock.json": {}, "package.json": {},
	"pdm.lock": {}, "pixi.lock": {}, "pixi.toml": {}, "pnpm-lock.yaml": {}, "poetry.lock": {},
	"pom.xml": {}, "pyproject.toml": {}, "requirements.txt": {}, "rust-toolchain": {},
	"rust-toolchain.toml": {}, "setup.cfg": {}, "setup.py": {}, "settings.gradle": {},
	"settings.gradle.kts": {}, "svelte.config.js": {}, "uv.lock": {}, "vite.config.js": {},
	"vite.config.mjs": {}, "vite.config.ts": {}, "yarn.lock": {},
}

var repositorySourceExtensions = map[string]string{
	".cs": "C#", ".dart": "Dart", ".ex": "Elixir", ".exs": "Elixir",
	".go": "Go", ".java": "Java", ".js": "JavaScript", ".jsx": "JavaScript",
	".kt": "Kotlin", ".kts": "Kotlin", ".php": "PHP", ".py": "Python",
	".rb": "Ruby", ".rs": "Rust", ".scala": "Scala", ".swift": "Swift",
	".svelte": "Svelte", ".ts": "TypeScript", ".tsx": "TypeScript", ".vue": "Vue",
}

var repositoryIgnoredDirectories = map[string]struct{}{
	".git": {}, ".venv": {}, "build": {}, "dist": {}, "node_modules": {},
	"target": {}, "vendor": {},
}

func (s *Service) GetRepositoryToolchainEvidence(
	ctx context.Context,
	projectID string,
) (RepositoryToolchainEvidence, error) {
	storedProject, err := s.store.GetByID(ctx, projectID)
	if err != nil {
		return RepositoryToolchainEvidence{}, err
	}
	if storedProject.ForgejoRepository == nil {
		return RepositoryToolchainEvidence{}, ErrForgejoRepositoryNotReady
	}
	treeReader, treeOK := s.repositoryOverviewReader.(RepositoryTreeReader)
	blobReader, blobOK := s.repositoryOverviewReader.(RepositoryBlobReader)
	if !treeOK || !blobOK {
		return RepositoryToolchainEvidence{}, ErrForgejoUnavailable
	}
	repository := *storedProject.ForgejoRepository
	snapshot, err := treeReader.ReadRepositoryTree(
		ctx, repository.Owner, repository.Name, repository.DefaultBranch,
	)
	if err != nil {
		return RepositoryToolchainEvidence{}, err
	}

	evidence := RepositoryToolchainEvidence{
		DefaultBranch: snapshot.DefaultBranch,
		CommitID:      snapshot.Head.CommitID,
		Files:         []RepositoryEvidenceFile{}, LanguageCounts: map[string]int{},
	}
	candidates := make([]RepositoryTreeEntry, 0)
	for _, entry := range snapshot.Tree {
		if entry.Type != "file" || repositoryPathIgnored(entry.Path) {
			continue
		}
		if language, ok := repositorySourceExtensions[strings.ToLower(path.Ext(entry.Path))]; ok {
			evidence.LanguageCounts[language]++
		}
		if repositoryToolchainEvidencePath(entry.Path) &&
			entry.Size <= RepositoryDetectionFileMaxBytes {
			candidates = append(candidates, entry)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		leftDepth, rightDepth := strings.Count(candidates[i].Path, "/"), strings.Count(candidates[j].Path, "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return candidates[i].Path < candidates[j].Path
	})

	var total int64
	for _, entry := range candidates {
		if len(evidence.Files) >= repositoryToolchainEvidenceMaxFiles {
			break
		}
		if total+entry.Size > RepositoryToolchainEvidenceMaxBytes {
			continue
		}
		contents, readErr := blobReader.ReadRepositoryBlob(
			ctx, repository.Owner, repository.Name, entry.BlobID, RepositoryDetectionFileMaxBytes,
		)
		if readErr != nil {
			return RepositoryToolchainEvidence{}, readErr
		}
		if !utf8.Valid(contents) || strings.IndexByte(string(contents), 0) >= 0 {
			continue
		}
		evidence.Files = append(evidence.Files, RepositoryEvidenceFile{Path: entry.Path, Content: string(contents)})
		total += int64(len(contents))
	}
	return evidence, nil
}

func repositoryToolchainEvidencePath(value string) bool {
	lower := strings.ToLower(value)
	base := path.Base(lower)
	if _, ok := repositoryToolchainEvidenceNames[base]; ok {
		return true
	}
	return !strings.Contains(lower, "/") && strings.HasPrefix(base, "readme")
}

func repositoryPathIgnored(value string) bool {
	for _, component := range strings.Split(strings.ToLower(value), "/") {
		if _, ignored := repositoryIgnoredDirectories[component]; ignored {
			return true
		}
	}
	return false
}
