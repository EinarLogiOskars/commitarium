// Package secretfile loads process credentials from files mounted by the
// trusted desktop host. Keeping this validation in one place prevents the
// coordinator and workers from accidentally accepting empty or multi-value
// credential files.
package secretfile

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// Read returns the single non-whitespace value stored at path. Error messages
// identify only the credential purpose and never include file contents.
func Read(path string, purpose string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s file path is required", purpose)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s file is unavailable", purpose)
	}
	value := strings.TrimSpace(string(contents))
	if value == "" || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("%s file must contain one non-whitespace value", purpose)
	}
	return value, nil
}

// RequiredPath reads a required file path from the supplied environment
// accessor before loading its value. It exists mainly to keep command startup
// configuration deterministic and easy to exercise in tests.
func RequiredPath(getenv func(string) string, variable string, purpose string) (string, error) {
	if getenv == nil {
		return "", errors.New("environment accessor is required")
	}
	path := strings.TrimSpace(getenv(variable))
	if path == "" {
		return "", fmt.Errorf("%s is required", variable)
	}
	return Read(path, purpose)
}
