package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestServiceGenerationImageReservationSurvivesReopen(t *testing.T) {
	const target = "ghcr.io/o/r@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name     string
		action   string
		measured int64
		target   string
		want     int64
	}{
		{"update larger", "update", 1 << 30, "", 1 << 30},
		{"secret larger", "secret", 1 << 30, "", 1 << 30},
		{"update exact smaller measurement", "update", 64 << 20, "", 64 << 20},
		{"secret exact smaller measurement", "secret", 64 << 20, "", 64 << 20},
		{"update unmeasured source", "update", -1, "", contract.ServiceImageEstimate},
		{"secret unmeasured source", "secret", -1, "", contract.ServiceImageEstimate},
		{"update replaces rollback target", "update", 1 << 30, target, 1 << 30},
		{"rollback skips source estimate", "rollback", 1 << 30, "", 0},
		{"secret keeps rollback target", "secret", 1 << 30, target, 0},
		{"stop does not start generation", "stop", 1 << 30, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, dir := context.Background(), t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			if _, err := s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0); INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('h','','ready',64,137438953472,1099511627776,0)`); err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100000, 0)
			events := 0
			newEvent := func(room contract.RoomID, body contract.EventBody) (contract.Event, error) {
				events++
				return contract.Event{ID: contract.EventID(fmt.Sprintf("ev_%d", events)), Room: room, Time: now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: body}, nil
			}
			parts, err := (contract.ServiceSpec{Name: "web", Image: "app", Disk: "0GiB", Secrets: []string{"TOKEN"}}).Parse()
			if err != nil {
				t.Fatal(err)
			}
			row, err := s.SetService(ctx, "r", parts, "svc_1", contract.Principal{Kind: contract.PrincipalOwner}, now, newEvent)
			if err != nil {
				t.Fatal(err)
			}
			// A previous live declaration still holds more than the desired one. Refreshing only the image hold must
			// neither free these resources nor replace the measured live-image hold with a smaller estimate.
			liveSpec := parts.Spec
			liveSpec.CPUs, liveSpec.Memory, liveSpec.Disk = 2, "2GiB", "2GiB"
			live, err := liveSpec.Parse()
			if err != nil {
				t.Fatal(err)
			}
			next := row
			next.Host, next.State, next.Deployed = "h", contract.ServiceRunning, row.Generation
			next.Live, next.ImageBytes, next.ImageEstimate, next.Target = &live, 100<<20, 0, tc.target
			if saved, err := s.SaveService(ctx, row, next, newEvent); err != nil || !saved {
				t.Fatalf("seed completed deployment: %v %v", saved, err)
			}
			if tc.measured >= 0 {
				if err := s.RecordServiceImage(ctx, "r", parts, tc.measured, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			unlock := s.LockAdmission()
			if tc.action == "secret" {
				err = s.SetServiceSecret(ctx, row.ID, "TOKEN", []byte("v2"), now)
			} else {
				var changed ServiceRow
				changed, err = s.ChangeService(ctx, row.ID, newEvent, func(row *ServiceRow) error {
					switch tc.action {
					case "update":
						row.Generation++
						row.Target = ""
					case "rollback":
						row.Generation++
						row.Target = target
					case "stop":
						row.Want = contract.ServiceWantStop
					}
					return nil
				})
				if err == nil && changed.ImageEstimate != tc.want {
					t.Errorf("returned estimate = %d, want %d", changed.ImageEstimate, tc.want)
				}
			}
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() })
			got, err := reopened.Service(ctx, row.ID)
			wantGeneration := row.Generation + 1
			if tc.action == "stop" {
				wantGeneration = row.Generation
			}
			wantTarget := tc.target
			if tc.action == "rollback" {
				wantTarget = target
			} else if tc.action == "update" {
				wantTarget = ""
			}
			if err != nil || got.ImageEstimate != tc.want || got.Generation != wantGeneration || got.Target != wantTarget || got.Live == nil {
				t.Errorf("reopened generation reservation: %+v, %v", got, err)
			}
			holds, err := reopened.ServiceHolds(ctx)
			if err != nil || len(holds) != 1 || holds[0].DiskBytes != 2<<30+max(100<<20, tc.want) || holds[0].CPUMilli != 2000 || holds[0].MemBytes != 2<<30 {
				t.Errorf("reopened live and image holds: %+v, %v", holds, err)
			}
		})
	}
}
