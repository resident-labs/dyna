//go:build linux

package computers

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestBrowserOpenSavesIntentAndWaitsForHumanControl(t *testing.T) {
	browserTestEnvironment(t)
	id := "browser-intent"
	if err := os.MkdirAll(Dir(id), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &supervisor{meta: Meta{SessionID: id}, state: Status{SessionID: id, Control: ControlHuman}, inputGate: make(chan struct{}, 1), compositor: &process{done: make(chan struct{})}}
	s.emit = func(event Event) {
		if event.Type == "status" {
			cancel()
		}
	}
	_, err := s.openBrowser(ctx, Request{Status: "  Checking the example page  ", BrowserArgs: []string{"https://example.test"}})
	if err == nil || !strings.Contains(err.Error(), "human has control") {
		t.Fatalf("browser ignored human control: %v", err)
	}
	saved := savedStatus(id)
	if saved.LatestStatus == nil || saved.LatestStatus.Text != "Checking the example page" || saved.LatestStatus.At.IsZero() {
		t.Fatalf("saved intent: %+v", saved)
	}
}

func TestBrowserOpenRejectsInvalidRequestBeforeSavingIntent(t *testing.T) {
	for _, request := range []Request{
		{BrowserArgs: []string{"https://example.test"}},
		{Status: "Checking a page", BrowserArgs: []string{"--user-data-dir=/other"}},
		{Status: "Checking a page", BrowserArgs: []string{"relative/path"}},
		{Status: "Checking a page"},
		{Status: "Checking a page", BrowserArgs: []string{"https://one.test", "https://two.test"}},
	} {
		s := &supervisor{}
		if _, err := s.openBrowser(context.Background(), request); err == nil {
			t.Fatalf("accepted invalid browser request: %+v", request)
		}
		if s.state.LatestStatus != nil {
			t.Fatal("invalid request changed the intent")
		}
	}
}
