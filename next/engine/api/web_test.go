package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestWebGuardAndLogin(t *testing.T) {
	now := time.Unix(1000, 0)
	a := New(&fakeService{}, nil)
	w, err := NewWeb(a, "127.0.0.1:7790", fstest.MapFS{"index.html": {Data: []byte("app")}}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	w.handler = a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalOwner}, Web: true})
	request := func(method, host, path, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader("{}"))
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		w.ServeHTTP(rec, r)
		return rec
	}
	for _, host := range []string{"127.0.0.1:7790", "localhost:7790"} {
		if got := request("GET", host, "/", "", nil).Code; got != 403 {
			t.Fatalf("host %s: %d", host, got)
		}
	}
	host := "dyna.localhost:7790"
	if got := request("POST", host, "/v1/post", "http://evil", nil).Code; got != 403 {
		t.Fatal(got)
	}
	if got := request("GET", host, "/v1/events", "", nil).Code; got != 403 {
		t.Fatal(got)
	}
	login, problem := w.login(nil, contract.Principal{}, contract.WebLoginRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	u, err := url.Parse(login.URL)
	if err != nil {
		t.Fatal(err)
	}
	rec := request("GET", host, u.RequestURI(), "", nil)
	if rec.Code != 303 || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("login: %d", rec.Code)
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", cookie)
	}
	if got := request("GET", host, u.RequestURI(), "", nil).Code; got != 403 {
		t.Fatal(got)
	}
	if got := request("POST", host, "/v1/room.list", "http://"+host, cookie).Code; got != 200 {
		t.Fatal(got)
	}
	if got := request("POST", host, "/v1/web.login", "http://"+host, cookie).Code; got != http.StatusForbidden {
		t.Fatal(got)
	}
	expiring, _ := w.login(nil, contract.Principal{}, contract.WebLoginRequest{})
	u, _ = url.Parse(expiring.URL)
	now = now.Add(time.Minute)
	if got := request("GET", host, u.RequestURI(), "", nil).Code; got != 403 {
		t.Fatal(got)
	}
}
