//go:build darwin || linux

package processsupervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const helperEnvironment = "COMMITARIUM_PROCESS_SUPERVISOR_HELPER=1"

func TestStartValidatesRequestAndContext(t *testing.T) {
	supervisor := New()
	valid := helperRequest("att_valid", "exit", "0")
	tests := []StartRequest{
		{},
		{AttemptID: "bad attempt", Executable: os.Args[0]},
		{AttemptID: "att_valid"},
		{AttemptID: "att_valid", Executable: "bad\x00path"},
		{AttemptID: "att_valid", Executable: os.Args[0], Arguments: []string{"bad\x00argument"}},
		{AttemptID: "att_valid", Executable: os.Args[0], Directory: "bad\x00directory"},
		{AttemptID: "att_valid", Executable: os.Args[0], Environment: []string{"MISSING_VALUE"}},
	}
	for index, request := range tests {
		if _, err := supervisor.Start(t.Context(), request); !errors.Is(err, ErrInvalidStartRequest) {
			t.Errorf("case %d error = %v, want ErrInvalidStartRequest", index, err)
		}
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := supervisor.Start(canceled, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start error = %v, want context.Canceled", err)
	}
	if _, err := (*Supervisor)(nil).Start(t.Context(), valid); !errors.Is(err, ErrInvalidStartRequest) {
		t.Fatalf("nil supervisor error = %v, want ErrInvalidStartRequest", err)
	}
}

func TestProcessCapturesBothStreamsAndReportsNonzeroExit(t *testing.T) {
	process := startHelper(t, helperRequest("att_output", "output-and-exit", "7"))
	outputs := collectOutputs(process)
	result, err := process.Wait(timeoutContext(t, 2*time.Second))
	if err != nil {
		t.Fatalf("wait for output helper: %v", err)
	}
	if result.AttemptID != "att_output" || result.ExitCode != 7 || result.Signal != "" {
		t.Fatalf("output helper result = %+v", result)
	}
	if result.StartedAt.IsZero() || result.EndedAt.Before(result.StartedAt) {
		t.Fatalf("invalid process timestamps: %+v", result)
	}
	observed := <-outputs
	if len(observed) < 2 {
		t.Fatalf("output count = %d, want at least 2: %+v", len(observed), observed)
	}
	for index, output := range observed {
		if output.Sequence != int64(index+1) {
			t.Fatalf("output %d sequence = %d", index, output.Sequence)
		}
	}
	combined := map[Stream]string{}
	for _, output := range observed {
		combined[output.Stream] += string(output.Data)
	}
	if combined[StreamStdout] != "stdout-record\n" || combined[StreamStderr] != "stderr-record\n" {
		t.Fatalf("captured output = %+v", combined)
	}
}

func TestStartPassesWorkingDirectoryAndExplicitEnvironment(t *testing.T) {
	directory := t.TempDir()
	request := helperRequest("att_configuration", "report-configuration")
	request.Directory = directory
	request.Environment = []string{
		helperEnvironment,
		"COMMITARIUM_PROCESS_SUPERVISOR_VALUE=expected",
	}
	process := startHelper(t, request)
	outputs := collectOutputs(process)
	result, err := process.Wait(timeoutContext(t, 2*time.Second))
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("configured helper result=%+v error=%v", result, err)
	}
	var stdout strings.Builder
	for _, output := range <-outputs {
		if output.Stream == StreamStdout {
			stdout.Write(output.Data)
		}
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || lines[1] != "expected" {
		t.Fatalf("configured helper output = %q", stdout.String())
	}
	wantDirectory, err := os.Stat(directory)
	if err != nil {
		t.Fatalf("stat requested directory: %v", err)
	}
	actualDirectory, err := os.Stat(lines[0])
	if err != nil {
		t.Fatalf("stat reported directory: %v", err)
	}
	if !os.SameFile(wantDirectory, actualDirectory) {
		t.Fatalf("reported directory = %q, want same file as %q", lines[0], directory)
	}
}

func TestProcessWritesSerializedInputAndClosesIt(t *testing.T) {
	process := startHelper(t, helperRequest("att_input", "copy-input"))
	outputs := collectOutputs(process)

	if err := process.WriteInput(timeoutContext(t, time.Second), []byte("first\n")); err != nil {
		t.Fatalf("write first input: %v", err)
	}
	if err := process.WriteInput(timeoutContext(t, time.Second), []byte("second\n")); err != nil {
		t.Fatalf("write second input: %v", err)
	}
	if err := process.CloseInput(); err != nil {
		t.Fatalf("close input: %v", err)
	}
	if err := process.CloseInput(); err != nil {
		t.Fatalf("repeat close input: %v", err)
	}
	if err := process.WriteInput(timeoutContext(t, time.Second), []byte("late\n")); !errors.Is(err, ErrInputClosed) {
		t.Fatalf("write after close error = %v, want ErrInputClosed", err)
	}

	result, err := process.Wait(timeoutContext(t, 2*time.Second))
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("copy-input result=%+v error=%v", result, err)
	}
	var stdout strings.Builder
	for _, output := range <-outputs {
		if output.Stream == StreamStdout {
			stdout.Write(output.Data)
		}
	}
	if stdout.String() != "first\nsecond\n" {
		t.Fatalf("copied input = %q", stdout.String())
	}
}

