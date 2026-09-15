package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	toolchainconfig "github.com/EinarLogiOskars/commitarium/internal/toolchain"
)

const installTimeout = 30 * time.Minute

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "commitarium-toolchain:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) != 2 || arguments[0] != "require" {
		return errors.New("usage: commitarium-toolchain require <tool>@<exact-version>")
	}
	tool, version, err := parseRequirement(arguments[1])
	if err != nil {
		return err
	}
	configPath := strings.TrimSpace(os.Getenv("COMMITARIUM_TOOLCHAIN_CONFIG"))
	if !filepath.IsAbs(configPath) || filepath.Base(configPath) != "mise.toml" {
		return errors.New("the worker did not provide a safe project toolchain config")
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	lock, err := os.OpenFile(configPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open config lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	tools, err := readGeneratedConfig(configPath)
	if err != nil {
		return err
	}
	tools[tool] = version
	if err := writeGeneratedConfig(configPath, tools); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "mise", "install", "--yes")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("tool installation exceeded 30 minutes")
		}
		return fmt.Errorf("install %s@%s: %w", tool, version, err)
	}
	return nil
}

func parseRequirement(value string) (string, string, error) {
	tool, version, found := strings.Cut(strings.TrimSpace(value), "@")
	if !found || tool == "" || version == "" || strings.Contains(version, "@") {
		return "", "", errors.New("requirement must be <tool>@<exact-version>")
	}
	return toolchainconfig.ValidateRequirement(tool, version)
}

func readGeneratedConfig(path string) (map[string]string, error) {
	return toolchainconfig.ReadGeneratedConfig(path)
}

func writeGeneratedConfig(path string, tools map[string]string) error {
	return toolchainconfig.WriteGeneratedConfig(path, tools)
}
