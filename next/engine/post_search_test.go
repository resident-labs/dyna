package engine

import (
	"github.com/resident-labs/dyna/contract"
	"strings"
	"testing"
)

func TestPostSearch(t *testing.T) {
	s, room, worker, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	var seq []int64
	for _, text := range []string{"redeploy blue", "auto-deploy BLUE", "deploys red", "DEPLOY blue"} {
		p, e := s.Post(ctx, owner, contract.PostRequest{Room: room, Text: text})
		if e != nil {
			t.Fatal(e)
		}
		seq = append(seq, p.Seq)
	}
	for _, tc := range []struct {
		query  string
		before int64
		want   []int64
		total  int
	}{
		{"deploy blue", 0, []int64{seq[3], seq[1]}, 2},
		{"DEPLOY", seq[3], []int64{seq[2], seq[1]}, 3},
		{"blue missing", 0, nil, 0},
	} {
		r, e := s.PostSearch(ctx, owner, contract.PostSearchRequest{Query: tc.query, Before: tc.before})
		if e != nil {
			t.Fatal(e)
		}
		if r.Total != tc.total || len(r.Posts) != len(tc.want) {
			t.Fatalf("%q: %+v", tc.query, r)
		}
		for i, n := range tc.want {
			if r.Posts[i].Seq != n {
				t.Fatalf("%q: %+v", tc.query, r)
			}
		}
	}
	other, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Post(ctx, owner, contract.PostRequest{Room: other.Room, Text: "deploy blue"}); e != nil {
		t.Fatal(e)
	}
	all, e := s.PostSearch(ctx, owner, contract.PostSearchRequest{Query: "deploy blue"})
	if e != nil || all.Total != 3 || all.Posts[0].Room != other.Room {
		t.Fatalf("all rooms: %+v, %v", all, e)
	}
	one, e := s.PostSearch(ctx, owner, contract.PostSearchRequest{Query: "deploy blue", Room: room, Limit: 1})
	if e != nil || one.Total != 2 || len(one.Posts) != 1 {
		t.Fatalf("one room: %+v, %v", one, e)
	}
	_, e = s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostRequest{Room: room, Text: "blue"})
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.PostSearch(ctx, owner, contract.PostSearchRequest{Query: "work blue", Limit: 1})
	if e != nil || result.Total != 1 || len(result.Posts) != 1 || result.Posts[0].Author.Session != worker {
		t.Fatalf("author search: %+v, %v", result, e)
	}
	if _, e := s.PostSearch(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostSearchRequest{Query: "deploy"}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("session: %v", e)
	}
}
func TestPostSearchLimits(t *testing.T) {
	s, _, _, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	for _, req := range []contract.PostSearchRequest{{Query: " "}, {Query: strings.Repeat("a", 201)}, {Query: "a b c d e f g h i"}, {Query: "ok", Limit: 51}, {Query: "ok", Before: -1}} {
		if _, e := s.PostSearch(ctx, owner, req); e == nil || e.Code != contract.ErrInvalid {
			t.Fatalf("%+v: %v", req, e)
		}
	}
}
