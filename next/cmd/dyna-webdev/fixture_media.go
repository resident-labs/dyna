package main

import (
	"fmt"
	"image"
	"image/color"
	pngenc "image/png"
	"net/http"
	"strings"
)

// fixtureShot is a 640x360 gradient, so an image has something to show.
func fixtureShot() image.Image {
	shot := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for y := range 360 {
		for x := range 640 {
			shot.Set(x, y, color.RGBA{uint8(40 + x/5), uint8(60 + y/3), 150, 255})
		}
	}
	return shot
}

// devFiles answers for the invented reads, since the fake harness writes no transcript. An item named gone stays
// missing, so its row reads as one without an image.
func devFiles(files http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files/transcript" || strings.Contains(r.URL.Query().Get("item"), "gone") {
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_ = pngenc.Encode(w, fixtureShot())
	})
}

// fixtureFiles is one invented file for each of the media viewer's modes, and the two that reach its limits.
func fixtureFiles() []struct{ name, media, body string } {
	var rows, log strings.Builder
	rows.WriteString("day,region,orders,revenue,note\n")
	for i := range 3000 {
		fmt.Fprintf(&rows, "2026-%02d-%02d,%s,%d,%d.50,\"row %d, quoted\"\n", i%12+1, i%28+1, []string{"north", "south", "east", "west"}[i%4], 40+i%97, 900+i*7%4000, i+1)
	}
	for i := 0; log.Len() <= 3<<20/2; i++ {
		fmt.Fprintf(&log, "2026-11-01T09:%02d:%02d.000Z INFO  sync worker=%d synced %d files in %d ms, no conflicts\n", i/60%60, i%60, i%8, i%400, 12+i%300)
	}
	return []struct{ name, media, body string }{
		{"release-notes.md", "application/octet-stream", "# Release notes\n\nThe sync banner now says **what to do** when a sync fails.\n\n## Changes\n\n- A retry hint under the failure message\n- A focused test for the wording\n\n| Case | Message |\n| --- | --- |\n| Offline | Check your connection |\n| Busy | Try again in a minute |\n\n```ts\nexport const retrySafe = true;\n```\n\n> It is safe to try again.\n"},
		{"orders.csv", "text/csv", "day,region,orders,revenue\n2026-10-27,north,42,1280.50\n2026-10-28,south,57,1875.00\n2026-10-29,east,38,1104.25\n2026-10-30,west,61,2030.75\n2026-10-31,\"north, coastal\",49,1560.00\n"},
		{"orders-year.csv", "text/csv", rows.String()},
		{"regions.tsv", "text/tab-separated-values", "region\towner\tstores\nnorth\tMara\t12\nsouth\tTheo\t9\neast\tInes\t14\n"},
		{"status.json", "application/json", "{\n  \"status\": \"ok\",\n  \"checks\": [\"sync\", \"retry\"],\n  \"attempts\": 3\n}\n"},
		{"sync-worker.log", "text/plain", log.String()},
		{"chart.svg", "image/svg+xml", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 320 180"><rect width="320" height="180" fill="#1f2a44"/><path d="M20 150 90 95l60 25 70-70 80 30" fill="none" stroke="#7fb4ff" stroke-width="6"/></svg>`},
		{"summary.html", "text/html", "<!doctype html><title>Summary</title><h1>Weekly summary</h1><p>Twelve syncs failed and all of them recovered.</p><button onclick=\"this.textContent='Scripts run'\">Try a script</button>"},
		{"invoice.pdf", "application/pdf", "%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n4 0 obj<</Length 50>>stream\nBT /F1 24 Tf 72 700 Td (Invoice for October) Tj ET\nendstream endobj\n5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\ntrailer<</Root 1 0 R>>\n"},
		{"backup.zip", "application/zip", "PK\x05\x06" + strings.Repeat("\x00", 18)},
	}
}
