// Package processsupervisor starts and owns provider CLI process trees inside
// a worker. It deliberately does not understand any provider's command-line
// arguments or output format.
package processsupervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultOutputBuffer = 64
	outputReadBytes     = 32 * 1024
)

var (
	ErrInvalidStartRequest = errors.New("invalid process start request")
	ErrUnsupportedPlatform = errors.New("process supervision is unsupported on this platform")
	ErrOutputBackpressure  = errors.New("process output consumer fell behind")
	ErrAttemptExists       = errors.New("process attempt ID was already used")
	attemptIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Output is one chunk observed from a child process. Sequence records the
// order in which the supervisor received chunks from the two operating-system
// pipes; the OS cannot provide a stronger global ordering between those pipes.
type Output struct {
	Sequence int64
	Stream   Stream
	Data     []byte
}

type StartRequest struct {
	AttemptID   string
	Executable  string
	Arguments   []string
	Directory   string
	Environment []string
}

func (request StartRequest) Validate() error {
	if !attemptIDPattern.MatchString(request.AttemptID) {
		return fmt.Errorf("%w: attempt ID is invalid", ErrInvalidStartRequest)
	}
	if strings.TrimSpace(request.Executable) == "" || strings.ContainsRune(request.Executable, '\x00') {
		return fmt.Errorf("%w: executable is required and cannot contain NUL", ErrInvalidStartRequest)
	}
	if strings.ContainsRune(request.Directory, '\x00') {
		return fmt.Errorf("%w: directory cannot contain NUL", ErrInvalidStartRequest)
	}
	for _, argument := range request.Arguments {
		if strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("%w: arguments cannot contain NUL", ErrInvalidStartRequest)
		}
	}
	for _, variable := range request.Environment {
		name, _, found := strings.Cut(variable, "=")
		if !found || name == "" || strings.ContainsRune(variable, '\x00') {
			return fmt.Errorf("%w: environment entries must be NAME=VALUE pairs without NUL", ErrInvalidStartRequest)
		}
	}
	return nil
}

type Result struct {
	AttemptID string
	ExitCode  int
	Signal    string
	StartedAt time.Time
	EndedAt   time.Time
}

type Supervisor struct {
	mu       sync.Mutex
	attempts map[string]struct{}
}

// New returns a supervisor that remembers every successfully started attempt
// ID for its lifetime. This small in-memory fence stores no arguments,
// environment values, or other configuration that might contain secrets.
func New() *Supervisor {
	return &Supervisor{attempts: make(map[string]struct{})}
}

// Start uses ctx only while starting the process. Canceling an HTTP request
// after Start returns must not kill a long-running agent, so process lifetime is
// controlled explicitly through Terminate and ForceStop instead.
func (supervisor *Supervisor) Start(
	ctx context.Context,
	request StartRequest,
) (*Process, error) {
	if supervisor == nil {
		return nil, fmt.Errorf("%w: supervisor is required", ErrInvalidStartRequest)
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalidStartRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}

	command := exec.Command(request.Executable, append([]string(nil), request.Arguments...)...)
	command.Dir = request.Directory
	if request.Environment != nil {
		command.Env = append([]string(nil), request.Environment...)
	}
	if err := prepareCommand(command); err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open child stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("open child stderr: %w", err)
	}
	if err := supervisor.reserve(request.AttemptID); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		supervisor.release(request.AttemptID)
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	if err := command.Start(); err != nil {
		supervisor.release(request.AttemptID)
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("start child process: %w", err)
	}

	process := &Process{
		attemptID: request.AttemptID,
		command:   command,
		startedAt: time.Now().UTC(),
		output:    make(chan Output, defaultOutputBuffer),
		done:      make(chan struct{}),
	}
	process.collect(stdout, stderr)
	return process, nil
}

func (supervisor *Supervisor) reserve(attemptID string) error {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.attempts == nil {
		supervisor.attempts = make(map[string]struct{})
	}
	if _, exists := supervisor.attempts[attemptID]; exists {
		return fmt.Errorf("%w: %s", ErrAttemptExists, attemptID)
	}
	supervisor.attempts[attemptID] = struct{}{}
	return nil
}

func (supervisor *Supervisor) release(attemptID string) {
	// Reservations are released only when no process was started. A successful
	// start permanently consumes its attempt ID in this supervisor instance.
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	delete(supervisor.attempts, attemptID)
}

type Process struct {
	attemptID string
	command   *exec.Cmd
	startedAt time.Time
	output    chan Output
	done      chan struct{}

	mu      sync.Mutex
	exited  bool
	result  Result
	waitErr error
}

