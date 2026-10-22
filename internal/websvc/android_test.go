package websvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAndroidApp(t *testing.T) {
	e := newEnv(t)
	get := func(path string) (int, string, string) {
		w := e.do(e.app, req{method: "GET", url: tsApp + path})
		return w.Code, w.Body.String(), w.Header().Get("Content-Type")
	}
	if code, _, _ := get("/api/android/app.apk"); code != 404 {
		t.Fatalf("apk before it is built = %d, want 404", code)
	}
	if code, _, _ := get("/api/android/version"); code != 404 {
		t.Fatalf("version before it is built = %d, want 404", code)
	}
	dir := filepath.Join(filepath.Dir(e.store.Dir()), "android")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dyna-call.apk"), []byte("PK-apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "version.json"), []byte(`{"versionCode":3,"protocol":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A phone that is not paired yet can download it: no cookie in these requests.
	code, body, ctype := get("/api/android/app.apk")
	if code != 200 || body != "PK-apk" || ctype != "application/vnd.android.package-archive" {
		t.Fatalf("apk = %d %q %q", code, body, ctype)
	}
	code, body, ctype = get("/api/android/version")
	if code != 200 || !strings.Contains(body, `"versionCode":3`) || ctype != "application/json" {
		t.Fatalf("version = %d %q %q", code, body, ctype)
	}
	if w := e.do(e.app, req{url: tsApp + "/api/android/version", body: "{}"}); w.Code != 405 {
		t.Fatalf("POST = %d, want 405", w.Code)
	}
}
