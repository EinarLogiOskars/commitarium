package forgejo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/project"
)

func (client *Client) ReadRepositoryOverview(
	ctx context.Context,
	owner string,
	name string,
	defaultBranch string,
) (project.RepositoryOverview, error) {
	owner, name, err := project.NormalizeRepositoryCoordinate(owner, name)
	if err != nil {
		return project.RepositoryOverview{}, err
	}
	defaultBranch = strings.TrimSpace(defaultBranch)
	if defaultBranch == "" {
		return project.RepositoryOverview{}, project.ErrForgejoRepositoryNotReady
	}

	status, body, err := client.doJSON(
		ctx,
		http.MethodGet,
		repositoryBranchPath(owner, name, defaultBranch),
		nil,
	)
	if err != nil {
		return project.RepositoryOverview{}, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return project.RepositoryOverview{}, project.ErrForgejoRepositoryNotReady
	default:
		return project.RepositoryOverview{}, fmt.Errorf(
			"%w: repository branch request returned HTTP %d",
			project.ErrForgejoUnavailable,
			status,
		)
	}
	head, err := decodeRepositoryOverviewHead(body, defaultBranch)
	if err != nil {
		return project.RepositoryOverview{}, err
	}

	status, body, err = client.doJSON(
		ctx,
		http.MethodGet,
		repositoryTreePath(owner, name, head.CommitID),
		nil,
	)
	if err != nil {
		return project.RepositoryOverview{}, err
	}
	if status != http.StatusOK {
		return project.RepositoryOverview{}, fmt.Errorf(
			"%w: repository tree request returned HTTP %d",
			project.ErrForgejoUnavailable,
			status,
		)
	}
	tree, readme, err := decodeRepositoryOverviewTree(body)
	if err != nil {
		return project.RepositoryOverview{}, err
	}
	overview := project.RepositoryOverview{
		DefaultBranch: defaultBranch,
		Head:          head,
		Tree:          tree,
	}
	if readme == nil {
		return overview, nil
	}
	if readme.Size > project.RepositoryReadmeMaxBytes {
		return project.RepositoryOverview{}, project.ErrRepositoryContentTooLarge
	}

	status, body, err = client.doJSON(
		ctx,
		http.MethodGet,
		repositoryBlobPath(owner, name, readme.SHA),
		nil,
	)
	if err != nil {
		return project.RepositoryOverview{}, err
	}
	if status != http.StatusOK {
		return project.RepositoryOverview{}, fmt.Errorf(
			"%w: README blob request returned HTTP %d",
			project.ErrForgejoUnavailable,
			status,
		)
	}
	contents, err := decodeRepositoryReadme(body, readme.SHA)
	if err != nil {
		return project.RepositoryOverview{}, err
	}
	overview.ReadmeMarkdown = &contents
	return overview, nil
}

type repositoryTreeEntry struct {
	Path string `json:"path"`
	SHA  string `json:"sha"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func decodeRepositoryOverviewHead(
	body []byte,
	expectedBranch string,
) (project.RepositoryHead, error) {
	var decoded struct {
		Name   string `json:"name"`
		Commit struct {
			ID        string `json:"id"`
			Message   string `json:"message"`
			Timestamp string `json:"timestamp"`
			Author    struct {
				Name     string `json:"name"`
				Username string `json:"username"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return project.RepositoryHead{}, fmt.Errorf(
			"%w: repository branch response is invalid JSON",
			project.ErrForgejoUnavailable,
		)
	}
	if strings.TrimSpace(decoded.Name) != expectedBranch {
		return project.RepositoryHead{}, fmt.Errorf(
			"%w: repository branch response returned a different branch",
			project.ErrForgejoUnavailable,
		)
	}
	committedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(decoded.Commit.Timestamp))
	if err != nil {
		return project.RepositoryHead{}, fmt.Errorf(
			"%w: repository branch response has an invalid commit time",
			project.ErrForgejoUnavailable,
		)
	}
	author := strings.TrimSpace(decoded.Commit.Author.Name)
	if author == "" {
		author = strings.TrimSpace(decoded.Commit.Author.Username)
	}
	head := project.RepositoryHead{
		CommitID:    strings.TrimSpace(decoded.Commit.ID),
		Message:     strings.TrimSpace(decoded.Commit.Message),
		Author:      author,
		CommittedAt: committedAt.UTC(),
	}
	if head.CommitID == "" || head.Author == "" {
		return project.RepositoryHead{}, fmt.Errorf(
			"%w: repository branch response omitted commit metadata",
			project.ErrForgejoUnavailable,
		)
	}
	return head, nil
}

