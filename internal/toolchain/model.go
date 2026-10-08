package toolchain

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Source string

const (
	SourcePicker    Source = "picker"
	SourceDetected  Source = "detected"
	SourceAssistant Source = "assistant"
	SourceRuntime   Source = "runtime"
)

type Status string

type ProvisioningStatus string

const (
	StatusNeedsSetup       Status             = "needs_setup"
	StatusConfigured       Status             = "configured"
	ProvisioningPending    ProvisioningStatus = "pending"
	ProvisioningInstalling ProvisioningStatus = "installing"
	ProvisioningReady      ProvisioningStatus = "ready"
	ProvisioningFailed     ProvisioningStatus = "failed"
)

var (
	ErrInvalidManifest = errors.New("invalid project toolchain manifest")
	ErrUnavailable     = errors.New("project toolchain storage is unavailable")
	exactVersion       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	safeService        = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	safeProcess        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	allowedTools       = map[string]struct{}{
		"bun": {}, "deno": {}, "go": {}, "gradle": {}, "java": {},
		"maven": {}, "node": {}, "php": {}, "python": {}, "ruby": {}, "rust": {},
	}
)

type Manifest struct {
	ProjectID           string             `json:"project_id"`
	Status              Status             `json:"status"`
	Source              Source             `json:"source,omitempty"`
	Tools               map[string]string  `json:"tools"`
	Services            []string           `json:"services"`
	ServicesRunnable    bool               `json:"services_runnable"`
	Run                 *Run               `json:"run,omitempty"`
	ProvisioningStatus  ProvisioningStatus `json:"provisioning_status,omitempty"`
	ProvisioningMessage string             `json:"provisioning_message,omitempty"`
	UpdatedAt           time.Time          `json:"updated_at,omitempty"`
}

type Suggestion struct {
	Tools      map[string]string `json:"tools"`
	Services   []string          `json:"services"`
	Run        *Run              `json:"run,omitempty"`
	Evidence   []string          `json:"evidence"`
	Confidence string            `json:"confidence"`
}

type Run struct {
	Setup     []string     `json:"setup"`
	Processes []RunProcess `json:"processes"`
}

// RunConfig keeps the descriptive name used by API-facing callers while Run
// is the manifest model named in ADR-013.
type RunConfig = Run

type RunProcess struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Port    *int   `json:"port,omitempty"`
	Open    bool   `json:"open,omitempty"`
}

type Preset struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	Tools       map[string]string `json:"tools"`
	Services    []string          `json:"services"`
}

func Presets() []Preset {
	return []Preset{
		{ID: "python", DisplayName: "Python", Description: "Current stable Python runtime", Tools: map[string]string{"python": "3.14.7"}},
		{ID: "node-lts", DisplayName: "Node.js LTS", Description: "Current Node.js long-term support runtime", Tools: map[string]string{"node": "24.21.0"}},
		{ID: "java-gradle", DisplayName: "Java 21 + Gradle", Description: "Mature Temurin 21 LTS with the Gradle build tool", Tools: map[string]string{"java": "temurin-21.0.12+8.0.LTS", "gradle": "9.7.1"}},
		{ID: "java-maven", DisplayName: "Java 21 + Maven", Description: "Mature Temurin 21 LTS with the Maven build tool", Tools: map[string]string{"java": "temurin-21.0.12+8.0.LTS", "maven": "3.9.16"}},
		{ID: "java25-gradle", DisplayName: "Java 25 + Gradle", Description: "Newer Temurin 25 LTS with the Gradle build tool", Tools: map[string]string{"java": "temurin-25.0.4+7.0.LTS", "gradle": "9.7.1"}},
		{ID: "java25-maven", DisplayName: "Java 25 + Maven", Description: "Newer Temurin 25 LTS with the Maven build tool", Tools: map[string]string{"java": "temurin-25.0.4+7.0.LTS", "maven": "3.9.16"}},
		{ID: "go", DisplayName: "Go", Description: "Current stable Go toolchain", Tools: map[string]string{"go": "1.27.1"}},
		{ID: "rust", DisplayName: "Rust", Description: "Current stable Rust toolchain", Tools: map[string]string{"rust": "1.98.1"}},
	}
}

func ValidateRequirement(tool, version string) (string, string, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	version = strings.TrimSpace(version)
	if _, ok := allowedTools[tool]; !ok {
		return "", "", fmt.Errorf("%w: tool %q is not supported", ErrInvalidManifest, tool)
	}
	if !exactVersion.MatchString(version) || strings.EqualFold(version, "latest") ||
		strings.EqualFold(version, "system") {
		return "", "", fmt.Errorf("%w: version %q is not explicit", ErrInvalidManifest, version)
	}
	return tool, version, nil
}

