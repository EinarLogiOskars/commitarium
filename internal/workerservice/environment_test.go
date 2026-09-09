package workerservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/EinarLogiOskars/commitarium/internal/workerhttp"
)

func TestRootedEnvironmentResolverSelectsPreparedWorkspaces(t *testing.T) {
	root := t.TempDir()
	firstDirectory := filepath.Join(root, "workspace_one")
	secondDirectory := filepath.Join(root, "workspace_two")
	for _, directory := range []string{firstDirectory, secondDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create workspace: %v", err)
		}
	}
	variables := []string{
		"PATH=/usr/bin:/bin",
		"CODEX_HOME=/var/lib/commitarium/provider",
	}
	resolver, err := NewRootedEnvironmentResolver(RootedEnvironmentResolverConfig{
		AgentProfileID: "profile_test",
		WorkspaceRoot:  root,
		Variables:      variables,
	})
	if err != nil {
		t.Fatalf("create rooted resolver: %v", err)
	}

	variables[0] = "PATH=/changed-after-resolver-creation"
	first := environmentTestAssignment()
	resolved, err := resolver.Resolve(t.Context(), first)
	if err != nil {
		t.Fatalf("resolve first workspace: %v", err)
	}
	canonicalFirst, err := filepath.EvalSymlinks(firstDirectory)
	if err != nil {
		t.Fatalf("canonicalize first workspace: %v", err)
	}
	if !launchEnvironmentMatches(resolved, first) ||
		resolved.WorkingDirectory != canonicalFirst ||
		resolved.Variables[0] != "PATH=/usr/bin:/bin" {
		t.Fatalf("first resolved environment = %+v", resolved)
	}

	// Project, feature, and role identify the current work; they do not require
	// a second predeclared environment record for the same workspace.
	second := first
	second.ProjectID = "prj_other"
	second.FeatureID = "fea_other"
	second.Role = workerhttp.RoleReviewer
	second.WorkspaceID = "workspace_two"
	resolved, err = resolver.Resolve(t.Context(), second)
	if err != nil {
		t.Fatalf("resolve second assignment: %v", err)
	}
	canonicalSecond, err := filepath.EvalSymlinks(secondDirectory)
	if err != nil {
		t.Fatalf("canonicalize second workspace: %v", err)
	}
	if !launchEnvironmentMatches(resolved, second) || resolved.WorkingDirectory != canonicalSecond {
		t.Fatalf("second resolved environment = %+v", resolved)
	}

	resolved.Variables[0] = "PATH=/mutated-by-caller"
	again, err := resolver.Resolve(t.Context(), first)
	if err != nil || again.Variables[0] != "PATH=/usr/bin:/bin" {
		t.Fatalf("caller mutated resolver state: environment=%+v error=%v", again, err)
	}
}

func TestRootedEnvironmentResolverRejectsUnavailableOrEscapingWorkspaces(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace_one")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "workspace_link")); err != nil {
		t.Fatalf("create escaping workspace symlink: %v", err)
	}
	resolver, err := NewRootedEnvironmentResolver(RootedEnvironmentResolverConfig{
		AgentProfileID: "profile_test",
		WorkspaceRoot:  root,
		Variables:      []string{},
	})
	if err != nil {
		t.Fatalf("create rooted resolver: %v", err)
	}

	tests := []struct {
		name       string
		assignment workerhttp.Assignment
		want       error
	}{
		{name: "different profile", assignment: withAssignmentProfile(environmentTestAssignment(), "profile_other"), want: ErrProfileUnavailable},
		{name: "unknown workspace", assignment: withAssignmentWorkspace(environmentTestAssignment(), "workspace_missing"), want: ErrWorkspaceUnavailable},
		{name: "escaping symlink", assignment: withAssignmentWorkspace(environmentTestAssignment(), "workspace_link"), want: ErrWorkspaceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolver.Resolve(t.Context(), test.assignment); !errors.Is(err, test.want) {
				t.Fatalf("resolve error = %v, want %v", err, test.want)
			}
		})
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := resolver.Resolve(cancelled, environmentTestAssignment()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolve error = %v", err)
	}
}

func TestRootedEnvironmentResolverRejectsInvalidConfiguration(t *testing.T) {
	root := t.TempDir()
	tests := []RootedEnvironmentResolverConfig{
		{},
		{AgentProfileID: "profile_test", WorkspaceRoot: "relative", Variables: []string{}},
		{AgentProfileID: "profile_test", WorkspaceRoot: root},
		{AgentProfileID: "profile_test", WorkspaceRoot: root, Variables: []string{"PATH=/one", "PATH=/two"}},
	}
	for index, config := range tests {
		if _, err := NewRootedEnvironmentResolver(config); !errors.Is(err, ErrInvalidEnvironmentResolver) {
			t.Errorf("case %d error = %v, want ErrInvalidEnvironmentResolver", index, err)
		}
	}
}

func environmentTestAssignment() workerhttp.Assignment {
	return workerhttp.Assignment{
		AgentProfileID: "profile_test",
		ProjectID:      "prj_test",
		FeatureID:      "fea_test",
		Role:           workerhttp.RoleCoder,
		WorkspaceID:    "workspace_one",
	}
}

func withAssignmentProfile(assignment workerhttp.Assignment, profileID string) workerhttp.Assignment {
	assignment.AgentProfileID = profileID
	return assignment
}

func withAssignmentWorkspace(assignment workerhttp.Assignment, workspaceID string) workerhttp.Assignment {
	assignment.WorkspaceID = workspaceID
	return assignment
}
