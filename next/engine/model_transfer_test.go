package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// useProfiles gives the service real profiles: test on model a, other on model b, spare on model c.
func useProfiles(t *testing.T, s *Service) *ProfileConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"test":{"provider":"anthropic","model":"a","allowedModels":["a"],"thinking":"low"},
	"other":{"provider":"openai","model":"b","allowedModels":["b"],"thinking":"high","compactTokens":150000},
	"spare":{"provider":"openai","model":"c","allowedModels":["c"],"thinking":"low"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	s.profiles = profiles
	return profiles
}

// nextTurn reads the session's next TurnStart.
func nextTurn(t *testing.T, s *Service, ctx context.Context, session contract.SessionID) contract.TurnStart {
	t.Helper()
	channelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	channel, problem := testChannel(t, s, channelCtx, contract.Principal{Kind: contract.PrincipalSession, Session: session})
	if problem != nil {
		t.Fatal(problem)
	}
	defer func() {
		cancel()
		for range channel {
		}
	}()
	for message := range channel {
		if start, ok := message.Body.(contract.TurnStart); ok {
			return start
		}
	}
	t.Fatal("the channel ended without a turn")
	return contract.TurnStart{}
}

// wake gives an idle session a reason to start a turn.
func wake(t *testing.T, s *Service, ctx context.Context, room contract.RoomID, session contract.SessionID) {
	t.Helper()
	home := contract.Conversation{Kind: contract.ConversationDM, Session: session}
	if _, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: home, Text: "next"}); problem != nil {
		t.Fatal(problem)
	}
}

func allowedModels(t *testing.T, s *Service, ctx context.Context, id contract.SessionID) []string {
	t.Helper()
	session, err := s.store.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := s.AllowedModels(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	return allowed
}

func TestTurnStartCarriesTheTargetProfile(t *testing.T) {
	s, _, session, ctx := setup(t)
	useProfiles(t, s)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: session, Profile: "other"}); problem != nil {
		t.Fatal(problem)
	}
	start := nextTurn(t, s, ctx, session)
	if start.Model.ID != "b" || start.CompactTokens != 150000 {
		t.Fatalf("turn start: %+v", start)
	}
}

// The next turn compacts on the model the session is leaving, so the route must still pass it. It closes once the
// session has moved on.
func TestSessionKeepsItsOldModelUntilItHasMoved(t *testing.T) {
	s, room, session, ctx := setup(t)
	useProfiles(t, s)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	end := func(turn contract.TurnID) {
		t.Helper()
		if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, Model: "a box can claim anything"}); problem != nil {
			t.Fatal(problem)
		}
	}
	first := nextTurn(t, s, ctx, session)
	end(first.Turn)
	if _, problem := s.SessionSetProfile(ctx, owner, contract.SessionSetProfileRequest{Session: session, Profile: "other"}); problem != nil {
		t.Fatal(problem)
	}
	if got := allowedModels(t, s, ctx, session); !slices.Contains(got, "a") || !slices.Contains(got, "b") {
		t.Fatalf("allowed after the switch: %v", got)
	}
	wake(t, s, ctx, room, session)
	second := nextTurn(t, s, ctx, session)
	if got := allowedModels(t, s, ctx, session); !slices.Contains(got, "a") {
		t.Fatalf("allowed during the transfer: %v", got)
	}
	end(second.Turn)
	wake(t, s, ctx, room, session)
	third := nextTurn(t, s, ctx, session)
	end(third.Turn)
	got := allowedModels(t, s, ctx, session)
	if slices.Contains(got, "a") || slices.Contains(got, "a box can claim anything") {
		t.Fatalf("allowed after the move: %v", got)
	}
}

// A replay keeps the model the turn really started on, even if the profile changed in between.
func TestDispatchedModelIsWrittenOnce(t *testing.T) {
	s, _, session, ctx := setup(t)
	useProfiles(t, s)
	start := nextTurn(t, s, ctx, session)
	if err := s.store.DispatchTurnModel(ctx, start.Turn, "b"); err != nil {
		t.Fatal(err)
	}
	models, err := s.store.RecentTurnModels(ctx, session)
	if err != nil || len(models) != 1 || models[0] != "a" {
		t.Fatalf("models %v err %v", models, err)
	}
}

func TestFailedSessionWakesOnTheNewProfile(t *testing.T) {
	s, _, session, ctx := setup(t)
	useProfiles(t, s)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	first := nextTurn(t, s, ctx, session)
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnFailed, Error: "overloaded", Cause: contract.CauseHarnessFailed}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.SessionSetProfile(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionSetProfileRequest{Session: session, Profile: "other"}); problem != nil {
		t.Fatal(problem)
	}
	retry := nextTurn(t, s, ctx, session)
	if retry.Turn == first.Turn || retry.Model.ID != "b" || retry.CompactTokens != 150000 {
		t.Fatalf("retry: %+v", retry)
	}
	if !slices.ContainsFunc(retry.Inputs, func(input contract.TurnInput) bool { return input.Delivery == first.Inputs[0].Delivery }) {
		t.Fatalf("the failed turn's input did not return: %+v", retry.Inputs)
	}
}

