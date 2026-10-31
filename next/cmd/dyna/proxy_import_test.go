package main

import (
	"context"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

func TestLegacyProxyImportRemovesFilesAfterCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invented.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude","access_token":"invented-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, client := net.Pipe()
	r := rpc.NewServer()
	if err := r.RegisterName("Auth", &model.AuthRPC{Store: db}); err != nil {
		t.Fatal(err)
	}
	go r.ServeConn(server)
	p := &proxyStore{client: rpc.NewClient(client)}
	defer p.client.Close()
	if err := p.importLegacy(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("import left credential file")
	}
	if err := p.importLegacy(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	auths, err := p.List(context.Background())
	if err != nil || len(auths) != 1 || auths[0].Metadata["access_token"] != "invented-token" {
		t.Fatal("import lost credential")
	}
	var label string
	if err := db.Read().QueryRow("SELECT label FROM accounts").Scan(&label); err != nil || label != "Imported Claude 1" {
		t.Fatal("imported label is not generic")
	}
}
