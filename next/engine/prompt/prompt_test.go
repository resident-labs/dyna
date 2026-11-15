package prompt

import (
	_ "embed"
	"strings"
	"testing"
)

//go:embed testdata/core.txt
var approvedCore string

func TestCoreMatchesApprovedText(t *testing.T) {
	want := strings.TrimSuffix(approvedCore, "\n")
	if Core != want {
		t.Fatalf("Core does not match the approved text:\ngot:\n%s\nwant:\n%s", Core, want)
	}
}
