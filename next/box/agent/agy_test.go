package agent

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestOpenAgyNeedsItsFiles(t *testing.T) {
	_, err := openAgy(t.Context(), contract.BoxHelloResponse{Harness: contract.HarnessAgy}, "/box/work", t.TempDir(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "without its files") {
		t.Fatal(err)
	}
}
