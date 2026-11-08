package shell

import (
	"sync"

	"github.com/resident-labs/dyna/contract"
)

// OutputReader belongs to one tool user's turn, not to the shared job server.
// Reset at turn start; crew workers each have their own reader.
type OutputReader struct {
	mu         sync.Mutex
	checked    map[contract.ShellJobID]bool
	generation uint64
}

func (r *OutputReader) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checked = make(map[contract.ShellJobID]bool)
	r.generation++
}

func (r *OutputReader) Job(s *Server, call JobCall) (any, error) {
	if call.Action != "output" {
		return s.Job(call)
	}
	r.mu.Lock()
	checked, generation := r.checked[call.Job], r.generation
	r.mu.Unlock()
	if checked {
		j, ok := s.find(call.Job)
		if !ok {
			return nil, unknownJob(call.Job)
		}
		// Snapshot without consuming unread output: a later finished result still gets it.
		j.mu.Lock()
		running := !j.finished
		j.mu.Unlock()
		if running {
			return Result{Job: call.Job, Running: true, Status: "running", Repeated: true}, nil
		}
	}
	answer, err := s.Job(call)
	if result, ok := answer.(Result); ok && result.Running {
		r.mu.Lock()
		if generation == r.generation {
			if r.checked == nil {
				r.checked = make(map[contract.ShellJobID]bool)
			}
			r.checked[call.Job] = true
		}
		r.mu.Unlock()
	}
	return answer, err
}
