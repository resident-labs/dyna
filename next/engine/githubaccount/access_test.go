package githubaccount

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyRepoDenialsDoNotPermitCachedAccess(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/repos/org/repo" || r.Header.Get("Authorization") != "Bearer work-token" {
					t.Fatal("incorrect repository credential")
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"denied work-token"}`)
			}))
			defer server.Close()
			err := VerifyRepo(t.Context(), server.Client(), server.URL, "org/repo", "work-token", "GitHub account work of room itasca")
			denied := status == 401 || status == 403 || status == 404
			if (err != nil) != denied || calls != 1 {
				t.Fatalf("status %d: %v, calls %d", status, err, calls)
			}
			if err != nil && (!strings.Contains(err.Error(), "GitHub account work of room itasca") || strings.Contains(err.Error(), "work-token")) {
				t.Fatal("unsafe denial")
			}
		})
	}
}
