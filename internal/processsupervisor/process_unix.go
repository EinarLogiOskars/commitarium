//go:build darwin || linux

package processsupervisor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processSignal int

const (
	terminationSignal processSignal = iota
	forceStopSignal
)

func prepareCommand(command *exec.Cmd) error {
	// A separate process group lets termination reach helper processes spawned
	// by a provider CLI without touching the worker's own process group.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func signalProcessGroup(pid int, kind processSignal) error {
	signal := syscall.SIGTERM
	if kind == forceStopSignal {
		signal = syscall.SIGKILL
	}
	// A negative PID addresses the process group whose ID is the child PID.
	if err := syscall.Kill(-pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func processExitSignal(state *os.ProcessState) string {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
