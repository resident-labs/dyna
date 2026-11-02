package shell

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
}

// Listed is one row of the job list.
type Listed struct {
	Job     contract.ShellJobID `json:"job"`
	Status  string              `json:"status"`
	Command string              `json:"command"`
	Host    contract.HostName   `json:"host,omitempty"`
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

// Handler serves the extension over plain JSON. A refused call answers with its typed error.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", serve(func(r *http.Request, run Run) (any, error) {
		if strings.TrimSpace(run.Command) == "" {
			return nil, invalid("command is required")
		}
		if run.Timeout != 0 && run.Host == "" {
			return nil, invalid("timeout is for a host run; stop a job in the box with job stop")
		}
		j, err := s.run(r.Context().Done(), run)
		if err != nil {
			return nil, err
		}
		return result(j), nil
	}))
	mux.HandleFunc("POST /job", serve(func(_ *http.Request, call JobCall) (any, error) {
		return s.jobCall(call)
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

func (s *Server) jobCall(call JobCall) (any, error) {
	if call.Action == "list" {
		s.mu.Lock()
		defer s.mu.Unlock()
		jobs := []Listed{}
		for _, j := range s.jobs {
			jobs = append(jobs, Listed{j.id, j.status(), Mask(j.command), j.host})
		}
		return struct {
			Jobs []Listed `json:"jobs"`
		}{jobs}, nil
	}
	if call.Action != "output" && call.Action != "stop" {
		return nil, invalid("action is output, stop or list")
	}
	j, ok := s.find(call.Job)
	if !ok {
		return nil, invalid(fmt.Sprintf("unknown job %q", call.Job))
	}
	if call.Action == "stop" {
		if j.host != "" {
			return nil, invalid(fmt.Sprintf("job %s runs on %s and ends at its timeout: nothing here can stop a host run", j.id, j.host))
		}
		s.stop(j)
	}
	return result(j), nil
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
