package box

import (
	"bytes"
	"log"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestStepLogTimesEachStepFromTheLast(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	defer log.SetOutput(os.Stderr)
	steps := NewStepLog("box box_1")
	steps.last = time.Now().Add(-2 * time.Second)
	steps.Done("image")
	steps.Done("slice")
	lines := out.String()
	if !regexp.MustCompile(`box box_1: image took 2\.\d{3}s\n`).MatchString(lines) {
		t.Fatalf("first step: %s", lines)
	}
	if !regexp.MustCompile(`box box_1: slice took 0\.\d{3}s\n`).MatchString(lines) {
		t.Fatalf("second step starts when the first ended: %s", lines)
	}
}
