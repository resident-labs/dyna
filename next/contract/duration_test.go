package contract

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestDurationOfDropsZeroUnits(t *testing.T) {
	for d, want := range map[time.Duration]Duration{
		0:                                  "",
		time.Hour:                          "1h",
		15 * time.Minute:                   "15m",
		90 * time.Second:                   "1m30s",
		time.Hour + 30*time.Minute:         "1h30m",
		time.Hour + 5*time.Second:          "1h0m5s",
		24 * time.Hour:                     "24h",
		time.Minute + 500*time.Millisecond: "1m0.5s",
	} {
		if got := DurationOf(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
		if back, err := DurationOf(d).Parse(); err != nil || back != d {
			t.Errorf("%v: read back as %v %v", d, back, err)
		}
	}
}

func TestSubscriptionAddRequestDecodes(t *testing.T) {
	var req SubscriptionAddRequest
	if err := json.Unmarshal([]byte(`{"scope":"session","session":"upstream-sync","every":"1h","wake":"ses_a"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.Scope != ScopeSession || req.Session != "upstream-sync" || req.Every != "1h" || req.Wake != "ses_a" {
		t.Fatalf("%+v", req)
	}
	// null is a field left out.
	req = SubscriptionAddRequest{}
	if err := json.Unmarshal([]byte(`{"scope":"room","room":"room_a","on":["notice.posted"],"every":null,"wake":"root","session":null,"run":null}`), &req); err != nil || req.Every != "" || req.Session != "" || req.Wake != "root" {
		t.Fatalf("%+v %v", req, err)
	}
	for body, want := range map[string]string{
		`{"scope":"session","every":3600}`:            `every is a duration string such as "1h", "15m" or "90s", not the number 3600`,
		`{"scope":"session","every":true}`:            `every is a duration string such as "1h", "15m" or "90s", not true`,
		`{"scope":"room","wake":{"session":"ses_a"}}`: `wake is a session name or ID as a string, such as "upstream-sync" or "ses_…", not an object`,
		`{"scope":"room","wake":["ses_a"]}`:           `wake is a session name or ID as a string, such as "upstream-sync" or "ses_…", not an array`,
		`{"scope":"session","session":7}`:             `session is a session name or ID as a string, such as "upstream-sync" or "ses_…", not the number 7`,
	} {
		if err := json.Unmarshal([]byte(body), &req); err == nil || err.Error() != want {
			t.Errorf("%s: %v", body, err)
		}
	}
	// A strict decoder still refuses a field the request does not have.
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"scope":"room","to":"x"}`)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err == nil || err.Error() != `json: unknown field "to"` {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestSubscriptionUpdateRequestDecodes(t *testing.T) {
	var req SubscriptionUpdateRequest
	if err := json.Unmarshal([]byte(`{"id":"sub_a","on":["github.pr.changed","github.ci.finished"],"every":"15m"}`), &req); err != nil {
		t.Fatal(err)
	}
	if req.ID != "sub_a" || req.On == nil || len(*req.On) != 2 || (*req.On)[1] != "github.ci.finished" || req.Every == nil || *req.Every != "15m" {
		t.Fatalf("%+v", req)
	}
	// null leaves fields nil; empty string clears every.
	req = SubscriptionUpdateRequest{}
	if err := json.Unmarshal([]byte(`{"id":"sub_a","on":null,"every":null}`), &req); err != nil || req.On != nil || req.Every != nil {
		t.Fatalf("null: %+v %v", req, err)
	}
	req = SubscriptionUpdateRequest{}
	if err := json.Unmarshal([]byte(`{"id":"sub_a","on":[],"every":""}`), &req); err != nil || req.On == nil || len(*req.On) != 0 || req.Every == nil || *req.Every != "" {
		t.Fatalf("empty: %+v %v", req, err)
	}
	for body, want := range map[string]string{
		`{"id":"sub_a","every":3600}`: `every is a duration string such as "1h", "15m" or "90s", not the number 3600`,
		`{"id":"sub_a","every":true}`: `every is a duration string such as "1h", "15m" or "90s", not true`,
	} {
		if err := json.Unmarshal([]byte(body), &req); err == nil || err.Error() != want {
			t.Errorf("%s: %v", body, err)
		}
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"id":"sub_a","to":"x"}`)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err == nil || err.Error() != `json: unknown field "to"` {
		t.Fatalf("unknown field: %v", err)
	}
}
