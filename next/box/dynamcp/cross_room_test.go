package dynamcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box/post"
	"github.com/resident-labs/dyna/contract"
)

func TestCrossRoomPostRefusesFilesAndEndTurnBeforePreparation(t *testing.T) {
	server, err := New(func(context.Context, contract.Method, []byte) ([]byte, *contract.Error) {
		t.Fatal("forwarded refused post")
		return nil, nil
	}, nil, post.Preparer{Room: "room-a", Upload: func(context.Context, string) (contract.Attachment, error) {
		t.Fatal("uploaded refused post")
		return contract.Attachment{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ args, code string }{
		{`"paths":["missing"]`, string(contract.ErrCrossRoomAttachment)},
		{`"html":"missing"`, string(contract.ErrCrossRoomAttachment)},
		{`"endTurn":true`, string(contract.ErrForbidden)},
	} {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"post","arguments":{"room":"room-b","text":"hi","mentions":["root"],` + tc.args + `}}}`
		w := httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/dyna/", strings.NewReader(body)))
		if !strings.Contains(w.Body.String(), tc.code) || !strings.Contains(w.Body.String(), `"isError":true`) {
			t.Fatalf("response: %s", w.Body.String())
		}
	}
}