func (process *Process) AttemptID() string {
	if process == nil {
		return ""
	}
	return process.attemptID
}

// Output must be consumed while the process runs. If its bounded buffer fills,
// the supervisor force-stops the child tree and Wait returns
// ErrOutputBackpressure instead of allocating unbounded worker memory or
// becoming unable to reap the child.
func (process *Process) Output() <-chan Output {
	if process == nil {
		return nil
	}
	return process.output
}

func (process *Process) Wait(ctx context.Context) (Result, error) {
	if process == nil {
		return Result{}, errors.New("process is required")
	}
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	select {
	case <-process.done:
		process.mu.Lock()
		defer process.mu.Unlock()
		return process.result, process.waitErr
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Terminate sends the operating system's gentle termination signal to the
// exact process group and waits. A context deadline lets the caller decide when
// to escalate to ForceStop.
func (process *Process) Terminate(ctx context.Context) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if err := process.signal(terminationSignal); err != nil {
		return Result{}, err
	}
	return process.Wait(ctx)
}

// ForceStop sends the operating system's unignorable kill signal to the exact
// process group and waits for the supervised parent to be reaped.
func (process *Process) ForceStop(ctx context.Context) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if err := process.signal(forceStopSignal); err != nil {
		return Result{}, err
	}
	return process.Wait(ctx)
}

func (process *Process) signal(kind processSignal) error {
	if process == nil {
		return errors.New("process is required")
	}
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.exited {
		return nil
	}
	if err := signalProcessGroup(process.command.Process.Pid, kind); err != nil {
		return fmt.Errorf("signal child process group: %w", err)
	}
	return nil
}

type rawOutput struct {
	stream Stream
	data   []byte
}

func (process *Process) collect(stdout io.ReadCloser, stderr io.ReadCloser) {
	raw := make(chan rawOutput, 16)
	collectionErrors := make(chan error, 4)
	var readers sync.WaitGroup
	readers.Add(2)
	go readOutput(&readers, stdout, StreamStdout, raw, collectionErrors)
	go readOutput(&readers, stderr, StreamStderr, raw, collectionErrors)

	sequenced := make(chan struct{})
	go func() {
		defer close(sequenced)
		defer close(process.output)
		var sequence int64
		discarding := false
		for chunk := range raw {
			if discarding {
				continue
			}
			sequence++
			output := Output{
				Sequence: sequence,
				Stream:   chunk.stream,
				Data:     chunk.data,
			}
			select {
			case process.output <- output:
			default:
				discarding = true
				collectionErrors <- ErrOutputBackpressure
				if err := process.signal(forceStopSignal); err != nil {
					collectionErrors <- fmt.Errorf("force-stop after output backpressure: %w", err)
				}
			}
		}
	}()

	go func() {
		// StdoutPipe and StderrPipe require their reads to finish before Wait;
		// otherwise Wait may close a pipe while its reader is still draining the
		// final bytes. EOF still arrives when the child closes its descriptors.
		readers.Wait()
		close(raw)
		<-sequenced
		waitErr := process.command.Wait()
		endedAt := time.Now().UTC()

		process.mu.Lock()
		process.exited = true
		process.mu.Unlock()

		close(collectionErrors)

		var outputErrors []error
		for err := range collectionErrors {
			outputErrors = append(outputErrors, err)
		}
		if waitErr != nil {
			var exitError *exec.ExitError
			if !errors.As(waitErr, &exitError) {
				outputErrors = append(outputErrors, fmt.Errorf("wait for child process: %w", waitErr))
			}
		}
		result := Result{
			AttemptID: process.attemptID,
			ExitCode:  process.command.ProcessState.ExitCode(),
			Signal:    processExitSignal(process.command.ProcessState),
			StartedAt: process.startedAt,
			EndedAt:   endedAt,
		}
		process.mu.Lock()
		process.result = result
		process.waitErr = errors.Join(outputErrors...)
		process.mu.Unlock()
		close(process.done)
	}()
}

func readOutput(
	readers *sync.WaitGroup,
	pipe io.ReadCloser,
	stream Stream,
	raw chan<- rawOutput,
	readErrors chan<- error,
) {
	defer readers.Done()
	defer pipe.Close()
	buffer := make([]byte, outputReadBytes)
	for {
		read, err := pipe.Read(buffer)
		if read > 0 {
			data := append([]byte(nil), buffer[:read]...)
			raw <- rawOutput{stream: stream, data: data}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErrors <- fmt.Errorf("read child %s: %w", stream, err)
			}
			return
		}
	}
}
