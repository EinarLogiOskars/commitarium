// Package redact hides what looks like a credential in text Commitarium shows
// or hands back to an agent. The worker redacts the secrets it knows from
// agents' activity; the coordinator does not know them, so it redacts by
// shape.
package redact

import "regexp"

// secretPatterns find what looks like a credential. Commit IDs stay: agents
// need them.
var secretPatterns = []*regexp.Regexp{
	// A URL's user and password.
	regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+(@)`),
	// An authorization header, or a named secret's value.
	regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|token|basic)\s+|\b(?:token|password|passwd|secret|api[_-]?key|access[_-]?token)\s*[=:]\s*)[^\s'"]+`),
	// Provider tokens by their prefixes.
	regexp.MustCompile(`()\b(?:ghp|gho|ghs|ghu|github_pat|glpat|sk|xox[abp])[-_][A-Za-z0-9_-]{16,}`),
}

// Secrets replaces what looks like a credential with [REDACTED], as the
// worker does.
func Secrets(text string) string {
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, "${1}[REDACTED]${2}")
	}
	return text
}
