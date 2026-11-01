package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	hostbox "github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/store"
)

type questionService struct {
	*fakeService
	core *engine.Service
}

func (s questionService) QuestionAsk(ctx context.Context, caller contract.Principal, req contract.QuestionAskRequest) (contract.QuestionAskResponse, *contract.Error) {
	return s.core.QuestionAsk(ctx, caller, req)
}
func (s questionService) QuestionAnswer(ctx context.Context, caller contract.Principal, req contract.QuestionAnswerRequest) (contract.QuestionAnswerResponse, *contract.Error) {
	return s.core.QuestionAnswer(ctx, caller, req)
}
func (s questionService) QuestionList(ctx context.Context, caller contract.Principal, req contract.QuestionListRequest) (contract.QuestionListResponse, *contract.Error) {
	return s.core.QuestionList(ctx, caller, req)
}

// A question crosses the API twice: a box asks over its socket, and the owner answers from a device.
func TestQuestionThroughTheAPI(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.AddHost(ctx, hostbox.Host{Name: "host", State: hostbox.HostReady, CPUs: 32, MemBytes: 64 * hostbox.GiB, DiskBytes: 100 * hostbox.GiB, CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	core := engine.New(st, nil, questionProfiles{}, time.Now)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	created, problem := core.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "room", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	var root contract.SessionID
	if err := st.Read().QueryRowContext(ctx, "SELECT id FROM sessions WHERE room=? AND parent IS NULL", created.Room).Scan(&root); err != nil {
		t.Fatal(err)
	}
	worker, problem := core.SessionStart(ctx, owner, contract.SessionStartRequest{Room: created.Room, Name: "worker", Profile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	a := New(questionService{fakeService: &fakeService{}, core: core}, nil)
	rootBox := a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: root}, Box: "box-root", Room: created.Room})
	workerBox := a.handler(Caller{Principal: contract.Principal{Kind: contract.PrincipalSession, Session: worker.Session}, Box: "box-worker", Room: created.Room})
	operator := a.handler(Caller{Principal: owner, Web: true, Scopes: []contract.Scope{contract.ScopeOperate}})
	reader := a.handler(Caller{Principal: owner, Web: true, Scopes: []contract.Scope{contract.ScopeRead}})

	refused := request(workerBox, "/v1/question.ask", `{"text":"may I?"}`)
	if refused.Code != http.StatusForbidden {
		t.Fatalf("no grant: %d %s", refused.Code, refused.Body.String())
	}
	var problemBody contract.Error
	if err := json.Unmarshal(refused.Body.Bytes(), &problemBody); err != nil || problemBody.Code != contract.ErrNoGrant {
		t.Fatalf("no grant body: %s", refused.Body.String())
	}
	if got := request(operator, "/v1/question.ask", `{"text":"owner"}`); got.Code != http.StatusForbidden {
		t.Fatalf("owner asked: %d", got.Code)
	}
	asked := request(rootBox, "/v1/question.ask", `{"text":"Which database?"}`)
	if asked.Code != http.StatusOK {
		t.Fatalf("ask: %d %s", asked.Code, asked.Body.String())
	}
	var response contract.QuestionAskResponse
	if err := json.Unmarshal(asked.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	listed := request(reader, "/v1/question.list", `{"room":"`+string(created.Room)+`"}`)
	var list contract.QuestionListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil || listed.Code != http.StatusOK || len(list.Questions) != 1 {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	body := `{"id":"` + string(response.Question.ID) + `","text":"Postgres"}`
	if got := request(reader, "/v1/question.answer", body); got.Code != http.StatusForbidden {
		t.Fatalf("read scope answered: %d", got.Code)
	}
	if got := request(workerBox, "/v1/question.answer", body); got.Code != http.StatusForbidden {
		t.Fatalf("a box answered: %d", got.Code)
	}
	if got := request(operator, "/v1/question.answer", body); got.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", got.Code, got.Body.String())
	}
	if got := request(operator, "/v1/question.answer", body); got.Code != http.StatusConflict {
		t.Fatalf("second answer: %d", got.Code)
	}
	var pending int
	if err := st.Read().QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE to_id=? AND state='pending' AND wake=1 AND reply_kind='final'", root).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("waking deliveries: %d %v", pending, err)
	}
}

type questionProfiles struct{}

func (questionProfiles) List() []contract.ProfileSummary           { return nil }
func (questionProfiles) CompactionTokens(contract.ProfileName) int { return 0 }
func (questionProfiles) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return contract.ModelChoice{}, nil, nil
}
