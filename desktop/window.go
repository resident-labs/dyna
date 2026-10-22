package main

import (
	"bytes"
	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
	"html/template"
	"net/http"
	"net/url"
	"time"
)

// The window starts with this small local page. The app itself loads directly
// from the server, so its cookies, API calls and event streams share one origin.
var windowPage = template.Must(template.New("window").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Dyna</title>
<style>body{margin:0;min-height:100vh;display:grid;place-content:center;background:#1c1b1e;color:#e8e6ea;font:16px system-ui}main{max-width:560px;padding:32px}code{display:block;margin-top:16px}</style>
</head><body><main>{{if .URL}}<p>Opening Dyna…</p><script>location.replace({{.URL}})</script>{{else}}<p>{{.Error}}</p>{{if .Start}}<code>systemctl --user start dyna-engine</code>{{end}}{{end}}</main></body></html>`))

func desktopPage(addr string) string {
	page := struct {
		URL   string
		Error string
		Start bool
	}{}
	render := func() string {
		var out bytes.Buffer
		_ = windowPage.Execute(&out, page)
		return out.String()
	}
	if _, err := websvc.LoopbackPort(addr); err != nil {
		page.Error = err.Error()
		return render()
	}
	u := url.URL{Scheme: "http", Host: addr, Path: "/"}
	client := http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Get(u.String())
	if err == nil {
		response.Body.Close()
	}
	if err != nil || response.StatusCode != http.StatusOK {
		page.Error = "Dyna cannot reach its server. Start the server, then open Dyna again."
		page.Start = true
		return render()
	}
	store, err := devicestore.DefaultStore()
	if err != nil {
		page.Error = "Dyna cannot open its pairing store: " + err.Error()
		return render()
	}
	code, err := store.NewScopedCode("desktop", scope.All())
	if err != nil {
		page.Error = "Dyna cannot create a pairing code: " + err.Error()
		return render()
	}
	u.Fragment = "pair=" + code
	page.URL = u.String()
	return render()
}