func decodeRepositoryOverviewTree(
	body []byte,
) ([]project.RepositoryTreeEntry, *repositoryTreeEntry, error) {
	var decoded struct {
		Tree      []repositoryTreeEntry `json:"tree"`
		Truncated bool                  `json:"truncated"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Truncated {
		return nil, nil, fmt.Errorf(
			"%w: repository tree response is invalid or incomplete",
			project.ErrForgejoUnavailable,
		)
	}
	tree := make([]project.RepositoryTreeEntry, 0, len(decoded.Tree))
	var readme *repositoryTreeEntry
	readmeRank := len(repositoryReadmeNames)
	seen := make(map[string]struct{}, len(decoded.Tree))
	for _, entry := range decoded.Tree {
		entry.Path = strings.TrimSpace(entry.Path)
		entry.SHA = strings.TrimSpace(entry.SHA)
		entry.Type = strings.TrimSpace(entry.Type)
		if entry.Path == "" || strings.Contains(entry.Path, "/") || entry.SHA == "" || entry.Size < 0 {
			return nil, nil, fmt.Errorf(
				"%w: repository tree response contains an invalid entry",
				project.ErrForgejoUnavailable,
			)
		}
		if _, exists := seen[entry.Path]; exists {
			return nil, nil, fmt.Errorf(
				"%w: repository tree response contains duplicate entries",
				project.ErrForgejoUnavailable,
			)
		}
		seen[entry.Path] = struct{}{}
		entryType := "file"
		switch entry.Type {
		case "tree":
			entryType = "dir"
		case "blob", "commit":
		default:
			return nil, nil, fmt.Errorf(
				"%w: repository tree response contains an unsupported entry type",
				project.ErrForgejoUnavailable,
			)
		}
		tree = append(tree, project.RepositoryTreeEntry{
			Path: entry.Path, Type: entryType, BlobID: entry.SHA, Size: entry.Size,
		})
		if entry.Type == "blob" {
			if rank := repositoryReadmeRank(entry.Path); rank < readmeRank {
				candidate := entry
				readme = &candidate
				readmeRank = rank
			}
		}
	}
	sort.Slice(tree, func(i, j int) bool { return tree[i].Path < tree[j].Path })
	return tree, readme, nil
}

func (client *Client) ReadRepositoryBlob(
	ctx context.Context,
	owner string,
	repository string,
	blobID string,
	maxBytes int64,
) ([]byte, error) {
	owner, repository, err := project.NormalizeRepositoryCoordinate(owner, repository)
	if err != nil {
		return nil, err
	}
	blobID = strings.TrimSpace(blobID)
	if blobID == "" || strings.ContainsAny(blobID, "/\\") || maxBytes <= 0 ||
		maxBytes > project.RepositoryDetectionFileMaxBytes {
		return nil, project.ErrForgejoUnavailable
	}
	status, body, err := client.doJSON(ctx, http.MethodGet, repositoryBlobPath(owner, repository, blobID), nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%w: repository blob request returned HTTP %d", project.ErrForgejoUnavailable, status)
	}
	return decodeRepositoryBlob(body, blobID, maxBytes)
}

func decodeRepositoryBlob(body []byte, expectedSHA string, maxBytes int64) ([]byte, error) {
	var decoded struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || strings.TrimSpace(decoded.SHA) != expectedSHA ||
		decoded.Encoding != "base64" || decoded.Size < 0 {
		return nil, fmt.Errorf("%w: repository blob response is invalid", project.ErrForgejoUnavailable)
	}
	if decoded.Size > maxBytes {
		return nil, project.ErrRepositoryContentTooLarge
	}
	contents, err := base64.StdEncoding.DecodeString(decoded.Content)
	if err != nil || int64(len(contents)) != decoded.Size || int64(len(contents)) > maxBytes {
		return nil, fmt.Errorf("%w: repository blob response has invalid content", project.ErrForgejoUnavailable)
	}
	return contents, nil
}

var repositoryReadmeNames = []string{
	"README.md",
	"README.markdown",
	"README",
	"README.txt",
	"README.rst",
}

func repositoryReadmeRank(path string) int {
	for index, name := range repositoryReadmeNames {
		if strings.EqualFold(path, name) {
			return index
		}
	}
	return len(repositoryReadmeNames)
}

func decodeRepositoryReadme(body []byte, expectedSHA string) (string, error) {
	var decoded struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		SHA      string `json:"sha"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf(
			"%w: README blob response is invalid JSON",
			project.ErrForgejoUnavailable,
		)
	}
	if strings.TrimSpace(decoded.SHA) != expectedSHA || decoded.Encoding != "base64" || decoded.Size < 0 {
		return "", fmt.Errorf(
			"%w: README blob response is invalid",
			project.ErrForgejoUnavailable,
		)
	}
	if decoded.Size > project.RepositoryReadmeMaxBytes {
		return "", project.ErrRepositoryContentTooLarge
	}
	contents, err := base64.StdEncoding.DecodeString(decoded.Content)
	if err != nil || int64(len(contents)) != decoded.Size {
		return "", fmt.Errorf(
			"%w: README blob response has invalid content",
			project.ErrForgejoUnavailable,
		)
	}
	if int64(len(contents)) > project.RepositoryReadmeMaxBytes {
		return "", project.ErrRepositoryContentTooLarge
	}
	return string(contents), nil
}

func repositoryTreePath(owner, repository, commitID string) string {
	return "/api/v1/repos/" + url.PathEscape(owner) + "/" +
		url.PathEscape(repository) + "/git/trees/" + url.PathEscape(commitID) +
		"?recursive=false"
}

func repositoryBlobPath(owner, repository, blobID string) string {
	return "/api/v1/repos/" + url.PathEscape(owner) + "/" +
		url.PathEscape(repository) + "/git/blobs/" + url.PathEscape(blobID)
}