func NormalizeManifest(manifest Manifest) (Manifest, error) {
	if len(manifest.Tools) == 0 {
		return Manifest{}, fmt.Errorf("%w: at least one tool is required", ErrInvalidManifest)
	}
	normalized := Manifest{
		ProjectID: strings.TrimSpace(manifest.ProjectID), Status: StatusConfigured,
		Source: manifest.Source, Tools: make(map[string]string, len(manifest.Tools)),
		Services: make([]string, 0, len(manifest.Services)), ServicesRunnable: false,
		UpdatedAt: manifest.UpdatedAt.UTC(),
	}
	switch normalized.Source {
	case SourcePicker, SourceDetected, SourceAssistant, SourceRuntime:
	default:
		return Manifest{}, fmt.Errorf("%w: source is not supported", ErrInvalidManifest)
	}
	for tool, version := range manifest.Tools {
		name, exact, err := ValidateRequirement(tool, version)
		if err != nil {
			return Manifest{}, err
		}
		normalized.Tools[name] = exact
	}
	seen := make(map[string]struct{}, len(manifest.Services))
	for _, configured := range manifest.Services {
		service := strings.ToLower(strings.TrimSpace(configured))
		if !safeService.MatchString(service) {
			return Manifest{}, fmt.Errorf("%w: service %q is invalid", ErrInvalidManifest, configured)
		}
		if _, exists := seen[service]; exists {
			continue
		}
		seen[service] = struct{}{}
		normalized.Services = append(normalized.Services, service)
	}
	sort.Strings(normalized.Services)
	if manifest.Run != nil {
		run, err := normalizeRun(*manifest.Run)
		if err != nil {
			return Manifest{}, err
		}
		normalized.Run = &run
	}
	return normalized, nil
}

func normalizeRun(run Run) (Run, error) {
	if len(run.Processes) == 0 {
		return Run{}, fmt.Errorf("%w: run requires at least one process", ErrInvalidManifest)
	}
	normalized := Run{
		Setup: make([]string, 0, len(run.Setup)), Processes: make([]RunProcess, 0, len(run.Processes)),
	}
	for _, configured := range run.Setup {
		command := strings.TrimSpace(configured)
		if command == "" || strings.ContainsRune(command, '\x00') {
			return Run{}, fmt.Errorf("%w: run setup command is invalid", ErrInvalidManifest)
		}
		normalized.Setup = append(normalized.Setup, command)
	}
	seenNames := make(map[string]struct{}, len(run.Processes))
	seenPorts := make(map[int]struct{}, len(run.Processes))
	openCount := 0
	for _, configured := range run.Processes {
		process := RunProcess{Name: strings.TrimSpace(configured.Name), Command: strings.TrimSpace(configured.Command), Open: configured.Open}
		if !safeProcess.MatchString(process.Name) {
			return Run{}, fmt.Errorf("%w: run process name %q is invalid", ErrInvalidManifest, configured.Name)
		}
		if _, exists := seenNames[process.Name]; exists {
			return Run{}, fmt.Errorf("%w: run process name %q is duplicated", ErrInvalidManifest, process.Name)
		}
		seenNames[process.Name] = struct{}{}
		if process.Command == "" || strings.ContainsRune(process.Command, '\x00') {
			return Run{}, fmt.Errorf("%w: run process %q has an invalid command", ErrInvalidManifest, process.Name)
		}
		if configured.Port != nil {
			port := *configured.Port
			if port < 1 || port > 65535 {
				return Run{}, fmt.Errorf("%w: run process %q has an invalid port", ErrInvalidManifest, process.Name)
			}
			if _, exists := seenPorts[port]; exists {
				return Run{}, fmt.Errorf("%w: run port %d is duplicated", ErrInvalidManifest, port)
			}
			seenPorts[port] = struct{}{}
			process.Port = &port
		}
		if process.Open {
			openCount++
			if process.Port == nil {
				return Run{}, fmt.Errorf("%w: open run process %q requires a port", ErrInvalidManifest, process.Name)
			}
		}
		normalized.Processes = append(normalized.Processes, process)
	}
	if openCount > 1 {
		return Run{}, fmt.Errorf("%w: at most one run process may open", ErrInvalidManifest)
	}
	return normalized, nil
}
