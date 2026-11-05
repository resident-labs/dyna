package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const digestImage = "ghcr.io/o/r@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const digestImage2 = "ghcr.io/o/r@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type serviceHosts struct{ h *fakehost.Host }

func (s serviceHosts) ServiceDriver(context.Context, contract.HostName) (box.ServiceDriver, error) {
	return s.h, nil
}
func (s serviceHosts) ServiceRoomImage(context.Context, contract.HostName, contract.RoomID, string) (string, error) {
	return "localhost/room-image:1", nil
}

type serviceRig struct {
	*hostRig
	host   *fakehost.Host
	id     contract.ServiceID
	n      int
	events []contract.EventBody
}

func newServiceRig(t *testing.T) *serviceRig {
	t.Helper()
	rig := &serviceRig{hostRig: newHostRig(t), host: fakehost.New()}
	rig.host.Service.Check = box.ServiceHostCheck{LowestPort: 443}
	inner := rig.r.event
	rig.r.event = func(room contract.RoomID, from contract.Principal, body contract.EventBody) (contract.Event, error) {
		rig.events = append(rig.events, body)
		return inner(room, from, body)
	}
	rig.r.SetServices(serviceHosts{rig.host}, ServiceTimings{Start: 50 * time.Millisecond, Stable: 20 * time.Millisecond, Health: 50 * time.Millisecond, Poll: time.Millisecond, CheckEvery: time.Hour})
	return rig
}

func (rig *serviceRig) declare(spec contract.ServiceSpec) store.ServiceRow {
	rig.t.Helper()
	parts, err := spec.Parse()
	if err != nil {
		rig.t.Fatal(err)
	}
	rig.n++
	row, err := rig.st.SetService(rig.ctx, "r", parts, contract.ServiceID(fmt.Sprintf("svc_%d", rig.n)), contract.Principal{Kind: contract.PrincipalOwner}, rig.now, rig.r.serviceEvent)
	if err != nil {
		rig.t.Fatal(err)
	}
	rig.id = row.ID
	return row
}

func (rig *serviceRig) row() store.ServiceRow {
	rig.t.Helper()
	rows, err := rig.st.Services(rig.ctx, "r")
	if err != nil || len(rows) == 0 {
		rig.t.Fatalf("services: %v %v", rows, err)
	}
	return rows[0]
}

// settle runs passes until the service's state stops changing.
func (rig *serviceRig) settle() store.ServiceRow {
	rig.t.Helper()
	for range 6 {
		rig.pass()
	}
	return rig.row()
}

func web() contract.ServiceSpec {
	return contract.ServiceSpec{Name: "web", Image: digestImage, Ports: []contract.ServicePort{"443"}}
}

func (rig *serviceRig) failures() []contract.ServiceFailure {
	rig.t.Helper()
	var out []contract.ServiceFailure
	for _, e := range rig.events {
		if f, ok := e.(contract.ServiceFailure); ok {
			out = append(out, f)
		}
	}
	return out
}

func TestDeclaredServiceRunsOnAFakeHost(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	if row.State != contract.ServiceRunning || row.Host != "h" || row.ImageRef != digestImage || row.Deployed != row.Generation {
		t.Fatalf("service %+v", row.Service)
	}
	file := rig.host.Service.Files[row.ID]
	if file == "" || row.UnitSHA == "" {
		t.Fatal("no quadlet written")
	}
	if !rig.host.Service.Slice {
		t.Fatal("service slice not written")
	}
}

func TestEngineRestartWithMatchingUnitSHARestartsNothing(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	rig.settle()
	restarts := count(rig.host.Calls, fakehost.RestartService)
	writes := count(rig.host.Calls, fakehost.WriteServiceUnit)
	// A new reconciler, as after an engine restart, finds the file with the hash the row holds.
	rig.r.SetServices(serviceHosts{rig.host}, rig.r.svc.timings)
	rig.settle()
	if got := count(rig.host.Calls, fakehost.RestartService); got != restarts {
		t.Fatalf("restarts %d, want %d", got, restarts)
	}
	if got := count(rig.host.Calls, fakehost.WriteServiceUnit); got != writes {
		t.Fatalf("unit writes %d, want %d", got, writes)
	}
}

func TestEngineRestartWithDifferingFileWritesItAgain(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	rig.host.Service.Files[row.ID] += "# edited by hand\n"
	rig.r.SetServices(serviceHosts{rig.host}, rig.r.svc.timings)
	rig.settle()
	if strings.Contains(rig.host.Service.Files[row.ID], "edited by hand") {
		t.Fatal("a differing file was left")
	}
}

func TestUnreachableHostShowsUnreachableAndMovesNothing(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	rig.settle()
	rig.setHost(box.HostUnreachable)
	row := rig.settle()
	if row.State != contract.ServiceUnreachable || row.Reason != contract.ServiceHostUnreachable || row.Host != "h" || row.LastState != contract.ServiceRunning {
		t.Fatalf("service %+v", row.Service)
	}
	rig.setHost(box.HostReady)
	if row = rig.settle(); row.State != contract.ServiceRunning || row.Host != "h" {
		t.Fatalf("after return %+v", row.Service)
	}
}

