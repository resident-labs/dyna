package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"dyna-agent/internal/callspec"
)

func TestStartupHealthMarksEngineNotReady(t *testing.T) {
	started := time.Now().Round(0)
	req := httptest.NewRequest(http.MethodPost, "http://engine/api/Health", nil)
	res := httptest.NewRecorder()
	startupHealthHandler(started).ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("startup Health status = %d", res.Code)
	}
	var health Health
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if health.Status != healthStarting || health.Ready || !health.Started.Equal(started) {
		t.Fatalf("startup Health = %+v", health)
	}

	res = httptest.NewRecorder()
	startupHealthHandler(started).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "http://engine/api/Post", nil))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("an ordinary call ran before migrations: %d", res.Code)
	}
	var reply struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil || reply.Code != callspec.CodeEngineStarting {
		t.Fatalf("startup reply code = %q, %v; want %q", reply.Code, err, callspec.CodeEngineStarting)
	}
}
