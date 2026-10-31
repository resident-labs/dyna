package contract

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestHostexecRunArgsRejectsTimeoutOutsideBounds(t *testing.T) {
	for _, timeout := range []int{0, 7200001} {
		data := fmt.Sprintf(`{"room":"room","host":"host","argv":["true"],"timeout":%d,"grant":"grant","mode":"preapproved"}`, timeout)
		var args HostexecRunArgs
		if err := json.Unmarshal([]byte(data), &args); err == nil {
			t.Fatalf("accepted timeout %d", timeout)
		}
	}
}
