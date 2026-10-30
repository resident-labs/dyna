package websvc

import (
	"bytes"
	"encoding/json"
	"testing"

	"dyna-agent/internal/jobs"
)

func TestMachineContractEncoding(t *testing.T) {
	value := struct {
		Detail MachineDetail `json:"detail"`
		Work   MachineWork   `json:"work"`
	}{Detail: MachineDetail{MachineSummary: MachineSummary{ID: "node"}}, Work: MachineWork{Running: []MachineWorkItem{{WorkItem: jobs.WorkItem{ID: "job"}}}}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range [][]byte{[]byte(`"id":"node"`), []byte(`"id":"job"`), []byte(`"waiting":null`)} {
		if !bytes.Contains(data, field) {
			t.Errorf("missing %s in %s", field, data)
		}
	}
	for _, field := range [][]byte{[]byte(`"MachineSummary":`), []byte(`"WorkItem":`)} {
		if bytes.Contains(data, field) {
			t.Errorf("embedded field %s was not flattened: %s", field, data)
		}
	}
}
