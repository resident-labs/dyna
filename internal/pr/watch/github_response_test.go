package watch

import (
	"encoding/json"
	"fmt"
	"io"
)

// One concrete type per GitHub response the CI tests fake, so a fixture cannot
// drift from the body the client decodes.

// checkRunsResponse is the body of GET /repos/{owner}/{repo}/commits/{ref}/check-runs.
type checkRunsResponse struct {
	CheckRuns []Check `json:"check_runs"`
}

// workflowRunsResponse is the body of GET /repos/{owner}/{repo}/actions/runs.
type workflowRunsResponse struct {
	WorkflowRuns []Run `json:"workflow_runs"`
}

// jobsResponse is the body of GET /repos/{owner}/{repo}/actions/runs/{id}/attempts/{n}/jobs.
type jobsResponse struct {
	Jobs []budgetJob `json:"jobs"`
}

// emptyResponse is the body of a GitHub write endpoint such as rerun or cancel,
// which answers success without content.
type emptyResponse struct{}

// writeFakeJSON answers one fake GitHub read with a concrete response.
func writeFakeJSON[T any](stdout io.Writer, value T) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "HTTP/2.0 200 OK\r\n\r\n%s", raw)
	return err
}
