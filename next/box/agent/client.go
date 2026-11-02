package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
)

const outgoingLimit = 64 << 20

type outgoing struct {
	method contract.Method
	body   []byte
	result chan response
}
type response struct {
	body []byte
	err  error
}

type apiError struct {
	status string
	code   contract.ErrorCode
}

func (e apiError) Error() string { return e.status }

// Client owns the box socket and a bounded outgoing buffer.
type Client struct {
	shellJobs func() []contract.ShellJobID
	http      *http.Client
	mu        sync.Mutex
	size      int
	space     chan struct{}
	queue     chan outgoing
}

func NewClient(socket string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Client{shellJobs: func() []contract.ShellJobID { return nil }, http: &http.Client{Transport: transport}, space: make(chan struct{}), queue: make(chan outgoing, 128)}
}

// Run sends queued requests in order. A disconnect does not end a running turn.
func (c *Client) Run(ctx context.Context) {
	for {
		select {
		case item := <-c.queue:
			delay := 500 * time.Millisecond
			for {
				body, err := c.post(ctx, item.method, item.body)
				var rejected apiError
				if err == nil || errors.As(err, &rejected) || ctx.Err() != nil {
					item.result <- response{body, err}
					c.mu.Lock()
					c.size -= len(item.body)
					close(c.space)
					c.space = make(chan struct{})
					c.mu.Unlock()
					break
				}
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
				}
				if delay < 10*time.Second {
					delay *= 2
					if delay > 10*time.Second {
						delay = 10 * time.Second
					}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) post(ctx context.Context, method contract.Method, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/"+string(method), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, outgoingLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var detail contract.Error
		_ = json.Unmarshal(data, &detail)
		return nil, apiError{status: fmt.Sprintf("box API %s: %s", method, resp.Status), code: detail.Code}
	}
	return data, nil
}

// forward sends an agent tool call once. It does not queue or retry a creation.
func (c *Client) forward(ctx context.Context, method contract.Method, body []byte) ([]byte, *contract.Error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/"+string(method), bytes.NewReader(body))
	if err != nil {
		return nil, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, outgoingLimit))
	if err != nil {
		return nil, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem contract.Error
		if err := json.Unmarshal(data, &problem); err != nil {
			return nil, &contract.Error{Code: contract.ErrUnavailable, Message: resp.Status}
		}
		return nil, &problem
	}
	return data, nil
}

// HostRun runs one command on a host and writes its output as the run ends. The engine answers only then, so the call stays
// open as long as the command does. A problem after the engine took the command is the job's failure, not a refusal.
func (c *Client) HostRun(ctx context.Context, r shell.Run, out io.Writer) (int, *contract.Error) {
	req := contract.HostexecRunRequest{Host: r.Host, Command: r.Command, Cwd: r.Cwd, Timeout: contract.DurationMS(r.Timeout) * 1000}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, &contract.Error{Code: contract.ErrInvalid, Message: err.Error()}
	}
	data, problem := c.forward(ctx, contract.MethodHostexecRun, body)
	if problem != nil {
		return 0, problem
	}
	var answer contract.HostexecRunResponse
	if err := json.Unmarshal(data, &answer); err != nil {
		fmt.Fprintln(out, "host run: invalid answer from the engine:", err)
		return 1, nil
	}
	result := answer.Operation.Result
	if result == nil {
		fmt.Fprintln(out, "host run: the call ended before the command; op.ran wakes the session with its result")
		return 1, nil
	}
	_, _ = io.WriteString(out, result.Output)
	if result.OutputFile != "" {
		if err := c.file(ctx, result.OutputFile, out); err != nil {
			fmt.Fprintln(out, "host run: the rest of the output could not be read:", err)
		}
	}
	if result.Truncated {
		fmt.Fprintln(out, "[output truncated at 8 MiB]")
	}
	if result.Error != "" {
		fmt.Fprintln(out, "host run:", result.Error)
		if result.ExitCode == 0 {
			return 1, nil
		}
	}
	return result.ExitCode, nil
}

func (c *Client) file(ctx context.Context, hash string, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://dyna/v1/files/"+hash, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	_, err = io.Copy(out, resp.Body)
	return err
}

func (c *Client) call(ctx context.Context, method contract.Method, body []byte) ([]byte, error) {
	if len(body) > outgoingLimit {
		return nil, fmt.Errorf("box API request exceeds outgoing buffer")
	}
	for {
		c.mu.Lock()
		if c.size+len(body) <= outgoingLimit {
			c.size += len(body)
			c.mu.Unlock()
			break
		}
		space := c.space
		c.mu.Unlock()
		select {
		case <-space:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	item := outgoing{method: method, body: body, result: make(chan response, 1)}
	select {
	case c.queue <- item:
	case <-ctx.Done():
		c.mu.Lock()
		c.size -= len(body)
		close(c.space)
		c.space = make(chan struct{})
		c.mu.Unlock()
		return nil, ctx.Err()
	}
	select {
	case r := <-item.result:
		return r.body, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) Hello(ctx context.Context) (contract.BoxHelloResponse, error) {
	body, err := json.Marshal(contract.BoxHelloRequest{})
	if err != nil {
		return contract.BoxHelloResponse{}, err
	}
	data, err := c.call(ctx, contract.MethodBoxHello, body)
	if err != nil {
		return contract.BoxHelloResponse{}, err
	}
	var result contract.BoxHelloResponse
	err = json.Unmarshal(data, &result)
	return result, err
}

func (c *Client) ArchiveDone(ctx context.Context, result contract.ArchiveDoneRequest) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, contract.MethodArchiveDone, body)
	return err
}

func (c *Client) ShellJobEnded(ctx context.Context, req contract.ShellJobEndedRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, contract.MethodShellJobEnded, body)
	return err
}

