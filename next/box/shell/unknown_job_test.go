package shell

import (
	"net/http"
	"strings"
	"syscall"
	"testing"
)

func TestAMissingJobSaysWhy(t *testing.T) {
	r := newRig("", syscall.SIGKILL)
	for _, c := range []struct{ input, code, text string }{
		{`{"action":"output","job":"job_1f3a9c0b7d42"}`, "not_found", "no job job_1f3a9c0b7d42 in this box; a box holds only the jobs it started since it began and the last 20 finished ones, and job list shows the current ones. Commands that ran in a box that was replaced ended with it. A host run (bash with host) is stopped when its box closes, but when the box was lost or its connection dropped the run keeps going on its host, and its end arrives as a notice"},
		{`{"action":"stop","job":"job_1f3a9c0b7d42"}`, "not_found", "no job job_1f3a9c0b7d42 in this box"},
		{`{"action":"output","job":"4012"}`, "invalid", `no job \"4012\": a job ID looks like job_1f3a9c0b7d42, and job list shows the current ones`},
		{`{"action":"output"}`, "invalid", "job is required for output and stop"},
	} {
		status, body := call(t, r.server.Handler(), "/job", c.input)
		if status != http.StatusBadRequest || !strings.Contains(body, `"code":"`+c.code+`"`) || !strings.Contains(body, c.text) {
			t.Fatalf("%s = %d %s", c.input, status, body)
		}
	}
}
