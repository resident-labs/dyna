package lifecycle

import (
	"context"
	"database/sql"
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
	rig.sql("UPDATE hosts SET engine_host=0")
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
	rows, err := rig.st.Services(rig.ctx, "r")
	if err != nil || len(rows) == 0 {
		return store.ServiceRow{}
	}
	return rows[0]
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

func (rig *serviceRig) sql(q string, args ...any) {
	rig.t.Helper()
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		rig.t.Fatal(err)
	}
}

func (rig *serviceRig) rows() map[contract.ServiceName]store.ServiceRow {
	rig.t.Helper()
	rows, err := rig.st.Services(rig.ctx, "r")
	if err != nil {
		rig.t.Fatal(err)
	}
	out := map[contract.ServiceName]store.ServiceRow{}
	for _, row := range rows {
		out[row.Spec.Name] = row
	}
	return out
}

// After an engine start the engine-host flag is NULL until the host check refills it. Nothing is placed or started meanwhile.
func TestServiceWaitsWhileTheEngineHostFlagIsUnknown(t *testing.T) {
	rig := newServiceRig(t)
	rig.sql("UPDATE hosts SET engine_host=NULL")
	rig.declare(web())
	rig.settle()
	if row := rig.row(); row.Host != "" || row.State != contract.ServicePending || count(rig.host.Calls, fakehost.RestartService) != 0 {
		t.Fatalf("service %+v", row.Service)
	}
	// And one placed before the flag was cleared starts nothing either.
	rig.sql("UPDATE hosts SET engine_host=0")
	rig.settle()
	rig.sql("UPDATE hosts SET engine_host=NULL")
	update := web()
	update.Image = digestImage2
	rig.declare(update)
	rig.host.Calls = nil
	rig.settle()
	if count(rig.host.Calls, fakehost.RestartService) != 0 || count(rig.host.Calls, fakehost.WriteServiceUnit) != 0 {
		t.Fatal("a deploy ran on a host that may be the engine's")
	}
}

// Two services placed in one pass are judged with each other: the quarter cap and the port rule hold across the pass.
func TestServicesPlacedInOnePassCountAgainstEachOther(t *testing.T) {
	rig := newServiceRig(t)
	rig.sql("UPDATE hosts SET cpus=47")
	eight := func(name contract.ServiceName, port contract.ServicePort) contract.ServiceSpec {
		return contract.ServiceSpec{Name: name, Image: digestImage, CPUs: 8, Ports: []contract.ServicePort{port}}
	}
	rig.declare(eight("a", "8001"))
	rig.declare(eight("b", "8002"))
	rig.settle()
	rows := rig.rows()
	placed := 0
	for _, row := range rows {
		if row.Host != "" {
			placed++
		}
	}
	if placed != 1 {
		t.Fatalf("%d of two 8-CPU services placed on a 47-CPU host (cap 11.75), want 1: %+v", placed, rows)
	}
	for _, row := range rows {
		if row.Host == "" && row.Reason != contract.ServiceNoCPU {
			t.Fatalf("waiting service %+v", row.Service)
		}
	}
}

func TestServicesWithOnePortArePlacedOnePerHostInOnePass(t *testing.T) {
	rig := newServiceRig(t)
	one := func(name contract.ServiceName) contract.ServiceSpec {
		return contract.ServiceSpec{Name: name, Image: digestImage, Ports: []contract.ServicePort{"8001"}}
	}
	rig.declare(one("a"))
	rig.declare(one("b"))
	rig.settle()
	placed := 0
	for _, row := range rig.rows() {
		if row.Host != "" {
			placed++
		} else if row.Reason != contract.ServicePortTaken {
			t.Fatalf("waiting service %+v", row.Service)
		}
	}
	if placed != 1 {
		t.Fatalf("%d placed, want 1", placed)
	}
}