func TestProcessWriteInputHonorsCanceledContext(t *testing.T) {
	process := startHelper(t, helperRequest("att_input_context", "delay-exit", "100ms"))
	go func() {
		for range process.Output() {
		}
	}()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := process.WriteInput(canceled, []byte("ignored")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled input error = %v, want context.Canceled", err)
	}
}

func TestProcessWriteInputClosesBlockedPipeOnDeadline(t *testing.T) {
	process := startHelper(t, helperRequest("att_input_deadline", "delay-exit", "250ms"))
	go func() {
		for range process.Output() {
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if err := process.WriteInput(ctx, make([]byte, 4*1024*1024)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked input error = %v, want context deadline", err)
	}
	if err := process.WriteInput(timeoutContext(t, time.Second), []byte("late")); !errors.Is(err, ErrInputClosed) {
		t.Fatalf("input after canceled write error = %v, want ErrInputClosed", err)
	}
}

func TestStartContextDoesNotOwnProcessLifetime(t *testing.T) {
	startContext, cancel := context.WithCancel(t.Context())
	process, err := New().Start(startContext, helperRequest("att_context", "delay-exit", "100ms"))
	if err != nil {
		t.Fatalf("start delayed helper: %v", err)
	}
	cleanupProcess(t, process)
	cancel()
	go func() {
		for range process.Output() {
		}
	}()
	result, err := process.Wait(timeoutContext(t, 2*time.Second))
	if err != nil {
		t.Fatalf("wait after start context cancellation: %v", err)
	}
	if result.ExitCode != 0 || result.Signal != "" {
		t.Fatalf("delayed helper result = %+v", result)
	}
}

func TestSupervisorNeverReusesAttemptID(t *testing.T) {
	supervisor := New()
	request := helperRequest("att_duplicate", "delay-exit", "100ms")
	first, err := supervisor.Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start first attempt: %v", err)
	}
	cleanupProcess(t, first)
	go func() {
		for range first.Output() {
		}
	}()
	if _, err := supervisor.Start(t.Context(), request); !errors.Is(err, ErrAttemptExists) {
		t.Fatalf("duplicate start error = %v, want ErrAttemptExists", err)
	}
	if _, err := first.Wait(timeoutContext(t, 2*time.Second)); err != nil {
		t.Fatalf("wait for first attempt: %v", err)
	}
	if _, err := supervisor.Start(t.Context(), request); !errors.Is(err, ErrAttemptExists) {
		t.Fatalf("completed attempt reuse error = %v, want ErrAttemptExists", err)
	}
	second, err := supervisor.Start(t.Context(), helperRequest("att_second", "exit", "0"))
	if err != nil {
		t.Fatalf("start distinct second attempt: %v", err)
	}
	cleanupProcess(t, second)
	go func() {
		for range second.Output() {
		}
	}()
	if result, err := second.Wait(timeoutContext(t, 2*time.Second)); err != nil || result.ExitCode != 0 {
		t.Fatalf("second attempt result=%+v error=%v", result, err)
	}
}

func TestFailedStartDoesNotConsumeAttemptID(t *testing.T) {
	supervisor := New()
	failed := StartRequest{
		AttemptID:  "att_retry_start",
		Executable: filepath.Join(t.TempDir(), "missing-executable"),
	}
	if _, err := supervisor.Start(t.Context(), failed); err == nil {
		t.Fatal("expected missing executable start to fail")
	}
	process, err := supervisor.Start(t.Context(), helperRequest("att_retry_start", "exit", "0"))
	if err != nil {
		t.Fatalf("retry after failed start: %v", err)
	}
	cleanupProcess(t, process)
	go func() {
		for range process.Output() {
		}
	}()
	if result, err := process.Wait(timeoutContext(t, 2*time.Second)); err != nil || result.ExitCode != 0 {
		t.Fatalf("retried start result=%+v error=%v", result, err)
	}
}

func TestOutputBackpressureStopsProcessWithoutBlockingWait(t *testing.T) {
	process := startHelper(t, helperRequest("att_backpressure", "flood-output"))
	result, err := process.Wait(timeoutContext(t, 3*time.Second))
	if !errors.Is(err, ErrOutputBackpressure) {
		t.Fatalf("backpressure wait error = %v, want ErrOutputBackpressure", err)
	}
	if result.AttemptID != "att_backpressure" || result.ExitCode != -1 || result.Signal == "" {
		t.Fatalf("backpressure result = %+v", result)
	}
}

func TestTerminateStopsExactProcessGroupGently(t *testing.T) {
	process := startHelper(t, helperRequest("att_terminate", "handle-terminate"))
	waitForReadyOutput(t, process)
	result, err := process.Terminate(timeoutContext(t, 2*time.Second))
	if err != nil {
		t.Fatalf("terminate helper: %v", err)
	}
	if result.ExitCode != 0 || result.Signal != "" {
		t.Fatalf("terminated helper result = %+v", result)
	}
	if repeated, err := process.Terminate(timeoutContext(t, time.Second)); err != nil || repeated != result {
		t.Fatalf("repeated terminate result=%+v error=%v, want %+v", repeated, err, result)
	}
}

func TestForceStopKillsDescendantsAfterGentleTimeout(t *testing.T) {
	directory := t.TempDir()
	childReady := filepath.Join(directory, "child-ready")
	childFinished := filepath.Join(directory, "child-finished")
	process := startHelper(t, helperRequest(
		"att_force",
		"parent-with-child",
		childReady,
		childFinished,
	))
	waitForReadyOutput(t, process)

	terminateContext, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	if _, err := process.Terminate(terminateContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gentle termination error = %v, want context deadline", err)
	}
	result, err := process.ForceStop(timeoutContext(t, 2*time.Second))
	if err != nil {
		t.Fatalf("force-stop process group: %v", err)
	}
	if result.ExitCode != -1 || result.Signal == "" {
		t.Fatalf("force-stopped helper result = %+v", result)
	}
	time.Sleep(900 * time.Millisecond)
	if _, err := os.Stat(childFinished); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived force-stop; marker error = %v", err)
	}
	if repeated, err := process.ForceStop(timeoutContext(t, time.Second)); err != nil || repeated != result {
		t.Fatalf("repeated force-stop result=%+v error=%v, want %+v", repeated, err, result)
	}
}

