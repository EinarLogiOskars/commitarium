package claudeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/worker"
)

// usageReadTimeout bounds one /usage read; it starts no model turn.
const usageReadTimeout = 45 * time.Second

// ReadUsage asks Claude Code for the account's usage limits with its /usage
// command, which runs no model and spends no tokens, and records them. It lets
// the Agents page show usage before the agent has worked since it started.
func (adapter *Adapter) ReadUsage(ctx context.Context, workingDirectory string, environment []string) error {
	readCtx, cancel := context.WithTimeout(ctx, usageReadTimeout)
	defer cancel()
	command := exec.CommandContext(readCtx, adapter.executable, "-p", "/usage", "--output-format", "json")
	command.Dir = workingDirectory
	command.Env = append([]string(nil), environment...)
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("read Claude usage: %w", err)
	}
	var result struct {
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("read Claude usage: %w", err)
	}
	if result.IsError {
		return errors.New("read Claude usage: Claude Code reported an error")
	}
	windows := parseUsage(result.Result, time.Now().UTC())
	if len(windows) == 0 {
		return errors.New("read Claude usage: no usage limits in the answer")
	}
	Usage.Record(windows)
	return nil
}

// usageLine matches one limit of /usage, such as
// "Current week (all models): 30% used · resets Nov 9, 5:59am (UTC)".
var usageLine = regexp.MustCompile(
	`^Current (session|week(?: \(([^)]+)\))?): (\d+(?:\.\d+)?)% used(?: · resets (.+))?$`)

// parseUsage reads /usage's limit lines, named as the stream's rate-limit
// events name them, so either source updates the same bars.
func parseUsage(text string, now time.Time) []worker.UsageWindow {
	windows := make([]worker.UsageWindow, 0, 3)
	for _, line := range strings.Split(text, "\n") {
		match := usageLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		name := "5-hour"
		if match[1] != "session" {
			name = "Weekly"
			if scope := match[2]; scope != "" && scope != "all models" {
				name = "Weekly (" + scope + ")"
			}
		}
		used, err := strconv.ParseFloat(match[3], 64)
		if err != nil {
			continue
		}
		window := worker.UsageWindow{Name: name, UsedPercent: &used, Limited: used >= 100}
		if resets, ok := parseReset(match[4], now); ok {
			window.ResetsAt = &resets
		}
		windows = append(windows, window)
	}
	return windows
}

// parseReset reads a reset such as "Nov 9, 5:59am (UTC)" or "Nov 9, 6am (UTC)".
// The year is the one that puts it closest after now.
func parseReset(text string, now time.Time) (time.Time, bool) {
	text = strings.TrimSpace(text)
	location := time.UTC
	if open := strings.LastIndex(text, " ("); open > 0 && strings.HasSuffix(text, ")") {
		if loaded, err := time.LoadLocation(text[open+2 : len(text)-1]); err == nil {
			location = loaded
		}
		text = text[:open]
	}
	for _, layout := range []string{"Jan 2, 3:04pm", "Jan 2, 3pm", "Jan 2 3:04pm", "Jan 2 3pm"} {
		parsed, err := time.ParseInLocation(layout, text, location)
		if err != nil {
			continue
		}
		at := time.Date(now.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location)
		if at.Before(now.Add(-24 * time.Hour)) {
			at = at.AddDate(1, 0, 0)
		}
		return at.UTC(), true
	}
	return time.Time{}, false
}
