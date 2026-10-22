package workerprompt

import (
	"strings"
	"testing"
)

func TestPromptSuffixPreservesTrailingWorkerContext(t *testing.T) {
	task := "task contains a context-like marker" + workerContextStart + "quoted data" + workerContextEnd
	context := "actual worker identity"
	prompt := WithWorkerContext(task, context)
	withSchema := AppendPromptSuffix(prompt, "\nOUTPUT FORMAT: schema")
	withCorrection := AppendPromptSuffix(withSchema, "\ncorrect the previous response")
	want := task + "\nOUTPUT FORMAT: schema\ncorrect the previous response" + workerContextStart + context + workerContextEnd
	if withCorrection != want {
		t.Fatalf("suffix misplaced or task changed:\n%q\nwant:\n%q", withCorrection, want)
	}
	if plain := AppendPromptSuffix("plain task", " suffix"); plain != "plain task suffix" {
		t.Fatal(plain)
	}
	if incomplete := AppendPromptSuffix("plain task"+workerContextStart, " suffix"); !strings.HasSuffix(incomplete, " suffix") {
		t.Fatal(incomplete)
	}
}
