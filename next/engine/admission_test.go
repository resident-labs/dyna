package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestAdmissionNoticeOncePerPendingSpell(t *testing.T) {
	r := newGoalRig(t)
	r.exec("UPDATE boxes SET host='host' WHERE state='ready'")
	b, err := r.st.Box(r.ctx, mustSessionBox(t, r, r.child))
	if err != nil {
		t.Fatal(err)
	}
	r.exec("UPDATE boxes SET state='pending',reason='memory',host='host' WHERE id=?", b.ID)
	count := func() int {
		var n int
		if err := r.st.Read().QueryRowContext(r.ctx, "SELECT count(*) FROM events WHERE type='session.notice' AND client_id LIKE 'admission:%'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	run := func() {
		t.Helper()
		if err := r.s.AdmissionNotices(r.ctx); err != nil {
			t.Fatal(err)
		}
	}
	run()
	if count() != 0 {
		t.Fatal("notice too soon")
	}
	r.advance(5 * time.Minute)
	run()
	if count() != 2 {
		t.Fatalf("notices %d", count())
	}
	var text string
	if err := r.st.Read().QueryRowContext(r.ctx, "SELECT json_extract(body,'$.text') FROM events WHERE type='session.notice' LIMIT 1").Scan(&text); err != nil {
		t.Fatal(err)
	}
	t.Log("Rendered sample notice:\n" + text)
	if !strings.Contains(text, "of 256 GiB reserved (4× 64 GiB") || !strings.Contains(text, "/lead:") {
		t.Fatalf("wrong explanation: %s", text)
	}
	r.exec("UPDATE boxes SET reason='cpu',updated_at=? WHERE id=?", r.now().UnixMilli(), b.ID)
	run()
	r.restart()
	run()
	if count() != 2 {
		t.Fatal("repeated during same spell")
	}
	b, err = r.st.Box(r.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	event, err := r.s.engineEvent(r.room, contract.BoxChanged{})
	if err != nil {
		t.Fatal(err)
	}
	b, err = r.st.TransitionBox(r.ctx, b, contract.BoxCreating, "", event)
	if err != nil {
		t.Fatal(err)
	}
	event, err = r.s.engineEvent(r.room, contract.BoxChanged{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.st.TransitionBox(r.ctx, b, contract.BoxPending, "disk", event)
	if err != nil {
		t.Fatal(err)
	}
	run()
	if count() != 2 {
		t.Fatal("new spell too soon")
	}
	r.advance(5 * time.Minute)
	run()
	if count() != 4 {
		t.Fatalf("new spell not delivered: %d", count())
	}
}

func mustSessionBox(t *testing.T, r *goalRig, id contract.SessionID) contract.BoxID {
	t.Helper()
	s, err := r.st.Session(r.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return s.Box
}

func TestAdmissionHostSharedAccounting(t *testing.T) {
	h := box.Host{Name: "desktop", CPUs: 8, MemBytes: 26 * box.GiB, CIScale: 1}
	size, _ := box.Size("medium")
	b := box.Box{ID: "wait", Size: size, Reason: "memory"}
	held := box.Box{ID: "held", Host: h.Name, Size: contract.BoxSize{CPUs: 4, MemBytes: 92 * box.GiB}, State: contract.BoxReady}
	got := admissionHost(h, []box.Box{held}, b)
	if !strings.Contains(got, "92 GiB of 104 GiB reserved (4× 26 GiB") {
		t.Fatal(got)
	}
	fmt.Println(got)
}

func TestHostListReservationsAndFreeDisk(t *testing.T) {
	r := newGoalRig(t)
	r.exec("UPDATE hosts SET disk_bytes=?, disk_total=?,disk_floor=? WHERE name='host'", 111*box.GiB, 930*box.GiB, 93*box.GiB)
	for _, who := range []contract.Principal{ownerCaller, asSession(r.child)} {
		list, p := r.s.HostList(r.ctx, who, contract.HostListRequest{})
		if p != nil {
			t.Fatal(p)
		}
		for _, h := range list.Hosts {
			if h.Name != "host" {
				continue
			}
			if h.DiskFree != 111*box.GiB || h.DiskBytes != 930*box.GiB || h.Reservations == nil || h.Reservations.MemoryCap != 256*box.GiB {
				t.Fatalf("%+v", h)
			}
		}
	}
}
