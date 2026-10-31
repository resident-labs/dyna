package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestEventsSkipsHelloEvent(t *testing.T) {
	event, err := json.Marshal(contract.Event{Seq: 3, Body: contract.PostCreated{Text: "third"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: hello\ndata: {\"build\":\"b\"}\n\n")
		fmt.Fprintf(w, "id: 3\ndata: %s\n\n", event)
	}))
	defer srv.Close()
	client := &socketClient{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}}}}
	var got []contract.Event
	if err := client.Events(context.Background(), "r", 0, func(e contract.Event) error {
		got = append(got, e)
		return nil
	}); err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(got) != 1 || got[0].Seq != 3 || got[0].Body.(contract.PostCreated).Text != "third" {
		t.Fatalf("events = %+v, want only the third post", got)
	}
}
