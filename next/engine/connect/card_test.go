package connect

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCardListenerDoesNotReachApp(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("card request reached app handler")
	}))
	defer app.Close()
	listener, server, err := CardListener()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go server.Serve(listener)
	if _, err := net.Listen("tcp", listener.Addr().String()); err == nil {
		t.Fatal("card port was not reserved")
	}
	response, err := http.Get("http://" + listener.Addr().String() + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("card status = %d, want 404", response.StatusCode)
	}
}
