package librarian

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLibraryViewZeroLastRunIsPresent(t *testing.T) {
	data, err := json.Marshal(LibraryView{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"lastRun":"0001-01-01T00:00:00Z"`) {
		t.Fatalf("missing zero lastRun in %s", data)
	}
}