// A failed update leaves the old image running with the old declaration, so the unit file and its hash are the old ones and an
// engine restart finds the matching hash and restarts nothing.
func TestRolledBackUpdateKeepsTheLiveUnitAcrossAnEngineRestart(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	spec.Health = []string{"/bin/true"}
	rig.declare(spec)
	live := rig.settle()
	rig.host.Service.Health = func(_ contract.ServiceID, file string) error {
		if strings.Contains(file, "bbbbbbbb") {
			return errors.New("unhealthy")
		}
		return nil
	}
	spec.Image = digestImage2
	spec.Env = map[string]string{"NEW": "1"}
	rig.declare(spec)
	row := rig.settle()
	if row.Reason != contract.ServiceRolledBack || row.UnitSHA != live.UnitSHA {
		t.Fatalf("service %+v unit %s, live %s", row.Service, row.UnitSHA, live.UnitSHA)
	}
	if strings.Contains(rig.host.Service.Files[row.ID], "NEW") {
		t.Fatal("the rolled back unit holds the new declaration")
	}
	rig.r.SetServices(serviceHosts{rig.host}, rig.r.svc.timings)
	restarts, writes := count(rig.host.Calls, fakehost.RestartService), count(rig.host.Calls, fakehost.WriteServiceUnit)
	rig.settle()
	rig.settle()
	if count(rig.host.Calls, fakehost.RestartService) != restarts || count(rig.host.Calls, fakehost.WriteServiceUnit) != writes {
		t.Fatal("an engine restart rewrote or restarted the rolled back service")
	}
}

// A host that answers again after a removal was owed is cleaned on the next pass, not at the next engine start.
func TestLeftoversAreRepaidWhenTheHostReturns(t *testing.T) {
	rig := newServiceRig(t)
	row := rig.declare(web())
	rig.settle()
	rig.setHost(box.HostUnreachable)
	if err := rig.r.RemoveService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	rig.settle()
	if len(rig.host.Service.Files) == 0 {
		t.Fatal("the host's files went while it was away")
	}
	rig.setHost(box.HostReady)
	rig.settle()
	if len(rig.host.Service.Files) != 0 {
		t.Fatal("the owed removal was not repaid after the host returned")
	}
}

// A service on a host that became the engine's can still be removed.
func TestServiceOnAHostThatBecameTheEngineHostIsStillRemoved(t *testing.T) {
	rig := newServiceRig(t)
	row := rig.declare(web())
	rig.settle()
	rig.sql("UPDATE hosts SET engine_host=1")
	if err := rig.r.RemoveService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	rig.settle()
	if rows := rig.rows(); len(rows) != 0 || len(rig.host.Service.Files) != 0 {
		t.Fatalf("rows %+v files %d", rows, len(rig.host.Service.Files))
	}
}

// A missing kept image is a typed failure of the step, not a plain error that leaves the service deploying.
func TestMissingImageOnDeployIsTyped(t *testing.T) {
	rig := newServiceRig(t)
	rig.host.Service.Fail[fakehost.ImageSize] = errors.New("no such image")
	rig.declare(web())
	if row := rig.settle(); row.State != contract.ServiceFailed || row.Reason != contract.ServiceImagePull {
		t.Fatalf("service %+v", row.Service)
	}
}

// Lost podman secrets are written again with the unit.
func TestRewriteRestoresLostSecrets(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	spec.Secrets = []string{"TOKEN"}
	row := rig.declare(spec)
	row = rig.settle()
	if err := rig.st.SetServiceSecret(rig.ctx, row.ID, "TOKEN", []byte("v1"), rig.now); err != nil {
		t.Fatal(err)
	}
	rig.settle()
	name := box.ServiceSecretName(row.ID, "TOKEN")
	delete(rig.host.Service.Secrets, name)
	delete(rig.host.Service.Files, row.ID)
	rig.r.SetServices(serviceHosts{rig.host}, rig.r.svc.timings)
	rig.settle()
	if string(rig.host.Service.Secrets[name]) != "v1" {
		t.Fatal("the secret was not written again with the unit")
	}
}

// A host removed with --discard-boxes that still answers has its services taken off it, and their rows fail with it.
func TestDiscardingAHostRemovesItsServicesFromIt(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	rig.settle()
	rig.sql("DELETE FROM boxes")
	if _, err := rig.r.RemoveHost(rig.ctx, "h", true); err != nil {
		t.Fatal(err)
	}
	if len(rig.host.Service.Files) != 0 || count(rig.host.Calls, fakehost.RemoveService) == 0 {
		t.Fatalf("the host kept %d service files", len(rig.host.Service.Files))
	}
}

