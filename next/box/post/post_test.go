package post

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVisualPreparationUploadsPreviewAndHTML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "card.html")
	doc := "<title>Report</title><p>Hello</p>"
	if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	uploaded := map[string]string{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/files" {
			t.Fatalf("upload: %s %s", r.Method, r.URL)
		}
		name := r.URL.Query().Get("name")
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		uploaded[name] = string(data)
		body, _ := json.Marshal(contract.Attachment{Name: name, SHA256: "hash-" + name})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})}
	check := contract.VisualCheck{Name: contract.VisualCheckRenders, Status: contract.VisualCheckPass}
	req, checks, err := prepare(context.Background(), func(ctx context.Context, path string) (contract.Attachment, error) {
		return UploadHTTP(ctx, client, path)
	}, tools.PostInput{Text: "report", HTML: path, VisualUpdate: "original", ClientID: "retry"}, func(_ context.Context, data []byte, csp string, allowNet bool) visual.Result {
		if string(data) != doc || csp != visual.CSP(false) || allowNet {
			t.Fatal("incorrect preview input")
		}
		return visual.Result{Banner: []byte("banner"), Thumb: []byte("thumb"), Widths: []visual.Shot{{Width: 320, Image: []byte("shot")}}, Checks: []contract.VisualCheck{check}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Attachments) != 1 || req.ClientID != "retry" || req.VisualUpdate != "original" {
		t.Fatalf("request: %+v", req)
	}
	file := req.Attachments[0]
	card := file.Visual
	if file.SHA256 != "hash-card.html" || card == nil || card.Title != "Report" || card.Height != 360 || card.Banner != "hash-banner.webp" || card.Thumbnail != "hash-thumbnail.webp" || len(card.Shots) != 1 || card.Shots[0].File != "hash-320.webp" || len(checks) != 1 || checks[0] != check || len(card.Checks) != 1 || card.Checks[0] != check {
		t.Fatalf("visual: %+v checks: %+v", file, checks)
	}
	if uploaded["card.html"] != doc || uploaded["banner.webp"] != "banner" || uploaded["thumbnail.webp"] != "thumb" || uploaded["320.webp"] != "shot" {
		t.Fatalf("uploads: %+v", uploaded)
	}
}
