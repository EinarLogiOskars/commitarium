package codexadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EinarLogiOskars/commitarium/internal/processsupervisor"
)

const (
	maxProtocolLineBytes = 8 * 1024 * 1024
	protocolStopTimeout  = 5 * time.Second
)

var ErrProtocol = errors.New("invalid Codex app-server protocol")

type protocolError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (problem *protocolError) Error() string {
	if problem == nil {
		return ""
	}
	return fmt.Sprintf("Codex app-server error %d: %s", problem.Code, problem.Message)
}

type protocolMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *protocolError  `json:"error,omitempty"`
}

type protocolResponse struct {
	result json.RawMessage
	issue  *protocolError
}

// protocolClient owns the JSONL request/response connection over one
// supervised app-server process. Provider diagnostics on stderr are drained by
// the supervisor but never interpreted as protocol or published as activity.
type protocolClient struct {
	process *processsupervisor.Process

	mu        sync.Mutex
	nextID    int64
	pending   map[int64]chan protocolResponse
	readErr   error
	exit      processsupervisor.Result
	completed bool

	notifications chan protocolMessage
	done          chan struct{}
}

func newProtocolClient(process *processsupervisor.Process) *protocolClient {
	client := &protocolClient{
		process:       process,
		pending:       make(map[int64]chan protocolResponse),
		notifications: make(chan protocolMessage, 128),
		done:          make(chan struct{}),
	}
	go client.readLoop()
	return client
}

func (client *protocolClient) request(
	ctx context.Context,
	method string,
	params any,
	result any,
) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client.mu.Lock()
	if client.completed {
		err := client.connectionErrorLocked()
		client.mu.Unlock()
		return err
	}
	client.nextID++
	id := client.nextID
	response := make(chan protocolResponse, 1)
	client.pending[id] = response
	client.mu.Unlock()

	message := struct {
		Method string `json:"method"`
		ID     int64  `json:"id"`
		Params any    `json:"params"`
	}{Method: method, ID: id, Params: params}
	if err := client.write(ctx, message); err != nil {
		client.removePending(id)
		return err
	}

	select {
	case received := <-response:
		if received.issue != nil {
			return received.issue
		}
		if result == nil {
			return nil
		}
		if len(received.result) == 0 {
			return fmt.Errorf("%w: response to %s omitted its result", ErrProtocol, method)
		}
		if err := json.Unmarshal(received.result, result); err != nil {
			return fmt.Errorf("%w: decode %s result: %v", ErrProtocol, method, err)
		}
		return nil
	case <-client.done:
		client.removePending(id)
		return client.connectionError()
	case <-ctx.Done():
		client.removePending(id)
		return ctx.Err()
	}
}

func (client *protocolClient) notify(ctx context.Context, method string, params any) error {
	return client.write(ctx, struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{Method: method, Params: params})
}

func (client *protocolClient) write(ctx context.Context, message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode Codex app-server request: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := client.process.WriteInput(ctx, encoded); err != nil {
		return fmt.Errorf("write Codex app-server request: %w", err)
	}
	return nil
}

func (client *protocolClient) removePending(id int64) {
	client.mu.Lock()
	defer client.mu.Unlock()
	delete(client.pending, id)
}

func (client *protocolClient) readLoop() {
	var stdout []byte
	for output := range client.process.Output() {
		if output.Stream != processsupervisor.StreamStdout || client.hasReadError() {
			continue
		}
		stdout = append(stdout, output.Data...)
		for {
			newline := bytes.IndexByte(stdout, '\n')
			if newline < 0 {
				break
			}
			line := bytes.TrimSpace(stdout[:newline])
			stdout = stdout[newline+1:]
			if len(line) == 0 {
				continue
			}
			if len(line) > maxProtocolLineBytes {
				client.failProtocol("message exceeds the size limit")
				break
			}
			if err := client.dispatch(line); err != nil {
				client.fail(err)
				break
			}
		}
		if len(stdout) > maxProtocolLineBytes {
			client.failProtocol("message exceeds the size limit")
		}
	}
	if !client.hasReadError() && len(bytes.TrimSpace(stdout)) > 0 {
		if len(stdout) > maxProtocolLineBytes {
			client.failProtocol("message exceeds the size limit")
		} else if err := client.dispatch(bytes.TrimSpace(stdout)); err != nil {
			client.fail(err)
		}
	}

	exit, waitErr := client.process.Wait(context.Background())
	client.mu.Lock()
	client.exit = exit
	if client.readErr == nil && waitErr != nil {
		client.readErr = fmt.Errorf("wait for Codex app-server process: %w", waitErr)
	}
	client.completed = true
	client.mu.Unlock()
	close(client.notifications)
	close(client.done)
}

func (client *protocolClient) dispatch(line []byte) error {
	var message protocolMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return fmt.Errorf("%w: decode JSONL message", ErrProtocol)
	}
	if len(message.ID) == 0 {
		if strings.TrimSpace(message.Method) == "" {
			return fmt.Errorf("%w: notification omitted its method", ErrProtocol)
		}
		select {
		case client.notifications <- message:
			return nil
		default:
			return fmt.Errorf("%w: notification consumer fell behind", ErrProtocol)
		}
	}
	if message.Method != "" {
		return fmt.Errorf("%w: server request %q is not supported by this adapter slice", ErrProtocol, message.Method)
	}
	var id int64
	if err := json.Unmarshal(message.ID, &id); err != nil || id < 1 {
		return fmt.Errorf("%w: response ID is not a positive integer", ErrProtocol)
	}
	if (len(message.Result) == 0) == (message.Error == nil) {
		return fmt.Errorf("%w: response must contain exactly one result or error", ErrProtocol)
	}
	client.mu.Lock()
	response, waiting := client.pending[id]
	if waiting {
		delete(client.pending, id)
	}
	client.mu.Unlock()
	if !waiting {
		// A caller may have timed out after the request was written. The late
		// response cannot safely be associated with another operation.
		return nil
	}
	response <- protocolResponse{result: message.Result, issue: message.Error}
	return nil
}

func (client *protocolClient) failProtocol(reason string) {
	client.fail(fmt.Errorf("%w: %s", ErrProtocol, reason))
}

func (client *protocolClient) fail(err error) {
	client.mu.Lock()
	first := client.readErr == nil
	if first {
		client.readErr = err
	}
	client.mu.Unlock()
	if !first {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), protocolStopTimeout)
		defer cancel()
		_, _ = client.process.ForceStop(ctx)
	}()
}

func (client *protocolClient) hasReadError() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.readErr != nil
}

func (client *protocolClient) connectionError() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.connectionErrorLocked()
}

func (client *protocolClient) connectionErrorLocked() error {
	if client.readErr != nil {
		return client.readErr
	}
	return fmt.Errorf(
		"Codex app-server exited before completing the turn (exit code %d, signal %q)",
		client.exit.ExitCode,
		client.exit.Signal,
	)
}
