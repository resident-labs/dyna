package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Result is what a call says about one job. Its field names are what codemode scripts read.
type Result struct {
	Output   string              `json:"output"`
	Job      contract.ShellJobID `json:"job"`
	Running  bool                `json:"running"`
	Status   string              `json:"status"`
	ExitCode *int                `json:"exit_code,omitempty"`
	// Interrupt names the engine-checked sender that ended the foreground wait.
	Interrupt      *contract.Interrupt `json:"interrupt,omitempty"`
	MessageArrived bool                `json:"message_arrived,omitempty"`
}

// Listed is one row of the job list.
type Listed struct {
	Job     contract.ShellJobID `json:"job"`
	Status  string              `json:"status"`
	Command string              `json:"command"`
	Host    contract.HostName   `json:"host,omitempty"`
}

// Jobs is the job list.
type Jobs struct {
	Jobs []Listed `json:"jobs"`
}

// JobCall is one call of the job tool.
type JobCall struct {
	Action string              `json:"action"`
	Job    contract.ShellJobID `json:"job"`
}

// refusal is a call turned down with a typed error, here or by the engine.
type refusal struct{ problem contract.Error }

func (r refusal) Error() string { return string(r.problem.Code) + ": " + r.problem.Message }

func invalid(message string) refusal {
	return refusal{contract.Error{Code: contract.ErrInvalid, Message: message}}
}

// Run is the bash tool: it starts a command and returns when it ends, at the foreground limit, when a message arrives or
// when ctx ends. A command still running then goes on as a background job.
func (s *Server) Run(ctx context.Context, run Run) (Result, error) {
	if strings.TrimSpace(run.Command) == "" {
		return Result{}, invalid("command is required")
	}
	if run.Timeout < 0 || run.Timeout > maxTimeout {
		return Result{}, invalid(fmt.Sprintf("timeout is in seconds, at most %d", maxTimeout))
	}
	j, err := s.run(ctx.Done(), run)
	if err != nil {
		return Result{}, err
	}
	answer := result(j)
	answer.MessageArrived = answer.Running && j.aside != nil
	answer.Interrupt = j.aside
	return answer, nil
}

// Handler serves the extension over plain JSON. A refused call answers with its typed error.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", serve(func(r *http.Request, run Run) (any, error) {
		return s.Run(r.Context(), run)
	}))
	mux.HandleFunc("POST /job", serve(func(_ *http.Request, call JobCall) (any, error) {
		return s.Job(call)
	}))
	return mux
}

func serve[T any](handle func(*http.Request, T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var request T
		var answer any
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request)
		if err != nil {
			err = invalid(err.Error())
		} else {
			answer, err = handle(r, request)
		}
		w.Header().Set("Content-Type", "application/json")
		var refused refusal
		switch {
		case errors.As(err, &refused):
			w.WriteHeader(http.StatusBadRequest)
			answer = refused.problem
		case err != nil:
			w.WriteHeader(http.StatusInternalServerError)
			answer = contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
		}
		_ = json.NewEncoder(w).Encode(answer)
	}
}

// Job is the job tool. A list answers with Jobs, the other actions with a Result.
func (s *Server) Job(call JobCall) (any, error) {
	if call.Action == "list" {
		s.mu.Lock()
		defer s.mu.Unlock()
		jobs := Jobs{Jobs: []Listed{}}
		for _, j := range s.jobs {
			jobs.Jobs = append(jobs.Jobs, Listed{j.id, j.status(), Mask(j.command), j.host})
		}
		return jobs, nil
	}
	if call.Action != "output" && call.Action != "stop" {
		return nil, invalid("action is output, stop or list")
	}
	j, ok := s.find(call.Job)
	if !ok {
		return nil, unknownJob(call.Job)
	}
	if call.Action == "stop" {
		s.stop(j)
	}
	return result(j), nil
}

var jobID = regexp.MustCompile(`^job_[0-9a-f]{12}$`)

// unknownJob says why a job is not here. The box keeps no record of jobs from before its start or of other boxes.
func unknownJob(id contract.ShellJobID) error {
	switch {
	case id == "":
		return invalid("job is required for output and stop")
	case !jobID.MatchString(string(id)):
		return invalid(fmt.Sprintf("no job %q: a job ID looks like job_1f3a9c0b7d42, and job list shows the current ones", id))
	}
	return refusal{contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("no job %s in this box; a box holds only the jobs it started since it began and the last %d finished ones, and job list shows the current ones. Commands that ran in a box that was replaced ended with it. A host run (bash with host) is stopped when its box closes, but when the box was lost or its connection dropped the run keeps going on its host, and its end arrives as a notice", id, keepFinished)}}
}

// result returns the unread output and the job state.
func result(j *job) Result {
	text, running := j.unread()
	out := Result{Output: text, Job: j.id, Running: running, Status: "running"}
	if !running {
		out.Status = j.status()
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.end.Kind == contract.ShellJobExited {
			out.ExitCode = &j.exit
		}
	}
	return out
}