func TestFailingHealthCheckRollsBackToThePreviousImage(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	spec.Health = []string{"/bin/true"}
	rig.declare(spec)
	rig.settle()
	rig.host.Service.Health = func(id contract.ServiceID, file string) error {
		if strings.Contains(file, "bbbbbbbb") {
			return errors.New("unhealthy")
		}
		return nil
	}
	spec.Image = digestImage2
	rig.declare(spec)
	row := rig.settle()
	if row.State != contract.ServiceRunning || row.Reason != contract.ServiceRolledBack || row.Cause != contract.ServiceHealthFailed || row.ImageRef != digestImage {
		t.Fatalf("service %+v ref %s", row.Service, row.ImageRef)
	}
	if !strings.Contains(rig.host.Service.Files[row.ID], digestImage) || strings.Contains(rig.host.Service.Files[row.ID], "bbbbbbbb") {
		t.Fatalf("quadlet holds the failed image:\n%s", rig.host.Service.Files[row.ID])
	}
	got := rig.failures()
	if len(got) != 1 || got[0].Reason != contract.ServiceRolledBack || got[0].Cause != contract.ServiceHealthFailed {
		t.Fatalf("failures %+v", got)
	}
}

func TestFirstDeployThatFailsIsFailedWithTheStepsReason(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(*serviceRig)
		reason  contract.ServiceReason
	}{
		{"image_pull", func(r *serviceRig) { r.host.Service.Fail[fakehost.PullService] = errors.New("denied") }, contract.ServiceImagePull},
		{"port_in_use", func(r *serviceRig) {
			r.host.Service.Listening = []contract.PortBinding{{Host: 443, Proto: contract.PortTCP}}
		}, contract.ServicePortInUse},
		{"port_privileged", func(r *serviceRig) { r.host.Service.Check.LowestPort = 1024 }, contract.ServicePortPrivileged},
		{"unit_failed", func(r *serviceRig) { r.host.Service.Fail[fakehost.WriteServiceUnit] = errors.New("disk full") }, contract.ServiceUnitFailed},
		{"start_failed", func(r *serviceRig) { r.host.Service.Fail[fakehost.RestartService] = errors.New("no") }, contract.ServiceStartFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig := newServiceRig(t)
			c.arrange(rig)
			rig.declare(web())
			row := rig.settle()
			if row.State != contract.ServiceFailed || row.Reason != c.reason {
				t.Fatalf("service %+v", row.Service)
			}
			if got := rig.failures(); len(got) != 1 || got[0].Reason != c.reason {
				t.Fatalf("failures %+v", got)
			}
		})
	}
}

func TestRoomImageBuildFailureIsImageBuild(t *testing.T) {
	rig := newServiceRig(t)
	rig.r.svc.hosts = failingBuild{serviceHosts{rig.host}}
	spec := web()
	spec.Image = "app"
	rig.declare(spec)
	if row := rig.settle(); row.State != contract.ServiceFailed || row.Reason != contract.ServiceImageBuild {
		t.Fatalf("service %+v", row.Service)
	}
}

type failingBuild struct{ serviceHosts }

func (failingBuild) ServiceRoomImage(context.Context, contract.HostName, contract.RoomID, string) (string, error) {
	return "", errors.New("build failed")
}

func TestMissingSecretHoldsTheStartUntilItIsSet(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	spec.Secrets = []string{"TOKEN"}
	row := rig.declare(spec)
	row = rig.settle()
	if row.Reason != contract.ServiceSecretMissing || row.State != contract.ServicePending || count(rig.host.Calls, fakehost.RestartService) != 0 {
		t.Fatalf("service %+v", row.Service)
	}
	if err := rig.st.SetServiceSecret(rig.ctx, row.ID, "TOKEN", []byte("s3cret"), rig.now); err != nil {
		t.Fatal(err)
	}
	if row = rig.settle(); row.State != contract.ServiceRunning {
		t.Fatalf("service %+v", row.Service)
	}
	if string(rig.host.Service.Secrets[box.ServiceSecretName(row.ID, "TOKEN")]) != "s3cret" {
		t.Fatal("secret not on the host")
	}
	if strings.Contains(rig.host.Service.Files[row.ID], "s3cret") {
		t.Fatal("secret value in the quadlet")
	}
}

func TestCrashLoopEmitsExactlyOneFailure(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	for i := range 4 {
		rig.r.ObserveServices([]box.ServiceUnit{{ID: row.ID, State: box.UnitCrashing, CrashLoop: true, Restarts: 5 + i}})
		rig.settle()
	}
	row = rig.row()
	if row.State != contract.ServiceCrashing || row.Reason != contract.ServiceCrashLoop {
		t.Fatalf("service %+v", row.Service)
	}
	got := rig.failures()
	if len(got) != 1 || got[0].Reason != contract.ServiceCrashLoop {
		t.Fatalf("failures %+v", got)
	}
}

func TestRemoveDeletesTheHostFilesAndTheRow(t *testing.T) {
	rig := newServiceRig(t)
	row := rig.declare(web())
	rig.settle()
	if err := rig.r.RemoveService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	rig.settle()
	if rows, _ := rig.st.Services(rig.ctx, "r"); len(rows) != 0 {
		t.Fatalf("rows %+v", rows)
	}
	if len(rig.host.Service.Files) != 0 || count(rig.host.Calls, fakehost.RemoveService) == 0 {
		t.Fatal("host files left")
	}
}

func TestHostWithAServiceCannotBeRemoved(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	rig.settle()
	var held *store.HostHeldError
	if err := rig.st.RemoveHost(rig.ctx, "h"); !errors.As(err, &held) || !slices.Contains(held.Held, "service web") {
		t.Fatalf("remove: %v", err)
	}
}

func count(calls []fakehost.Call, c fakehost.Call) int {
	n := 0
	for _, x := range calls {
		if x == c {
			n++
		}
	}
	return n
}
