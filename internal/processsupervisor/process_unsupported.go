//go:build !darwin && !linux

package processsupervisor

import (
	"os"
	"os/exec"
)

type processSignal int

const (
	terminationSignal processSignal = iota
	forceStopSignal
)

func prepareCommand(*exec.Cmd) error {
	return ErrUnsupportedPlatform
}

func signalProcessGroup(int, processSignal) error {
	return ErrUnsupportedPlatform
}

func processExitSignal(*os.ProcessState) string {
	return ""
}