// The owner-facing restart bypasses the normal step, but must not bypass its engine-host exclusion. The host flag is read at
// restart time, not from the earlier placement, and unknown after ResetEngineHosts is just as unsafe as known engine host.
func TestDirectServiceRestartRefusesTheEngineHostAndUnknownHosts(t *testing.T) {
	for _, flag := range []string{"NULL", "1", "0"} {
		t.Run(flag, func(t *testing.T) {
			rig := newServiceRig(t)
			rig.declare(web())
			row := rig.settle()
			rig.sql("UPDATE hosts SET engine_host=" + flag)
			before := count(rig.host.Calls, fakehost.RestartService)
			_, err := rig.r.RestartServiceUnit(rig.ctx, row)
			if flag == "0" {
				if err != nil || count(rig.host.Calls, fakehost.RestartService) != before+1 {
					t.Fatalf("safe host restart: %v, calls %v", err, rig.host.Calls)
				}
				return
			}
			if err == nil || count(rig.host.Calls, fakehost.RestartService) != before {
				t.Fatalf("unsafe host restart: %v, calls %v", err, rig.host.Calls)
			}
			if after := rig.row(); after.Rev != row.Rev {
				t.Fatalf("refused restart changed row: before %+v, after %+v", row, after)
			}
		})
	}
}

func TestServicePlacementCountsRoomBudgetsAtOneTimes(t *testing.T) {
	for _, tc := range []struct {
		name, budget string
		cpus         float64
		memory       contract.ServiceSize
	}{
		{"cpu", "cpus=1", 8, "512MiB"},
		{"memory", "mem_bytes=1073741824", 1, "2GiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newServiceRig(t)
			rig.sql("UPDATE rooms SET " + tc.budget)
			spec := web()
			spec.CPUs, spec.Memory = tc.cpus, tc.memory
			rig.declare(spec)
			row := rig.settle()
			if row.Host != "" || string(row.Reason) != "room-limit" {
				t.Fatalf("room budget ignored: %+v", row.Service)
			}
		})
	}
}

func TestServicesPlacedInOnePassShareTheirRoomBudget(t *testing.T) {
	rig := newServiceRig(t)
	rig.sql("UPDATE rooms SET cpus=3,mem_bytes=4294967296")
	for _, name := range []contract.ServiceName{"a", "b"} {
		spec := web()
		spec.Name, spec.CPUs = name, 2
		spec.Ports = nil
		rig.declare(spec)
	}
	rig.settle()
	placed := 0
	for _, row := range rig.rows() {
		if row.Host != "" {
			placed++
		} else if string(row.Reason) != "room-limit" {
			t.Fatalf("wrong wait: %+v", row.Service)
		}
	}
	if placed != 1 {
		t.Fatalf("%d services placed within a room's 3-CPU budget, want 1", placed)
	}
}

func TestFailedShrinkKeepsTheLiveResourcesAndPortsReserved(t *testing.T) {
	rig := newServiceRig(t)
	rig.sql("UPDATE hosts SET cpus=47,mem_bytes=51539607552") // service cap 11.75 CPU and 12 GiB
	spec := web()
	spec.CPUs, spec.Memory, spec.Disk = 8, "8GiB", "8GiB"
	spec.Ports = []contract.ServicePort{"8080"}
	spec.Health = []string{"/bin/true"}
	rig.declare(spec)
	live := rig.settle()
	rig.host.Service.Health = func(_ contract.ServiceID, file string) error {
		if strings.Contains(file, "bbbbbbbb") {
			return errors.New("unhealthy")
		}
		return nil
	}
	spec.Image, spec.CPUs, spec.Memory, spec.Disk = digestImage2, .25, "512MiB", "1GiB"
	spec.Ports = []contract.ServicePort{"8081"}
	pending := rig.declare(spec)
	if pending.CPUMilli != 8000 || pending.MemBytes != 8<<30 || pending.DiskBytes != 8<<30 {
		t.Errorf("in-flight shrink dropped live reservation: %+v", pending.Service)
	}
	row := rig.settle()
	if row.Reason != contract.ServiceRolledBack || row.CPUMilli != 8000 || row.MemBytes != 8<<30 || row.DiskBytes != 8<<30 || row.UnitSHA != live.UnitSHA {
		t.Errorf("rollback dropped live reservation: %+v", row.Service)
	}
	if row.Parts.CPUMilli != 250 || row.Parts.Ports[0].Host != 8081 {
		t.Errorf("rollback lost desired declaration: %+v", row.Service)
	}
	for _, port := range []contract.ServicePort{"8080", "8081"} {
		other := web()
		other.Name, other.Ports = contract.ServiceName("port"+string(port)), []contract.ServicePort{port}
		parts, err := other.Parse()
		if err != nil {
			t.Fatal(err)
		}
		// With the host named, a conflict is refused at set even before admission.
		parts.Spec.Host = "h"
		_, err = rig.st.SetService(rig.ctx, "r", parts, contract.ServiceID("svc_"+string(port)), contract.Principal{Kind: contract.PrincipalOwner}, rig.now, rig.r.serviceEvent)
		var taken *store.PortTakenError
		if !errors.As(err, &taken) {
			t.Errorf("live/desired port %s not held: %v", port, err)
		}
	}
	for _, tc := range []struct {
		name contract.ServiceName
		cpus float64
		mem  contract.ServiceSize
	}{
		{"cpu", 8, "512MiB"}, {"memory", .25, "8GiB"},
	} {
		other := web()
		other.Name, other.CPUs, other.Memory, other.Ports = tc.name, tc.cpus, tc.mem, nil
		rig.declare(other)
	}
	rig.settle()
	for _, name := range []contract.ServiceName{"cpu", "memory"} {
		if row := rig.rows()[name]; row.Host != "" {
			t.Errorf("live reservation ignored for %s: %+v", name, row.Service)
		}
	}
}

