package engine

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/browser"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestBrowserCheckoutCheckinConflictAndContext(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	dataDir := t.TempDir()
	r.s.SetLayout(box.Layout{Data: dataDir})

	// Non-owner cannot manage browser profiles.
	if _, problem := r.s.BrowserCreate(ctx, r.root, contract.BrowserCreateRequest{Name: "personal"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("non-owner create: %+v", problem)
	}
	if _, problem := r.s.BrowserList(ctx, r.worker, contract.BrowserListRequest{}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("non-owner list: %+v", problem)
	}

	// Owner creates profile and assigns it to r.room.
	if _, problem := r.s.BrowserCreate(ctx, r.owner, contract.BrowserCreateRequest{Name: "personal"}); problem != nil {
		t.Fatal(problem)
	}
	profileName := "personal"
	if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, Browser: &profileName}); problem != nil {
		t.Fatal(problem)
	}

	// dyna_room shows alone line first.
	roomSection := func(who contract.Principal) string {
		res, problem := r.s.ContextSnapshot(ctx, who, contract.ContextSnapshotRequest{Section: contract.ContextRoom})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, sec := range res.Sections {
			if sec.Key == contract.ContextRoom {
				return sec.Text
			}
		}
		return ""
	}
	if got := roomSection(r.worker); got != "Browser: personal; one session at a time" {
		t.Fatalf("dyna_room alone = %q", got)
	}

	// Assign same profile to a second room; dyna_room now lists the sibling room.
	room2, problem := r.s.RoomCreate(ctx, r.owner, contract.RoomCreateRequest{Name: "ops", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: room2.Room, Browser: &profileName}); problem != nil {
		t.Fatal(problem)
	}
	if got := roomSection(r.worker); got != "Browser: personal, shared with ops; one session at a time" {
		t.Fatalf("dyna_room shared = %q", got)
	}

	// Box A checks out the profile over BrowserSync.
	boxA := contract.BoxID("box_a")
	boxB := contract.BoxID("box_b")
	hA := r.s.BrowserSync(r.worker, boxA, r.room)
	hB := r.s.BrowserSync(r.root, boxB, r.room)

	rec := httptest.NewRecorder()
	hA.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/browser/checkout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout A status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Dyna-Browser-Profile") != "personal" {
		t.Fatalf("checkout profile header = %q", rec.Header().Get("X-Dyna-Browser-Profile"))
	}
	genA, err := strconv.ParseInt(rec.Header().Get("X-Dyna-Browser-Generation"), 10, 64)
	if err != nil || genA <= 1 {
		t.Fatalf("checkout gen = %q %v", rec.Header().Get("X-Dyna-Browser-Generation"), err)
	}

	// Box B checkout is refused with conflict naming holding session and room and how it frees.
	recB := httptest.NewRecorder()
	hB.ServeHTTP(recB, httptest.NewRequest(http.MethodPost, "/v1/browser/checkout", nil))
	if recB.Code != http.StatusConflict {
		t.Fatalf("checkout B status = %d, want 409", recB.Code)
	}
	errB := contract.ReadError(recB.Body)
	for _, want := range []string{"personal", "worker", "computer stop", "idle"} {
		if !strings.Contains(errB.Message, want) {
			t.Errorf("refusal %q missing %q", errB.Message, want)
		}
	}

	// Box A writes a cookie and checks in.
	workA := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workA, "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workA, "Default", "Cookies"), []byte("auth=secret-cookie"), 0600); err != nil {
		t.Fatal(err)
	}
	var tarBuf bytes.Buffer
	if err := browser.PackProfile(workA, &tarBuf); err != nil {
		t.Fatal(err)
	}
	recIn := httptest.NewRecorder()
	hA.ServeHTTP(recIn, httptest.NewRequest(http.MethodPost, "/v1/browser/checkin?profile=personal&generation="+strconv.FormatInt(genA, 10), bytes.NewReader(tarBuf.Bytes())))
	if recIn.Code != http.StatusOK {
		t.Fatalf("checkin A status = %d body=%s", recIn.Code, recIn.Body.String())
	}

	// Box B now checks out and receives the cookie from Box A.
	recB2 := httptest.NewRecorder()
	hB.ServeHTTP(recB2, httptest.NewRequest(http.MethodPost, "/v1/browser/checkout", nil))
	if recB2.Code != http.StatusOK {
		t.Fatalf("checkout B after checkin status = %d body=%s", recB2.Code, recB2.Body.String())
	}
	workB := t.TempDir()
	if err := browser.UnpackProfile(recB2.Body, workB); err != nil {
		t.Fatal(err)
	}
	gotCookie, err := os.ReadFile(filepath.Join(workB, "Default", "Cookies"))
	if err != nil || string(gotCookie) != "auth=secret-cookie" {
		t.Fatalf("box B cookie = %q %v", gotCookie, err)
	}
	genB, _ := strconv.ParseInt(recB2.Header().Get("X-Dyna-Browser-Generation"), 10, 64)

	// Owner runs release; Box B's late checkin is refused with 409 and does not overwrite engine copy.
	relRes, problem := r.s.BrowserRelease(ctx, r.owner, contract.BrowserReleaseRequest{Name: "personal"})
	if problem != nil || !relRes.Released {
		t.Fatalf("BrowserRelease = %+v %+v", relRes, problem)
	}
	if err := os.WriteFile(filepath.Join(workB, "Default", "Cookies"), []byte("stale-overwrite"), 0600); err != nil {
		t.Fatal(err)
	}
	var staleTar bytes.Buffer
	if err := browser.PackProfile(workB, &staleTar); err != nil {
		t.Fatal(err)
	}
	recLate := httptest.NewRecorder()
	hB.ServeHTTP(recLate, httptest.NewRequest(http.MethodPost, "/v1/browser/checkin?profile=personal&generation="+strconv.FormatInt(genB, 10), bytes.NewReader(staleTar.Bytes())))
	if recLate.Code != http.StatusConflict {
		t.Fatalf("late checkin status = %d, want 409", recLate.Code)
	}
	onDisk, err := os.ReadFile(filepath.Join(dataDir, "browsers", "personal", "Default", "Cookies"))
	if err != nil || string(onDisk) != "auth=secret-cookie" {
		t.Fatalf("profile overwritten after release: %q %v", onDisk, err)
	}

	// Second checkin rotates previous copy to .prev.
	recA2 := httptest.NewRecorder()
	hA.ServeHTTP(recA2, httptest.NewRequest(http.MethodPost, "/v1/browser/checkout", nil))
	genA2, _ := strconv.ParseInt(recA2.Header().Get("X-Dyna-Browser-Generation"), 10, 64)
	if err := os.WriteFile(filepath.Join(workA, "Default", "Cookies"), []byte("auth=v2"), 0600); err != nil {
		t.Fatal(err)
	}
	var v2Tar bytes.Buffer
	if err := browser.PackProfile(workA, &v2Tar); err != nil {
		t.Fatal(err)
	}
	recIn2 := httptest.NewRecorder()
	hA.ServeHTTP(recIn2, httptest.NewRequest(http.MethodPost, "/v1/browser/checkin?profile=personal&generation="+strconv.FormatInt(genA2, 10), bytes.NewReader(v2Tar.Bytes())))
	if recIn2.Code != http.StatusOK {
		t.Fatalf("checkin v2 status = %d", recIn2.Code)
	}
	prevCookie, err := os.ReadFile(filepath.Join(dataDir, "browsers", "personal.prev", "Default", "Cookies"))
	if err != nil || string(prevCookie) != "auth=secret-cookie" {
		t.Fatalf(".prev cookie = %q %v", prevCookie, err)
	}
	curCookie, err := os.ReadFile(filepath.Join(dataDir, "browsers", "personal", "Default", "Cookies"))
	if err != nil || string(curCookie) != "auth=v2" {
		t.Fatalf("current cookie = %q %v", curCookie, err)
	}
}