func TestTurnStoppedBeforeStartKeepsItsInputs(t *testing.T) {
	s, _, session, ctx := setup(t)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	first := nextTurn(t, s, ctx, session)
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnFailed, Cause: contract.CauseNotStarted}); problem == nil {
		t.Fatal("not_started on a failed turn was accepted")
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnStopped, Cause: contract.CauseHarnessFailed}); problem == nil {
		t.Fatal("a failure cause on a stopped turn was accepted")
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnStopped, Cause: contract.CauseNotStarted}); problem != nil {
		t.Fatal(problem)
	}
	pending := 0
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM deliveries WHERE to_id=? AND state='pending' AND turn IS NULL", session).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != len(first.Inputs) {
		t.Fatalf("pending %d, want %d", pending, len(first.Inputs))
	}
}

func openSessions(t *testing.T, s *Service, ctx context.Context, profile contract.ProfileName) []store.Session {
	t.Helper()
	sessions, err := s.store.SessionsOnProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	return sessions
}

func TestProfileRemoveMovesItsSessions(t *testing.T) {
	s, room, session, ctx := setup(t)
	profiles := useProfiles(t, s)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	moved := openSessions(t, s, ctx, "test")
	if len(moved) < 2 {
		t.Fatalf("sessions on the profile: %+v", moved)
	}
	if _, problem := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "test"}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("removal in use: %v", problem)
	}
	if _, problem := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "test", Replacement: "test"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("own replacement: %v", problem)
	}
	if _, problem := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "test", Replacement: "missing"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("unknown replacement: %v", problem)
	}
	if _, problem := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "missing", Replacement: "other"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("unknown profile: %v", problem)
	}
	if len(openSessions(t, s, ctx, "test")) != len(moved) {
		t.Fatal("a refused removal moved sessions")
	}

	if _, problem := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "test", Replacement: "other"}); problem != nil {
		t.Fatal(problem)
	}
	if _, _, err := profiles.Model("test"); err == nil {
		t.Fatal("the profile is still there")
	}
	if len(openSessions(t, s, ctx, "test")) != 0 || len(openSessions(t, s, ctx, "other")) != len(moved) {
		t.Fatal("the sessions did not move")
	}
	listed, problem := s.ProfileList(ctx, owner, contract.ProfileListRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, item := range listed.Profiles {
		if item.Name == "other" && item.InUse != len(moved) {
			t.Fatalf("listed: %+v", item)
		}
	}
	var changed int
	if err := s.store.Read().QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE room=? AND type='session.profile_changed' AND json_extract(body,'$.profile')='other'", room).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != len(moved) {
		t.Fatalf("events: %d, want %d", changed, len(moved))
	}
	start := nextTurn(t, s, ctx, session)
	if start.Model.ID != "b" {
		t.Fatalf("turn start: %+v", start)
	}
	// The profile is gone, and the session can still compact on its model.
	if got := allowedModels(t, s, ctx, session); !slices.Contains(got, "b") {
		t.Fatalf("allowed: %v", got)
	}
}

func TestProfileRemoveKeepsTheOldModelForTheTransfer(t *testing.T) {
	s, _, session, ctx := setup(t)
	useProfiles(t, s)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	first := nextTurn(t, s, ctx, session)
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.ProfileRemove(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.ProfileRemoveRequest{Name: "test", Replacement: "other"}); problem != nil {
		t.Fatal(problem)
	}
	if got := allowedModels(t, s, ctx, session); !slices.Contains(got, "a") || !slices.Contains(got, "b") {
		t.Fatalf("allowed: %v", got)
	}
}

func TestProfileRemoveWakesFailedSessions(t *testing.T) {
	s, _, session, ctx := setup(t)
	useProfiles(t, s)
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: session}
	first := nextTurn(t, s, ctx, session)
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnFailed, Error: "gone", Cause: contract.CauseHarnessFailed}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.ProfileRemove(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.ProfileRemoveRequest{Name: "test", Replacement: "other"}); problem != nil {
		t.Fatal(problem)
	}
	retry := nextTurn(t, s, ctx, session)
	if retry.Turn == first.Turn || retry.Model.ID != "b" {
		t.Fatalf("retry: %+v", retry)
	}
}

// An assignment that holds the profile while it commits finishes first, and the move then takes that session too.
func TestProfileReplaceWaitsForAssignment(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	profiles := useProfiles(t, s)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	session := store.Session{ID: "profile-race", Room: room, Name: "profile-race", Profile: "test", Box: "profile-race-box"}
	event, err := s.event(room, owner, contract.SessionStarted{Session: session.ID, Parent: root, Name: session.Name, Profile: "test"})
	if err != nil {
		t.Fatal(err)
	}
	boxEvent, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.BoxChanged{Box: session.Box, Session: session.ID, To: contract.BoxPending})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	started := make(chan *contract.Error, 1)
	go func() {
		started <- profiles.WithProfile("test", func() *contract.Error {
			close(entered)
			<-release
			if _, err := s.store.StartSession(ctx, session, root, contract.BoxSizeAgent, "", event, boxEvent, "", false, "", 0); err != nil {
				return failure(err)
			}
			return nil
		})
	}()
	<-entered
	removed := make(chan *contract.Error, 1)
	go func() {
		_, problem := s.ProfileRemove(ctx, owner, contract.ProfileRemoveRequest{Name: "test", Replacement: "other"})
		removed <- problem
	}()
	close(release)
	if problem := <-started; problem != nil {
		t.Fatal(problem)
	}
	if problem := <-removed; problem != nil {
		t.Fatal(problem)
	}
	got, err := s.store.Session(ctx, session.ID)
	if err != nil || got.Profile != "other" {
		t.Fatalf("session: %+v %v", got, err)
	}
}