func TestRestartingAStoppedServiceChangesItsIntentToRun(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	if _, err := rig.r.StopService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	row = rig.settle()
	if row.State != contract.ServiceStopped {
		t.Fatalf("not stopped: %+v", row.Service)
	}
	if _, err := rig.r.RestartServiceUnit(rig.ctx, row); err != nil {
		t.Fatal(err)
	}
	row = rig.settle()
	if row.Want != contract.ServiceWantRun || row.State != contract.ServiceRunning || rig.host.Service.Units[row.ID].State != box.UnitRunning {
		t.Fatalf("restart left intent/state inconsistent with running unit: %+v unit=%+v", row.Service, rig.host.Service.Units[row.ID])
	}
}

func TestServicePlacementHoldsItsImageEstimateInThePassAndTheStore(t *testing.T) {
	rig := newServiceRig(t)
	rig.sql("UPDATE hosts SET disk_floor=0,disk_bytes=1610612736") // 1.5 GiB: only one unknown 1 GiB image fits
	rig.sql("UPDATE boxes SET state='deleted'")
	for i, image := range []string{digestImage, digestImage2} {
		rig.declare(contract.ServiceSpec{Name: contract.ServiceName(fmt.Sprintf("svc%d", i)), Image: image, Disk: "0GiB"})
	}
	hosts, err := rig.st.Hosts(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := rig.st.Boxes(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.r.servicePass(rig.ctx, hosts, boxes); err != nil {
		t.Fatal(err)
	}
	rig.r.Wait()
	placed := 0
	for _, row := range rig.rows() {
		if row.Host != "" {
			placed++
		}
	}
	if placed != 1 {
		t.Errorf("placed %d unknown 1GiB images with only 1.5GiB available", placed)
	}
	holds, err := rig.st.ServiceHolds(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(holds) != 1 || holds[0].DiskBytes != contract.ServiceImageEstimate {
		t.Errorf("image estimate not persisted as a disk hold: %+v", holds)
	}
}

func TestServiceDeployRechecksCurrentDiskBeforePullingOrBuilding(t *testing.T) {
	for _, image := range []string{digestImage, "app"} {
		t.Run(image, func(t *testing.T) {
			rig := newServiceRig(t)
			rig.sql("UPDATE boxes SET state='deleted'")
			rig.sql("UPDATE hosts SET disk_floor=0,disk_bytes=2147483648")
			row := rig.declare(contract.ServiceSpec{Name: "web", Image: image, Disk: "0GiB"})
			hosts, err := rig.st.Hosts(rig.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := rig.r.servicePass(rig.ctx, hosts, nil); err != nil {
				t.Fatal(err)
			}
			rig.r.Wait()                                     // placed only; no deploy launched in this pass
			rig.sql("UPDATE hosts SET disk_bytes=536870912") // shrinks below the reserved estimate before the image phase
			row = rig.settle()
			if row.State != contract.ServicePending || row.Reason != contract.ServiceNoDisk || row.Deployed != 0 || count(rig.host.Calls, fakehost.PullService) != 0 || count(rig.host.Calls, fakehost.ImageSize) != 0 {
				t.Fatalf("full-disk deploy was not held before the image step: %+v calls=%v", row.Service, rig.host.Calls)
			}
			rig.sql("UPDATE hosts SET disk_bytes=2147483648")
			if row = rig.settle(); row.State != contract.ServiceRunning {
				t.Fatalf("no retry after disk returned: %+v", row.Service)
			}
		})
	}
}

func TestSuccessfulServiceShrinkReleasesOldResourcesAndPorts(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	spec.CPUs, spec.Memory, spec.Disk = 8, "8GiB", "8GiB"
	spec.Ports = []contract.ServicePort{"8080"}
	rig.declare(spec)
	rig.settle()
	spec.CPUs, spec.Memory, spec.Disk, spec.Ports, spec.Image = .25, "512MiB", "0GiB", []contract.ServicePort{"8081"}, digestImage2
	rig.declare(spec)
	row := rig.settle()
	if row.CPUMilli != 250 || row.MemBytes != 512<<20 || row.DiskBytes != 0 || row.Reason != "" || row.ImageRef != digestImage2 {
		t.Fatalf("successful shrink did not replace the hold: %+v", row.Service)
	}
	if row.Live == nil || row.Live.Ports[0].Host != 8081 {
		t.Fatalf("live declaration not replaced: %+v", row.Live)
	}
	other := contract.ServiceSpec{Name: "oldport", Image: digestImage, CPUs: 8, Ports: []contract.ServicePort{"8080"}, Host: "h"}
	rig.declare(other)
	rig.settle()
	if row := rig.rows()["oldport"]; row.State != contract.ServiceRunning {
		t.Fatalf("old resources/port still held: %+v", row.Service)
	}
}

// A pass can read want=run just before stop takes the exclusive owner-action gate, then enter its host step afterward. That
// queued step must use the row after the gate, not restart from its old stopped/want=run snapshot and lose the final CAS.
func TestQueuedServiceStepReadsTheLatestIntentAfterEnteringHostWork(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	if _, err := rig.r.StopService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	row = rig.settle()
	stale, err := rig.r.StartService(rig.ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rig.r.StopService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	hosts, err := rig.st.Hosts(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	restarts := count(rig.host.Calls, fakehost.RestartService)
	if err := rig.r.serviceStep(rig.ctx, stale, hosts[0]); err != nil {
		t.Fatal(err)
	}
	current := rig.row()
	if count(rig.host.Calls, fakehost.RestartService) != restarts || current.Want != contract.ServiceWantStop || rig.host.Service.Units[row.ID].State != box.UnitStopped {
		t.Fatalf("queued step used stale intent: row=%+v unit=%+v", current.Service, rig.host.Service.Units[row.ID])
	}
}

func TestStaleDirectRestartRefusesRemovalBeforeStartingTheUnit(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	if err := rig.r.RemoveService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	restarts := count(rig.host.Calls, fakehost.RestartService)
	if _, err := rig.r.RestartServiceUnit(rig.ctx, row); !errors.Is(err, ErrServiceRemoving) {
		t.Fatalf("stale restart: %v", err)
	}
	if count(rig.host.Calls, fakehost.RestartService) != restarts {
		t.Fatal("a rejected restart still started a removing unit")
	}
}

func TestStoppedServiceIsStoppedAgainIfItsUnitComesBackRunning(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	if _, err := rig.r.StopService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	row = rig.settle()
	// WantedBy=default.target can start the unit after a host reboot. The heartbeat, rather than the old row state, is current.
	if err := rig.host.RestartService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	rig.r.ObserveServices([]box.ServiceUnit{rig.host.Service.Units[row.ID]})
	row = rig.settle()
	if row.Want != contract.ServiceWantStop || row.State != contract.ServiceStopped || rig.host.Service.Units[row.ID].State != box.UnitStopped {
		t.Fatalf("stopped row hid a running unit: %+v unit=%+v", row.Service, rig.host.Service.Units[row.ID])
	}
}
