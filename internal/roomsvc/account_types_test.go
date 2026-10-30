package roomsvc

import (
	"bytes"
	"encoding/json"

	"dyna-agent/internal/account/service"
	"os"
	"testing"
)

func TestAccountTypesFileMatchesGo(t *testing.T) {
	const output = "web/src/lib/api/account.gen.ts"
	const filename = "../../" + output
	const update = "DYNA_UPDATE_ACCOUNT_TYPES=1 go test ./internal/roomsvc/ -run AccountTypes"
	want, err := generateTypes(output, update)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DYNA_UPDATE_ACCOUNT_TYPES") != "" {
		if err := os.WriteFile(filename, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; run %s (read error: %v)", filename, update, err)
	}
}

// TestAccountTypesMatchJSON verifies the embedding and nullable slices that
// need explicit tygo tags.
func TestAccountTypesMatchJSON(t *testing.T) {
	data, err := json.Marshal(service.ScopeAccount{Account: service.Account{ID: "test"}, ActiveHere: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range [][]byte{[]byte(`"id":"test"`), []byte(`"activeHere":true`), []byte(`"repos":null`), []byte(`"windows":null`)} {
		if !bytes.Contains(data, field) {
			t.Errorf("missing %s in %s", field, data)
		}
	}
	if bytes.Contains(data, []byte(`"Account":`)) {
		t.Fatalf("embedded account was not flattened: %s", data)
	}
}
