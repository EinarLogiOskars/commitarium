package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

const importDigestPrefix = "sha256:"

var (
	ErrInvalidImportID      = errors.New("invalid project import ID")
	ErrInvalidDefaultBranch = errors.New("invalid project import default branch")
	ErrInvalidGitBundle     = errors.New("invalid Git bundle")
	ErrImportConflict       = errors.New("project import conflicts with existing state")
	ErrImportUnavailable    = errors.New("project import is unavailable")
	safeImportID            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

// ImportSpec contains only portable project metadata. The trusted desktop host
// deliberately keeps the source directory path out of the coordinator.
type ImportSpec struct {
	ImportID       string
	Name           string
	RecoveryPolicy RecoveryPolicy
	DialogueLimits DialogueLimits
	DefaultBranch  string
}

type RepositoryImportSpec struct {
	ImportID      string
	Repository    string
	DefaultBranch string
	BundleDigest  string
}

type RepositoryImporter interface {
	Import(ctx context.Context, spec RepositoryImportSpec, bundlePath string) (ForgejoRepository, error)
	Verify(ctx context.Context, spec RepositoryImportSpec) (ForgejoRepository, error)
}

type unavailableRepositoryImporter struct{}

func (unavailableRepositoryImporter) Import(context.Context, RepositoryImportSpec, string) (ForgejoRepository, error) {
	return ForgejoRepository{}, ErrImportUnavailable
}

func (unavailableRepositoryImporter) Verify(context.Context, RepositoryImportSpec) (ForgejoRepository, error) {
	return ForgejoRepository{}, ErrImportUnavailable
}

func normalizeImportSpec(spec ImportSpec) (ImportSpec, error) {
	spec.ImportID = strings.TrimSpace(spec.ImportID)
	spec.Name = strings.TrimSpace(spec.Name)
	spec.DefaultBranch = strings.TrimSpace(spec.DefaultBranch)
	if !safeImportID.MatchString(spec.ImportID) {
		return ImportSpec{}, ErrInvalidImportID
	}
	if spec.Name == "" {
		return ImportSpec{}, ErrNameRequired
	}
	policy, err := NormalizeRecoveryPolicy(spec.RecoveryPolicy)
	if err != nil {
		return ImportSpec{}, err
	}
	spec.RecoveryPolicy = policy
	if err := spec.DialogueLimits.Validate(); err != nil {
		return ImportSpec{}, err
	}
	if spec.DefaultBranch == "" || strings.ContainsAny(spec.DefaultBranch, " ~^:?*[\\") ||
		strings.HasPrefix(spec.DefaultBranch, "-") || strings.HasPrefix(spec.DefaultBranch, ".") ||
		strings.HasSuffix(spec.DefaultBranch, "/") || strings.HasSuffix(spec.DefaultBranch, ".") ||
		strings.Contains(spec.DefaultBranch, "..") || strings.Contains(spec.DefaultBranch, "//") ||
		strings.HasSuffix(spec.DefaultBranch, ".lock") {
		return ImportSpec{}, ErrInvalidDefaultBranch
	}
	return spec, nil
}

func projectImportID(importID string) string {
	digest := sha256.Sum256([]byte(importID))
	return "prj_" + hex.EncodeToString(digest[:12])
}

func importRepositoryName(name, importID string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(name) {
		isAlphaNumeric := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if isAlphaNumeric {
			builder.WriteRune(character)
			lastDash = false
		} else if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
		if builder.Len() >= 72 {
			break
		}
	}
	base := strings.Trim(builder.String(), "-")
	if base == "" {
		base = "project"
	}
	digest := sha256.Sum256([]byte(importID))
	return base + "-" + hex.EncodeToString(digest[:6])
}

func writeImportBundle(bundle io.Reader) (string, string, error) {
	file, err := os.CreateTemp("", "commitarium-import-*.bundle")
	if err != nil {
		return "", "", fmt.Errorf("%w: create temporary bundle: %v", ErrImportUnavailable, err)
	}
	path := file.Name()
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, hasher), bundle); err != nil {
		return "", "", fmt.Errorf("%w: store uploaded bundle: %v", ErrImportUnavailable, err)
	}
	if err := file.Close(); err != nil {
		return "", "", fmt.Errorf("%w: close uploaded bundle: %v", ErrImportUnavailable, err)
	}
	remove = false
	return path, importDigestPrefix + hex.EncodeToString(hasher.Sum(nil)), nil
}

func sameImportedProject(stored Project, spec ImportSpec, repository ForgejoRepository) bool {
	return stored.ID == projectImportID(spec.ImportID) && stored.Name == spec.Name &&
		stored.RecoveryPolicy == spec.RecoveryPolicy && stored.DialogueLimits == spec.DialogueLimits &&
		stored.ForgejoRepository != nil &&
		sameRepositoryCoordinate(*stored.ForgejoRepository, repository.Owner, repository.Name) &&
		stored.ForgejoRepository.DefaultBranch == repository.DefaultBranch
}
