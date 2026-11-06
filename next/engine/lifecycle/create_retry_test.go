package lifecycle

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type otherHostBoxes struct {
	runningBoxes
	hosts                     []contract.HostName
	alternateEntered, release chan struct{}
}

func (b *otherHostBoxes) Create(ctx context.Context, spec BoxSpec) (string, error) {
	b.hosts = append(b.hosts, spec.Host)
	if spec.Host == "h" {
		return "", box.StepTimeout{Host: spec.Host, Step: contract.StartBinary}
	}
	if b.alternateEntered != nil {
		close(b.alternateEntered)
		<-ctx.Done()
		<-b.release
	}
	return "alternate-image", nil
}
func prepareOtherHostRetry(t *testing.T, boxes *otherHostBoxes) *hostRig {
	t.Helper()
	rig := newHostRig(t)
	rig.r.boxes = boxes
	rig.view.connected["s"] = false
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) SELECT 'spare',ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at FROM hosts WHERE name='h';
UPDATE boxes SET state='creating',image='',failures=2`)
		if err != nil {
			return err
		}
		_, err = rig.st.AppendEvent(tx, contract.Event{ID: "retry-initial-placement", Room: "r", Time: rig.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return rig
}
func TestCreateRetryPlacesOnceOnDifferentEligibleHost(t *testing.T) {
	boxes := &otherHostBoxes{}
	rig := prepareOtherHostRetry(t, boxes)
	if b := rig.pass(); b.State != contract.BoxPending || b.Host != "" {
		t.Fatalf("first failure did not queue alternate: %+v", b)
	}
	if b := rig.pass(); b.Host != "spare" || b.Image != "alternate-image" {
		t.Fatalf("alternate placement failed: %+v", b)
	}
	if len(boxes.hosts) != 2 || boxes.hosts[0] != "h" || boxes.hosts[1] != "spare" {
		t.Fatalf("unexpected attempts: %v", boxes.hosts)
	}
}
func TestCreateRetryFailsClearlyWhenNoOtherHostIsEligible(t *testing.T) {
	for _, scenario := range []string{"absent", "degraded", "room", "memory", "gpu", "exec-only", "room limit"} {
		t.Run(scenario, func(t *testing.T) {
			boxes := &otherHostBoxes{}
			rig := prepareOtherHostRetry(t, boxes)
			if b := rig.pass(); b.State != contract.BoxPending {
				t.Fatalf("first failure: %+v", b)
			}
			query := ""
			switch scenario {
			case "absent":
				query = "DELETE FROM hosts WHERE name='spare'"
			case "degraded":
				rig.r.SetHostHealth(func(name contract.HostName) contract.HostHealth {
					if name == "spare" {
						return contract.HostDegraded
					}
					return contract.HostHealthy
				})
			case "room":
				query = `UPDATE rooms SET only_hosts='["h"]'`
			case "memory":
				query = "UPDATE hosts SET mem_bytes=1 WHERE name='spare'"
			case "gpu":
				query = "UPDATE boxes SET gpu_gib=8"
			case "exec-only":
				query = "UPDATE hosts SET exec_only=1,agents=0,ci=0 WHERE name='spare'"
			case "room limit":
				query = "UPDATE rooms SET mem_bytes=1"
			}
			if query != "" {
				if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(query); return err }); err != nil {
					t.Fatal(err)
				}
			}
			b := rig.pass()
			if b.State != contract.BoxFailed || !strings.Contains(b.Reason, "no other eligible host") || !strings.Contains(b.Reason, "h") {
				t.Fatalf("unclear failure for %s: %+v", scenario, b)
			}
			if len(boxes.hosts) != 1 {
				t.Fatalf("ineligible host used: %v", boxes.hosts)
			}
		})
	}
}
func TestStopAndCloseWinDuringOtherHostRetry(t *testing.T) {
	for _, want := range []string{"stop", "close"} {
		t.Run(want, func(t *testing.T) {
			boxes := &otherHostBoxes{alternateEntered: make(chan struct{}), release: make(chan struct{})}
			rig := prepareOtherHostRetry(t, boxes)
			rig.pass()
			if err := rig.r.Pass(rig.ctx); err != nil {
				t.Fatal(err)
			}
			<-boxes.alternateEntered
			if want == "stop" {
				id, accepted, err := rig.st.RequestStartStop(rig.ctx, "s", rig.now)
				if err != nil || !accepted {
					t.Fatalf("stop acceptance: %v %v", accepted, err)
				}
				rig.r.CancelStart(id)
			} else {
				if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE boxes SET want='close'; UPDATE sessions SET state='closing'")
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := rig.r.Pass(rig.ctx); err != nil {
					t.Fatal(err)
				}
			}
			close(boxes.release)
			rig.r.Wait()
			b := rig.pass()
			if want == "stop" && b.State != contract.BoxStopped || want == "close" && b.State != contract.BoxDeleted {
				t.Fatalf("%s lost to late create: %+v", want, b)
			}
			if b.Image != "" {
				t.Fatalf("late result was committed: %+v", b)
			}
			if len(boxes.hosts) != 2 {
				t.Fatalf("retry loop after %s: %v", want, boxes.hosts)
			}
		})
	}
}
func TestStoppedBeforeCreateCanWake(t *testing.T) {
	rig := newHostRig(t)
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='pending',image='',want='run'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	id, accepted, err := rig.st.RequestStartStop(rig.ctx, "s", time.Now())
	if err != nil || !accepted {
		t.Fatalf("stop: %v %v", accepted, err)
	}
	rig.r.CancelStart(id)
	if b := rig.pass(); b.State != contract.BoxStopped {
		t.Fatalf("not stopped: %+v", b)
	}
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE boxes SET want='run'"); return err }); err != nil {
		t.Fatal(err)
	}
	if b := rig.pass(); b.Image == "" || b.Host != "h" {
		t.Fatalf("never-created box did not wake: %+v", b)
	}
}

type failEveryRetryHost struct {
	runningBoxes
	hosts []contract.HostName
}

func (b *failEveryRetryHost) Create(ctx context.Context, spec BoxSpec) (string, error) {
	b.hosts = append(b.hosts, spec.Host)
	return "", box.StepTimeout{Host: spec.Host, Step: contract.StartBinary}
}

func TestQuarantineCannotGiveRetryAThirdHost(t *testing.T) {
	rig := prepareOtherHostRetry(t, &otherHostBoxes{})
	boxes := &failEveryRetryHost{}
	rig.r.boxes = boxes
	rig.pass()           // Terminal failure on original h, queues its one retry.
	second := rig.pass() // First nonterminal create error on spare.
	if second.Host != "spare" || second.State != contract.BoxPending {
		t.Fatalf("unexpected second allocation: %+v", second)
	}
	if err := rig.st.QuarantineShare(rig.ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) SELECT 'third',ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at FROM hosts WHERE name='h'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rig.now = rig.now.Add(31 * time.Second)
	after := rig.pass()
	if len(boxes.hosts) != 2 || after.Host != "spare" || after.State != contract.BoxFailed {
		t.Fatalf("one-other-host budget did not terminate on its selected second host: %v; final box %+v", boxes.hosts, after)
	}
}
