package connect

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWhoamiKeepsEmptyConnectFields(t *testing.T) {
	user := ""
	expires, grantEnd := time.Time{}, time.Time{}
	data, err := json.Marshal(Whoami{Kind: "connect", User: &user, Expires: &expires, GrantEnd: &grantEnd})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"user":""`, `"expires":"0001-01-01T00:00:00Z"`, `"grantEnd":"0001-01-01T00:00:00Z"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("missing %s in %s", field, data)
		}
	}
}
