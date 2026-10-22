package websvc

import (
	"net/http"
	"os"
	"path/filepath"
)

// The Android call app is built on this machine (mobile/android/build-apk.sh) into
// <data dir>/android: dyna-call.apk and version.json ({"versionCode": N, "protocol": N}).
// The files are not secret, and a phone that is not paired yet needs them, so both routes
// are public like /api/pair. A missing file is a 404, which hides the download in the page.
const androidFiles = "android"

func (s *Server) android(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		jsonError(w, http.StatusMethodNotAllowed, "use GET")
		return
	}
	dir := filepath.Join(filepath.Dir(s.o.Store.Dir()), androidFiles)
	switch r.URL.Path {
	case "/api/android/version":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		serveIfExists(w, r, filepath.Join(dir, "version.json"))
	default:
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		w.Header().Set("Content-Disposition", `attachment; filename="dyna-call.apk"`)
		serveIfExists(w, r, filepath.Join(dir, "dyna-call.apk"))
	}
}

func serveIfExists(w http.ResponseWriter, r *http.Request, path string) {
	if _, err := os.Stat(path); err != nil {
		w.Header().Del("Content-Disposition")
		jsonError(w, http.StatusNotFound, "the Android app is not built on this machine")
		return
	}
	http.ServeFile(w, r, path)
}