type fakeBrowserVolumes struct {
	*fakeBoxes
	tarData []byte
	found   bool
	pullErr error
	removed []contract.BoxID
}

func (f *fakeBrowserVolumes) PullBrowserProfile(_ context.Context, _ contract.HostName, _ contract.BoxID, dst io.Writer) (bool, error) {
	if f.pullErr != nil {
		return false, f.pullErr
	}
	if !f.found {
		return false, nil
	}
	_, err := dst.Write(f.tarData)
	return true, err
}

func (f *fakeBrowserVolumes) RemoveBrowserProfile(_ context.Context, _ contract.HostName, id contract.BoxID) error {
	f.removed = append(f.removed, id)
	return nil
}

func TestRecoverBrowserLeaseFromDeadBoxVolume(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	ctx := t.Context()

	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Default", "Cookies"), []byte("recovered=1"), 0600); err != nil {
		t.Fatal(err)
	}
	var tarBuf bytes.Buffer
	if err := browser.PackProfile(src, &tarBuf); err != nil {
		t.Fatal(err)
	}

	vols := &fakeBrowserVolumes{fakeBoxes: &fakeBoxes{}, tarData: tarBuf.Bytes(), found: true}
	dataDir := t.TempDir()
	svc := New(st, vols, fakeProfiles{}, func() time.Time { return now })
	svc.SetLayout(box.Layout{Data: dataDir})

	room := contract.RoomID("room_1")
	if err := st.CreateRoom(ctx, room, "work", now); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, p := svc.BrowserCreate(ctx, owner, contract.BrowserCreateRequest{Name: "shared"}); p != nil {
		t.Fatal(p)
	}
	if err := st.SetRoomBrowser(ctx, room, "shared"); err != nil {
		t.Fatal(err)
	}
	boxID := contract.BoxID("box_dead")
	sesID := contract.SessionID("ses_dead")
	ev := contract.Event{ID: "e_dead", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionStarted{}}
	boxEv := contract.Event{ID: "be_dead", Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{}}
	if _, err := st.StartSession(ctx, store.Session{ID: sesID, Room: room, Name: "worker", Profile: "sol", Box: boxID}, "", contract.BoxSizeAgent, "", ev, boxEv, "", false, "", "", 0); err != nil {
		t.Fatal(err)
	}
	_, gen, _, err := st.BrowserCheckout(ctx, boxID, sesID, room, now)
	if err != nil {
		t.Fatal(err)
	}

	lease := store.BrowserLeaseRow{Profile: "shared", Box: boxID, Session: sesID, Generation: gen}
	if err := svc.RecoverBrowserLease(ctx, lease, "host1", true); err != nil {
		t.Fatalf("RecoverBrowserLease: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, "browsers", "shared", "Default", "Cookies"))
	if err != nil || string(got) != "recovered=1" {
		t.Fatalf("recovered cookie = %q %v", got, err)
	}
	if len(vols.removed) != 1 || vols.removed[0] != boxID {
		t.Fatalf("volume cleanup not called: %v", vols.removed)
	}
	holds, err := st.BoxHoldsBrowserLease(ctx, boxID)
	if err != nil || holds {
		t.Fatalf("lease still held after recovery: holds=%v err=%v", holds, err)
	}
}
