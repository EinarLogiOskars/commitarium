package toolchain

import (
	"bufio"
	"bytes"
	"strings"
)

// DetectToolsFromMise treats repository mise.toml as inert text. It extracts
// only plain string entries from [tools] and ignores every other section.
func DetectToolsFromMise(contents []byte) map[string]string {
	tools := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	inTools := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[tools]" {
			inTools = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			inTools = false
			continue
		}
		if !inTools || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, raw, found := strings.Cut(line, "=")
		raw = strings.TrimSpace(raw)
		if !found || len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			continue
		}
		tool, version, err := ValidateRequirement(strings.TrimSpace(name), raw[1:len(raw)-1])
		if err == nil {
			tools[tool] = version
		}
	}
	return tools
}

func DetectToolsFromToolVersions(contents []byte) map[string]string {
	tools := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		tool, version, err := ValidateRequirement(fields[0], fields[1])
		if err == nil {
			tools[tool] = version
		}
	}
	return tools
}
