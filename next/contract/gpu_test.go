package contract

import (
	"encoding/json"
	"testing"
)

func TestSessionStartGPUReadsTrueAsOneGiB(t *testing.T) {
	for body, want := range map[string]GPUGiB{`{}`: 0, `{"gpu":true}`: 1, `{"gpu":false}`: 0, `{"gpu":null}`: 0, `{"gpu":0}`: 0, `{"gpu":1}`: 1, `{"gpu":16}`: 16} {
		var req SessionStartRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil || req.GPU != want {
			t.Errorf("%s: %d, %v; want %d", body, req.GPU, err, want)
		}
	}
	for _, body := range []string{`{"gpu":-1}`, `{"gpu":1.5}`, `{"gpu":"yes"}`, `{"gpu":"2"}`, `{"gpu":[]}`} {
		var req SessionStartRequest
		if err := json.Unmarshal([]byte(body), &req); err == nil {
			t.Errorf("%s decoded to %d", body, req.GPU)
		}
	}
	out, err := json.Marshal(SessionStartRequest{GPU: 4})
	if err != nil || string(out) != `{"room":"","name":"","profile":"","gpu":4}` {
		t.Errorf("encoded %s, %v", out, err)
	}
}
