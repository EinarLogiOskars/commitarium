package claudeadapter

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// aliasProbeTimeout bounds one alias probe; Claude Code reports its model at
// start-up, long before a turn would finish.
const aliasProbeTimeout = 30 * time.Second

// Efforts are the reasoning efforts Claude Code's --effort accepts.
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// ModelAliases are the Claude Code model aliases that name the newest model
// of each family.
var ModelAliases = []string{"opus", "sonnet", "haiku"}

// ResolveModelAliases asks Claude Code which exact model each alias stands for.
// Claude Code has no model listing; it names the resolved model in the
// start-up event of a print-mode run, which is stopped as soon as that event
// arrives. Aliases that cannot be resolved are skipped; an error means none
// could be.
func (adapter *Adapter) ResolveModelAliases(
	ctx context.Context,
	workingDirectory string,
	environment []string,
	aliases []string,
) ([]string, error) {
	resolved := make([]string, 0, len(aliases))
	var failures []error
	for _, alias := range aliases {
		model, err := adapter.resolveModelAlias(ctx, workingDirectory, environment, alias)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", alias, err))
			continue
		}
		resolved = append(resolved, model)
	}
	if len(resolved) == 0 && len(failures) > 0 {
		return nil, errors.Join(failures...)
	}
	return resolved, nil
}

func (adapter *Adapter) resolveModelAlias(
	ctx context.Context,
	workingDirectory string,
	environment []string,
	alias string,
) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, aliasProbeTimeout)
	defer cancel()
	command := exec.CommandContext(probeCtx, adapter.executable,
		"-p", "Reply with ok.", "--model", alias,
		"--output-format", "stream-json", "--verbose", "--max-turns", "1",
	)
	command.Dir = workingDirectory
	command.Env = append([]string(nil), environment...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("start Claude Code: %w", err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	return startupModel(stdout)
}

// startupModel reads Claude Code's stream until its start-up event and returns
// the model it names.
func startupModel(stream interface{ Read([]byte) (int, error) }) (string, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Model   string `json:"model"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if event.Type == "system" && event.Subtype == "init" {
			model := strings.TrimSpace(event.Model)
			if model == "" {
				return "", errors.New("the start-up event names no model")
			}
			return model, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("Claude Code ended without a start-up event")
}
