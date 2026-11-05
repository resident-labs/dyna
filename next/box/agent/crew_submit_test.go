package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/resident-labs/dyna/box/dynaext"
	"github.com/resident-labs/dyna/contract"
)

const countSchema = `{ schema: { type: 'object', required: ['count'], properties: { count: { type: 'number' } } } }`

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAValidSubmitIsTheStepsResultAndEndsTheTurn(t *testing.T) {
	h := newCrewHarness(t)
	h.work = func(dir string, submit dynaext.Submit) {
		write(t, dir, "out.json", `{"count": 3}`)
		if text, end, err := submit("out.json"); !end || err != nil {
			t.Errorf("submit %q %v %v", text, end, err)
		}
	}
	// The final text is not read: the file is the result.
	h.reply = func(string, string) (contract.TurnEnded, error) {
		return contract.TurnEnded{Final: "see the file"}, nil
	}
	got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
	if got["count"] != float64(3) || fmt.Sprint(h.submits) != "[true]" {
		t.Fatalf("%v, submit tools %v", got, h.submits)
	}
}

func TestAnInvalidSubmitComesBackAndARetrySucceeds(t *testing.T) {
	h := newCrewHarness(t)
	var texts []string
	h.work = func(dir string, submit dynaext.Submit) {
		for i, content := range []string{"not json", `{"count": "many"}`, `{"count": 4}`} {
			write(t, dir, "out.json", content)
			text, end, err := submit("out.json")
			texts = append(texts, text)
			if err != nil || end != (i == 2) {
				t.Errorf("submit %d: end %v, %v", i, end, err)
			}
		}
	}
	got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`).(map[string]any)
	if got["count"] != float64(4) || !strings.Contains(texts[0], "not JSON") || !strings.Contains(texts[1], "count") {
		t.Fatalf("%v from %q", got, texts)
	}
}

func TestThreeInvalidSubmitsFailTheStep(t *testing.T) {
	h := newCrewHarness(t)
	var ends []bool
	h.work = func(dir string, submit dynaext.Submit) {
		write(t, dir, "out.json", `{"count": "many"}`)
		for range 4 {
			_, end, _ := submit("out.json")
			ends = append(ends, end)
			if end {
				return
			}
		}
	}
	ended := h.run(crewMeta + `return await agent('count them', ` + countSchema + `)`)
	if ended.Outcome != contract.TurnFailed || !strings.Contains(ended.Error, "3 submits") || fmt.Sprint(ends) != "[false false true]" {
		t.Fatalf("turn %+v, ends %v", ended, ends)
	}
}

func TestAWorkerThatEndsWithoutASubmitFailsWithNoSubmit(t *testing.T) {
	h := newCrewHarness(t)
	h.reply = func(string, string) (contract.TurnEnded, error) {
		return contract.TurnEnded{Final: `{"count": 3}`}, nil
	}
	crew, ended := h.start(crewMeta + `return 1`)
	<-ended
	schema := `{"type":"object"}`
	out := crew.worker(context.Background(), strings.Repeat("0", 64), crewProfiles[0], "p", agentOpts{Schema: []byte(schema)}, h.workdir)
	if out.cause != contract.CauseNoSubmit || out.value != nil || out.reason == "" {
		t.Fatalf("outcome %+v", out)
	}
	// The same through a script: even a final text that is valid JSON is no result.
	if got := h.run(crewMeta + `return await agent('count them', ` + countSchema + `)`); got.Outcome != contract.TurnFailed {
		t.Fatalf("turn %+v", got)
	}
}

func TestAStepWithoutASchemaIsUnchanged(t *testing.T) {
	h := newCrewHarness(t)
	h.work = func(string, dynaext.Submit) { t.Error("a worker without a schema was given a submit") }
	if got := h.result(crewMeta + `return await agent('plain')`); got != "done: plain" || fmt.Sprint(h.submits) != "[false]" {
		t.Fatalf("%v, submit tools %v", got, h.submits)
	}
}

func TestSubmitRefusesPathsOutsideTheFolder(t *testing.T) {
	h := newCrewHarness(t)
	outside := t.TempDir()
	write(t, outside, "secret.json", `{"count": 1}`)
	var refused atomic.Int32
	h.work = func(dir string, submit dynaext.Submit) {
		if err := os.Symlink(filepath.Join(outside, "secret.json"), filepath.Join(dir, "link.json")); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
			t.Error(err)
		}
		for path, want := range map[string]error{
			filepath.Join(outside, "secret.json"):           errSubmitOutside,
			"../" + filepath.Base(outside) + "/secret.json": nil,
			"link.json":    nil,
			"folder":       errSubmitNotFile,
			"missing.json": nil,
		} {
			_, end, err := submit(path)
			if err == nil || end || (want != nil && !errors.Is(err, want)) {
				t.Errorf("%s: end %v, %v", path, end, err)
			} else {
				refused.Add(1)
			}
		}
		write(t, dir, "out.json", `{"count": 2}`)
		if _, end, err := submit("out.json"); !end || err != nil {
			t.Errorf("a path inside: end %v, %v", end, err)
		}
	}
	got := h.result(crewMeta + `return await agent('count them', ` + countSchema + `)`)
	if refused.Load() != 5 || fmt.Sprint(got) != "map[count:2]" {
		t.Fatalf("%d refused, result %v", refused.Load(), got)
	}
}
