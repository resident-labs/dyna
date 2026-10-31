package main

import (
	"bytes"

	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
)

func TestVisualCheckOutput(t *testing.T) {
	var out bytes.Buffer
	printVisualChecks(&out, []contract.VisualCheck{{Name: contract.VisualCheckRenders, Status: contract.VisualCheckPass}, {Name: contract.VisualCheckWidths, Status: contract.VisualCheckWarn, Detail: "360"}})
	out.WriteString("/tmp/banner.webp\n")
	if got, want := out.String(), "renders: pass \nwidths: warn 360\n/tmp/banner.webp\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVisualFlagsAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		doc     string
		height  int
		wantErr bool
	}{
		{"default", "<title>Chart</title>", 360, false},
		{"low", `<meta name="dyna-height" content="1">`, 120, false},
		{"high", `<meta name="dyna-height" content="1201">`, 1200, false},
		{"requested", `<meta name="dyna-height" content="480">`, 480, false},
		{"refresh", `<meta http-equiv="refresh" content="0">`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, height, err := visualMeta("test.html", []byte(tc.doc))
			if (err != nil) != tc.wantErr || height != tc.height {
				t.Fatalf("height=%d err=%v", height, err)
			}
		})
	}
	title, _, err := visualMeta("test.html", []byte("<title>"+strings.Repeat("漢", 86)+"</title>"))
	if err != nil || title != strings.Repeat("漢", 85) {
		t.Fatalf("multibyte title: %q, err=%v", title, err)
	}
	for _, args := range [][]string{
		{"post", "text", "--update", "ev_1"},
		{"post", "text", "--allow-net"},
		{"post", " ", "--html", "test.html"},
	} {
		_, err := execCLI(&fakeClient{}, args...)
		if err == nil {
			t.Fatalf("accepted %s", strings.Join(args, " "))
		}
	}
}
