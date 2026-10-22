package native

import (
	"context"
	"dyna-agent/internal/account/store"
	"errors"
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
func TestOpenCodeUsagePreservesCurrentRouteErrors(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		status     int
	}{
		{"inference-404", "/inference/go/v1/usage", 404},
		{"inference-405", "/inference/go/v1/usage", 405},
		{"console-400", "/console/api/go/status", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			stubOpenCodeHTTP(t, func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Path)
				status, body := 200, `{"usage":{"rolling":{"percent":20}},"access":{"meters":{"fiveHour":{"usedMicroCents":"0","limitMicroCents":"100000000"}}}}`
				if len(paths) == 1 {
					status, body = tc.status, ""
				} else if r.URL.Path == "/console/api/orgs" {
					body = `[{"id":"org_one"}]`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			var windows []store.Window
			var err error
			m := &openCodeManager{}
			if tc.status == 400 {
				windows, err = m.readOpenCodeConsoleUsage(context.Background(), "fixture-private", "")
			} else {
				windows, err = m.readOpenCodeKeyUsage(context.Background(), "fixture-private")
			}
			if len(paths) != 1 || paths[0] != tc.path || len(windows) != 0 {
				t.Fatalf("paths = %v, windows = %v; want one current route and no windows", paths, windows)
			}
			if tc.status == 400 {
				var status *UsageHTTPError
				if !errors.As(err, &status) || status.Status != 400 {
					t.Fatalf("error = %v; want HTTP 400", err)
				}
			} else if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("error = %v; want unsupported", err)
			}
		})
	}
}