func (c *Client) ShellJobRunning(ctx context.Context, req contract.ShellJobRunningRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, contract.MethodShellJobRunning, body)
	return err
}

// Notice tells the session and the owner what this box could not set up.
func (c *Client) Notice(ctx context.Context, text string) error {
	body, err := json.Marshal(contract.BoxNoticeRequest{Text: text})
	if err != nil {
		return err
	}
	_, err = c.call(ctx, contract.MethodBoxNotice, body)
	return err
}

func (c *Client) DeliveryDone(ctx context.Context, id contract.DeliverySeq) error {
	body, err := json.Marshal(contract.DeliveryDoneRequest{Delivery: id})
	if err != nil {
		return err
	}
	_, err = c.call(ctx, contract.MethodDeliveryDone, body)
	return err
}

func (c *Client) TranscriptAppend(ctx context.Context, req contract.TranscriptAppendRequest) (contract.TranscriptAppendResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return contract.TranscriptAppendResponse{}, err
	}
	data, err := c.call(ctx, contract.MethodTranscriptAppend, body)
	if err != nil {
		return contract.TranscriptAppendResponse{}, err
	}
	var result contract.TranscriptAppendResponse
	err = json.Unmarshal(data, &result)
	return result, err
}

func (c *Client) TurnOutput(ctx context.Context, result contract.TurnEnded) error {
	jobs := c.shellJobs()
	body, err := json.Marshal(contract.TurnOutputRequest{ShellJobs: len(jobs), Jobs: jobs, Turn: result.Turn, Outcome: result.Outcome, Error: result.Error, Cause: result.Cause, Final: result.Final, EndPosts: result.EndPosts, Model: result.Usage.Model, Usage: result.Usage, Unread: result.Unread})
	if err != nil {
		return err
	}
	_, err = c.call(ctx, contract.MethodTurnOutput, body)
	return err
}

// Compacted answers a Compact through the queue, so it waits out an engine restart.
func (c *Client) Compacted(ctx context.Context, answer contract.BoxCompactedRequest) error {
	_, err := request[contract.BoxCompactedRequest, contract.BoxCompactedResponse](ctx, c, contract.MethodBoxCompacted, answer)
	return err
}

// request sends one typed call through the queue, so it waits out an engine restart.
func request[Req, Res any](ctx context.Context, c *Client, method contract.Method, req Req) (Res, error) {
	var result Res
	body, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	data, err := c.call(ctx, method, body)
	if err != nil {
		return result, err
	}
	return result, json.Unmarshal(data, &result)
}

func (c *Client) WorkflowPin(ctx context.Context, commit string) error {
	_, err := request[contract.WorkflowPinRequest, contract.WorkflowPinResponse](ctx, c, contract.MethodWorkflowPin, contract.WorkflowPinRequest{Commit: commit})
	return err
}

func (c *Client) WorkflowStepStart(ctx context.Context, req contract.WorkflowStepStartRequest) (contract.WorkflowStepStartResponse, error) {
	return request[contract.WorkflowStepStartRequest, contract.WorkflowStepStartResponse](ctx, c, contract.MethodWorkflowStepStart, req)
}

func (c *Client) WorkflowStepEnd(ctx context.Context, req contract.WorkflowStepEndRequest) error {
	_, err := request[contract.WorkflowStepEndRequest, contract.WorkflowStepEndResponse](ctx, c, contract.MethodWorkflowStepEnd, req)
	return err
}

func (c *Client) WorkflowStepTranscript(ctx context.Context, req contract.WorkflowStepTranscriptRequest) error {
	_, err := request[contract.WorkflowStepTranscriptRequest, contract.WorkflowStepTranscriptResponse](ctx, c, contract.MethodWorkflowStepTranscript, req)
	return err
}

// WorkflowPost posts for a script. The step key is the client ID, so a retry or a rerun posts once.
func (c *Client) WorkflowPost(ctx context.Context, req contract.PostRequest) (contract.PostResponse, error) {
	return request[contract.PostRequest, contract.PostResponse](ctx, c, contract.MethodPost, req)
}

func (c *Client) TranscriptFetch(ctx context.Context, req contract.TranscriptFetchRequest) (contract.TranscriptFetchResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return contract.TranscriptFetchResponse{}, err
	}
	data, err := c.call(ctx, contract.MethodTranscriptFetch, body)
	if err != nil {
		return contract.TranscriptFetchResponse{}, err
	}
	var result contract.TranscriptFetchResponse
	err = json.Unmarshal(data, &result)
	return result, err
}
