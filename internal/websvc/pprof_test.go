package websvc

import (
	"net/http"
	"strings"
	"testing"
)

func TestProfilesStayOnPrivateSocket(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
		w := e.do(asCaller(e.srv.UnixHandler(), owner), req{method: http.MethodGet, url: "http://engine" + path})
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Fatalf("private profile %s = %d %s", path, w.Code, w.Body)
		}
	}
	for _, connect := range []bool{false, true} {
		if connect {
			e.srv.o.Ext = terminalExtension{result: AuthOK}
		}
		for _, path := range []string{"/debug/pprof", "/debug/pprof/", "/debug/pprof/profile?seconds=1", "/debug/pprof/trace?seconds=1", "/debug/pprof/heap", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
			for origin, handler := range map[string]http.Handler{tsApp: e.app, tsCards: e.cards} {
				w := e.do(handler, req{method: http.MethodGet, url: origin + path, cookie: e.paired(), header: map[string]string{"X-Dyna-Local": "1"}})
				if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "profile") {
					t.Fatalf("public profile %s (Connect %v) = %d %s", path, connect, w.Code, w.Body)
				}
			}
		}
	}
}