func TestProcessSupervisorHelper(t *testing.T) {
	if os.Getenv("COMMITARIUM_PROCESS_SUPERVISOR_HELPER") != "1" {
		return
	}
	arguments := helperArguments(os.Args)
	if len(arguments) == 0 {
		os.Exit(90)
	}
	switch arguments[0] {
	case "exit":
		var code int
		if _, err := fmt.Sscanf(arguments[1], "%d", &code); err != nil {
			os.Exit(91)
		}
		os.Exit(code)
	case "output-and-exit":
		fmt.Fprintln(os.Stdout, "stdout-record")
		time.Sleep(25 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "stderr-record")
		var code int
		if _, err := fmt.Sscanf(arguments[1], "%d", &code); err != nil {
			os.Exit(92)
		}
		os.Exit(code)
	case "delay-exit":
		delay, err := time.ParseDuration(arguments[1])
		if err != nil {
			os.Exit(93)
		}
		time.Sleep(delay)
		os.Exit(0)
	case "report-configuration":
		directory, err := os.Getwd()
		if err != nil {
			os.Exit(89)
		}
		fmt.Fprintln(os.Stdout, directory)
		fmt.Fprintln(os.Stdout, os.Getenv("COMMITARIUM_PROCESS_SUPERVISOR_VALUE"))
		os.Exit(0)
	case "copy-input":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(88)
		}
		os.Exit(0)
	case "flood-output":
		block := make([]byte, 4*1024*1024)
		if _, err := os.Stdout.Write(block); err != nil {
			os.Exit(99)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "handle-terminate":
		terminated := make(chan os.Signal, 1)
		signal.Notify(terminated, syscall.SIGTERM)
		fmt.Fprintln(os.Stdout, "ready")
		<-terminated
		os.Exit(0)
	case "parent-with-child":
		signal.Ignore(syscall.SIGTERM)
		child := exec.Command(
			os.Args[0],
			"-test.run=^TestProcessSupervisorHelper$",
			"--",
			"delayed-marker",
			arguments[1],
			arguments[2],
		)
		child.Env = os.Environ()
		if err := child.Start(); err != nil {
			os.Exit(94)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(arguments[1]); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(95)
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprintln(os.Stdout, "ready")
		for {
			time.Sleep(time.Hour)
		}
	case "delayed-marker":
		signal.Ignore(syscall.SIGTERM)
		if err := os.WriteFile(arguments[1], []byte("ready"), 0o600); err != nil {
			os.Exit(96)
		}
		time.Sleep(600 * time.Millisecond)
		if err := os.WriteFile(arguments[2], []byte("survived"), 0o600); err != nil {
			os.Exit(97)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(98)
	}
}

func helperRequest(attemptID string, arguments ...string) StartRequest {
	return StartRequest{
		AttemptID:  attemptID,
		Executable: os.Args[0],
		Arguments: append(
			[]string{"-test.run=^TestProcessSupervisorHelper$", "--"},
			arguments...,
		),
		Environment: append(os.Environ(), helperEnvironment),
	}
}

func helperArguments(arguments []string) []string {
	for index, argument := range arguments {
		if argument == "--" {
			return arguments[index+1:]
		}
	}
	return nil
}

func startHelper(t *testing.T, request StartRequest) *Process {
	t.Helper()
	process, err := New().Start(t.Context(), request)
	if err != nil {
		t.Fatalf("start process helper: %v", err)
	}
	if process.AttemptID() != request.AttemptID {
		t.Fatalf("process attempt ID = %q, want %q", process.AttemptID(), request.AttemptID)
	}
	cleanupProcess(t, process)
	return process
}

func cleanupProcess(t *testing.T, process *Process) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = process.ForceStop(ctx)
	})
}

func collectOutputs(process *Process) <-chan []Output {
	collected := make(chan []Output, 1)
	go func() {
		var outputs []Output
		for output := range process.Output() {
			outputs = append(outputs, output)
		}
		collected <- outputs
	}()
	return collected
}

func waitForReadyOutput(t *testing.T, process *Process) {
	t.Helper()
	select {
	case output, open := <-process.Output():
		if !open || output.Stream != StreamStdout || strings.TrimSpace(string(output.Data)) != "ready" {
			t.Fatalf("ready output = %+v, open=%t", output, open)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for helper readiness")
	}
}

func timeoutContext(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	t.Cleanup(cancel)
	return ctx
}
