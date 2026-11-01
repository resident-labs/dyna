package management

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func zaiMillis(after time.Duration) string {
	return strconv.FormatInt(dynaUsageNow.Add(after).UnixMilli(), 10)
}

// The owner's live response shape: code 200, level max, a TIME_LIMIT, and a
// TOKENS_LIMIT with unit 3 and number 5.
func TestDynaUsageZAIFiveHourAndTools(t *testing.T) {
	usage := dynaParse(t, parseZAIUsage, `{"code":200,"msg":"ok","success":true,"data":{"level":"max","limits":[
		{"type":"TIME_LIMIT","unit":5,"number":1,"percentage":90,"nextResetTime":`+zaiMillis(time.Hour)+`},
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":12,"nextResetTime":`+zaiMillis(2*time.Hour)+`}]}}`)
	if len(usage.windows) != 1 {
		t.Fatalf("windows = %#v, want the coding window only", usage.windows)
	}
	session := dynaWindowNamed(t, usage, "five_hour")
	if session.Utilization != 12 || session.ResetsAt != dynaUsageNow.Add(2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("session = %#v", session)
	}
}

// Near its end a week resets before five hours do, so reset order cannot classify.
func TestDynaUsageZAIClassifiesByUnitNotResetOrder(t *testing.T) {
	usage := dynaParse(t, parseZAIUsage, `{"code":200,"success":true,"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":6,"number":7,"percentage":80,"nextResetTime":`+zaiMillis(time.Hour)+`},
		{"type":"CREDIT_LIMIT","unit":3,"number":5,"percentage":5,"nextResetTime":`+zaiMillis(4*time.Hour)+`}]}}`)
	if dynaWindowNamed(t, usage, "seven_day").Utilization != 80 || dynaWindowNamed(t, usage, "five_hour").Utilization != 5 {
		t.Fatalf("windows = %#v", usage.windows)
	}
}

func TestDynaUsageZAIOmitsImpossibleResets(t *testing.T) {
	usage := dynaParse(t, parseZAIUsage, `{"code":200,"success":true,"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":0,"nextResetTime":0},
		{"type":"TOKENS_LIMIT","unit":6,"number":7,"percentage":1,"nextResetTime":`+zaiMillis(90*24*time.Hour)+`}]}}`)
	if dynaWindowNamed(t, usage, "five_hour").ResetsAt != "" || dynaWindowNamed(t, usage, "seven_day").ResetsAt != "" {
		t.Fatalf("windows = %#v", usage.windows)
	}
}

func TestDynaUsageZAIEnvelopeFailuresAreUnknown(t *testing.T) {
	for _, body := range []string{
		`{"code":500,"msg":"no coding plan","success":false}`,
		`{"code":500,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":1}]}}`,
		`{"code":200,"success":false,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3,"percentage":1}]}}`,
		`{"code":200,"success":true,"data":{"limits":[]}}`,
		`{"code":200,"success":true,"data":{}}`,
		`{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":3}]}}`,
		`{"code":200,"success":true,"data":{"limits":[{"type":"TOKENS_LIMIT","unit":9,"percentage":1}]}}`,
		`{"code":200,"success":true,"data":{"limits":[{"type":"TIME_LIMIT","unit":3,"percentage":1}]}}`,
	} {
		if usage := dynaParse(t, parseZAIUsage, body); len(usage.windows) != 0 {
			t.Fatalf("%s produced %#v", body, usage.windows)
		}
	}
}

func TestDynaUsageZAISendsTheKeyRawToAFixedHost(t *testing.T) {
	fake := dynaFixedUsage(t, http.StatusOK, `{"code":500,"success":false}`)
	auth := &coreauth.Auth{ID: "invented", Provider: "openai-compatible-zai", Attributes: map[string]string{"api_key": "invented-key", "base_url": "https://elsewhere.example"}}
	if entry := dynaOneEntry(t, dynaUsageHandler(t, auth)); entry.State != dynaUsageUnknown || entry.Error != "" {
		t.Fatalf("entry = %#v", entry)
	}
	request := fake.requests[0]
	if request.URL.String() != "https://api.z.ai/api/monitor/usage/quota/limit" || request.Header.Get("Authorization") != "invented-key" {
		t.Fatalf("request = %s %#v", request.URL, request.Header)
	}
}
