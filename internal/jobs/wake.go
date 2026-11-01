package jobs

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"time"

	"dyna-agent/internal/machines"
)

const wakeFreshness = 30 * time.Second

// wakeDemand starts one bounded coordinator for a node only when its queue
// contains work from a room the node still serves.
func (s *Service) wakeDemand(ctx context.Context, queued []Job) {
	a := s.admission()
	db, err := a.db()
	if err != nil {
		return
	}
	_, err = db.ExecContext(ctx, `UPDATE node_wake_leases SET ready_ms=0 WHERE ready_ms<>0 AND NOT EXISTS (SELECT 1 FROM machines m WHERE m.id=node_wake_leases.node_id AND (EXISTS (SELECT 1 FROM jobs j WHERE j.node=m.name AND j.state='queued' AND j.cancel_requested=0) OR EXISTS (SELECT 1 FROM admissions a WHERE a.node=m.name AND a.state='waiting')))`)
	if err != nil {
		return
	}
	nodes, err := a.waitingNodes(ctx)
	if err != nil {
		return
	}
	for _, job := range queued {
		nodes = append(nodes, job.Node)
	}
	slices.Sort(nodes)
	nodes = slices.Compact(nodes)
	for _, node := range nodes {
		m, err := a.machines().Get(ctx, node)
		if err != nil || !m.Enabled || len(m.WakeCommand) == 0 {
			continue
		}
		if s.wakeDemandSince(ctx, m).IsZero() {
			continue
		}
		s.mu.Lock()
		if s.stopped {
			s.mu.Unlock()
			return
		}
		key := "wake:" + m.ID
		if s.stops == nil {
			s.stops = map[string]func(){}
		}
		if s.stops[key] != nil {
			s.mu.Unlock()
			continue
		}
		work, cancel := context.WithCancel(context.Background())
		s.stops[key] = cancel
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer func() { s.mu.Lock(); delete(s.stops, key); s.mu.Unlock(); cancel() }()
			s.wakeNode(work, m)
		}()
	}
}

func (s *Service) wakeDemandSince(ctx context.Context, m machines.Machine) time.Time {
	q, err := s.admission().readQueue(ctx, m.Name)
	if err != nil {
		return time.Time{}
	}
	var oldest time.Time
	for _, job := range q.queued {
		if !job.CancelRequested && m.AllowsRoom(job.RoomID) && (oldest.IsZero() || job.Created.Before(oldest)) {
			oldest = job.Created
		}
	}
	for _, row := range q.waitingRows {
		if (row.Engine || ownerAlive(row.PID, row.Start)) && m.AllowsRoom(row.Room) && (oldest.IsZero() || row.Created.Before(oldest)) {
			oldest = row.Created
		}
	}
	return oldest
}

func (s *Service) wakeNode(ctx context.Context, original machines.Machine) {
	db, err := s.admission().db()
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	result, err := db.ExecContext(ctx, `INSERT INTO node_wake_leases(node_id,lease_until,next_wake,ready_ms) VALUES(?,?,0,0)
		ON CONFLICT(node_id) DO UPDATE SET lease_until=excluded.lease_until WHERE lease_until<=?`,
		original.ID, now+90000, now)
	if err != nil {
		return
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return
	}
	defer db.ExecContext(context.Background(), `UPDATE node_wake_leases SET lease_until=0 WHERE node_id=? AND lease_until=?`, original.ID, now+90000)

	m, err := s.admission().machines().Get(ctx, original.Name)
	if err != nil || m.ID != original.ID || !m.Enabled || !slices.Equal(m.WakeCommand, original.WakeCommand) || s.wakeDemandSince(ctx, m).IsZero() {
		return
	}
	var ready, next int64
	if err := db.QueryRowContext(ctx, `SELECT ready_ms,next_wake FROM node_wake_leases WHERE node_id=?`, m.ID).Scan(&ready, &next); err != nil {
		return
	}
	if oldest := s.wakeDemandSince(ctx, m); oldest.IsZero() || ready < oldest.UnixMilli() {
		ready = 0
	}
	if ready < m.Updated.UnixMilli() {
		ready = 0
	}
	if ready == 0 {
		if time.Now().UnixMilli() < next {
			return
		}
		runCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		var runErr error
		if s.wakeRun != nil {
			runErr = s.wakeRun(runCtx, m.WakeCommand)
		} else {
			cmd := exec.CommandContext(runCtx, m.WakeCommand[0], m.WakeCommand[1:]...)
			cmd.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=/usr/bin:/bin", "LANG=C"}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			cmd.WaitDelay = 2 * time.Second
			runErr = cmd.Run()
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}
		stop()
		if runErr != nil {
			_, _ = db.ExecContext(ctx, `UPDATE node_wake_leases SET next_wake=? WHERE node_id=? AND lease_until=?`, time.Now().Add(time.Minute).UnixMilli(), m.ID, now+90000)
			return
		}
		m, err = s.admission().machines().Get(ctx, original.Name)
		if err != nil || m.ID != original.ID || !m.Enabled || !slices.Equal(m.WakeCommand, original.WakeCommand) || s.wakeDemandSince(ctx, m).IsZero() {
			return
		}
		ready = time.Now().UnixMilli() + 1
		if _, err := db.ExecContext(ctx, `UPDATE node_wake_leases SET ready_ms=?,next_wake=0 WHERE node_id=? AND lease_until=?`, ready, m.ID, now+90000); err != nil {
			return
		}
	}
	if m.Health == machines.HealthOnline && m.LastProbe.UnixMilli() >= ready && time.Since(m.LastProbe) <= wakeFreshness {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	probe := s.wakeProbe
	if probe == nil {
		probe = (machines.Prober{Store: machines.Store{DataDir: s.dataDir()}}).ProbeAndSave
	}
	probed, err := probe(probeCtx, m)
	if err != nil || probed.Health != machines.HealthOnline {
		return
	}
	s.mu.Lock()
	wake := s.wake
	s.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
