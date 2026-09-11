package secretfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAcceptsOneTrimmedSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("test-token\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	value, err := Read(path, "test token")
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	if value != "test-token" {
		t.Fatalf("token = %q", value)
	}
}

func TestReadRejectsMissingEmptyOrWhitespaceValuesWithoutLeakingThem(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name     string
		contents *string
	}{
		{name: "missing"},
		{name: "empty", contents: stringPointer("")},
		{name: "spaces", contents: stringPointer("secret value")},
		{name: "multiple lines", contents: stringPointer("first\nsecond")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name)
			if test.contents != nil {
				if err := os.WriteFile(path, []byte(*test.contents), 0o600); err != nil {
					t.Fatalf("write invalid token: %v", err)
				}
			}
			_, err := Read(path, "test token")
			if err == nil {
				t.Fatal("expected invalid token to fail")
			}
			if test.contents != nil && *test.contents != "" && strings.Contains(err.Error(), *test.contents) {
				t.Fatalf("error leaked secret contents: %v", err)
			}
		})
	}
}

func TestRequiredPathUsesNamedEnvironmentPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("test-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	value, err := RequiredPath(func(name string) string {
		if name == "TEST_TOKEN_FILE" {
			return path
		}
		return ""
	}, "TEST_TOKEN_FILE", "test token")
	if err != nil || value != "test-token" {
		t.Fatalf("required token = %q, error=%v", value, err)
	}
	if _, err := RequiredPath(func(string) string { return "" }, "TEST_TOKEN_FILE", "test token"); err == nil {
		t.Fatal("expected missing path variable to fail")
	}
}

func stringPointer(value string) *string { return &value }
