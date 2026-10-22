package native

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func stubOpenCodeHTTP(t *testing.T, f openCodeUsageTransport) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = previous })
}
func TestConsoleAmountsAndNativeReset(t *testing.T) {
	stubOpenCodeHTTP(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://opencode.ai/console/api/go/status" || r.Header.Get("Authorization") != "Bearer fixture-private" || r.Header.Get("x-org-id") != "org_fixture" {
			t.Fatal("wrong scope")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access":{"endsAt":"2027-01-01T00:00:00Z","meters":{"fiveHour":{"usedMicroCents":"300000000","limitMicroCents":"1200000000","resetsAt":"2027-01-01T03:00:00Z"},"month":{"usedMicroCents":"12300000000","limitMicroCents":"40000000000"}}}}`))}, nil
	})
	ws, err := (&openCodeManager{}).readOpenCodeConsoleUsage(context.Background(), "fixture-private", "org_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(ws) != 2 || *ws[0].Used != 3 || *ws[0].Limit != 12 || *ws[0].UsedPercent != 25 || ws[0].Unit != "USD" {
		t.Fatalf("wrong units %+v", ws)
	}
	if ws[1].ResetsAt != "" {
		t.Fatal("invented monthly reset from period end")
	}
}
func TestKeyUsageMissingAndZeroAreDifferent(t *testing.T) {
	stubOpenCodeHTTP(t, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"usage":{"rolling":{"percent":0},"weekly":{},"monthly":{"percent":20,"resetsAt":"invalid"}}}`))}, nil
	})
	ws, err := (&openCodeManager{}).readOpenCodeKeyUsage(context.Background(), "fixture-private")
	if err != nil || len(ws) != 2 || *ws[0].UsedPercent != 0 || ws[1].ResetsAt != "" {
		t.Fatalf("%+v %v", ws, err)
	}
}
func TestMicroUSDExactIntegerBeforeProjection(t *testing.T) {
	for _, raw := range []string{`"1.5"`, `1.5`, `"1e8"`, `1e8`, `"-1"`, `null`, `""`, `"NaN"`} {
		if _, ok := microUSD([]byte(raw)); ok {
			t.Fatalf("accepted noninteger microcents %s", raw)
		}
	}
	amount, ok := microUSD([]byte(`"9007199254740993"`))
	if !ok || amount != 90071992.54740994 {
		t.Fatalf("large integer rounded before unit conversion %.17g %v", amount, ok)
	}
}
func TestKeyUsageCurrentRouteWithLegacyCapabilityFallback(t *testing.T) {
	var paths []string
	stubOpenCodeHTTP(t, func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if len(paths) == 1 {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"usage":{"rolling":{"percent":20}}}`))}, nil
	})
	if _, err := (&openCodeManager{}).readOpenCodeKeyUsage(context.Background(), "fixture-private"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/inference/go/v1/usage" || paths[1] != "/zen/go/v1/usage" {
		t.Fatal(paths)
	}
}
func TestConsoleMissingOrgUsesOnlySoleWorkspace(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(map[bool]string{false: "sole", true: "ambiguous"}[multiple], func(t *testing.T) {
			scoped := false
			stubOpenCodeHTTP(t, func(r *http.Request) (*http.Response, error) {
				body := ""
				status := 400
				switch r.URL.Path {
				case "/console/api/orgs":
					status = 200
					body = `[{"id":"org_one"}]`
					if multiple {
						body = `[{"id":"org_one"},{"id":"org_two"}]`
					}
				case "/console/api/go/status":
					if r.Header.Get("x-org-id") == "org_one" {
						scoped = true
						status = 200
						body = `{"access":{"meters":{"fiveHour":{"usedMicroCents":"0","limitMicroCents":"100000000"}}}}`
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			windows, err := (&openCodeManager{}).readOpenCodeConsoleUsage(context.Background(), "fixture-private", "")
			if multiple {
				if scoped || err == nil {
					t.Fatal("selected ambiguous workspace")
				}
			} else if !scoped || err != nil || len(windows) != 1 {
				t.Fatalf("sole workspace failed %v", err)
			}
		})
	}
}
