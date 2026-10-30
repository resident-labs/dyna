package podman

import (
	"os/exec"
	"strings"
	"testing"
)

func TestFrameReturnsStderrOnFailure(t *testing.T) {
	cmd := exec.Command("bash", "-c", frameScript, "dyna-frame", "sh", "-c", "echo out; echo err >&2; exit 1")
	cmd.Stdin = strings.NewReader("DYNA-IN 00000000000000000000\n")
	reply, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	body, status, err := parseReply(reply)
	if err != nil {
		t.Fatal(err)
	}
	if status != 1 || string(body) != "out\nerr\n" {
		t.Fatalf("status %d body %q", status, body)
	}
}
