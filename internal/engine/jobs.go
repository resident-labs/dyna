package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
)

// newJobsService builds the engine's job service: the machine registry for
// nodes and the room event contract for results.
func newJobsService(rooms *roomsvc.Service) *jobs.Service {
	reporter := jobsReporter{rooms: rooms}
	return &jobs.Service{
		Store:    jobs.Store{},
		Machines: machines.Store{},
		Reporter: reporter,
		RoomName: reporter.roomName,
	}
}

// jobsReporter posts a job's typed events to the room of the session that
// submitted the job.
type jobsReporter struct{ rooms *roomsvc.Service }

func (r jobsReporter) roomStore(job jobs.Job) (*room.Store, string) {
	if r.rooms == nil || r.rooms.Notify == nil || r.rooms.Notify.Rooms == nil || job.RoomID == "" {
		return nil, ""
	}
	rooms := r.rooms.Notify.Rooms
	rec, err := rooms.Resolve(job.RoomID)
	if err != nil {
		return nil, ""
	}
	return rooms, rec.Name
}

// roomName names a resource holder's room, and keeps its ID if it is gone.
func (r jobsReporter) roomName(id string) string {
	_, name := r.roomStore(jobs.Job{RoomID: id})
	if name == "" {
		return id
	}
	return name
}

// sessionName reads the display name of a session ID.
func (r jobsReporter) sessionName(id string) string {
	if id == "" || r.rooms == nil || r.rooms.Notify == nil || r.rooms.Notify.Sessions == nil {
		return ""
	}
	root := r.rooms.Notify.Sessions.Store.Root
	var session struct {
		Name string `json:"name"`
	}
	if err := sessionlife.Load(filepath.Join(filepath.Dir(root), "sessions"), id, &session); err != nil {
		return ""
	}
	return session.Name
}

func (r jobsReporter) event(job jobs.Job) room.JobEvent {
	state := room.JobState(job.State)
	outcome := ""
	if job.State.Finished() {
		outcome = job.State.Label()
		if job.ExitCode != nil {
			outcome = fmt.Sprintf("%s with exit code %d", outcome, *job.ExitCode)
		} else {
			switch state {
			case room.JobCancelled:
				outcome = "was cancelled"
			case room.JobLost:
				outcome = "was lost"
			}
			if job.Note != "" && !(state == room.JobCancelled && job.Note == "cancelled") {
				outcome += ": " + job.Note
			}
		}
	}
	duration := time.Duration(0)
	if !job.Started.IsZero() && !job.Ended.IsZero() {
		duration = job.Ended.Sub(job.Started)
	}
	detail := "log: dyna job logs " + job.ID
	if state == room.JobQueued {
		detail = ""
	}
	return room.JobEvent{
		JobID: job.ID, Node: job.Node,
		Label:      strings.Join(job.Command, " "),
		State:      state,
		ExitCode:   job.ExitCode,
		Outcome:    outcome,
		DurationMS: int(duration / time.Millisecond),
		Parent:     r.sessionName(job.Session),
		WaitReason: job.WaitReason,
		Detail:     detail,
	}
}

func (r jobsReporter) post(job jobs.Job) error {
	rooms, name := r.roomStore(job)
	if rooms == nil {
		return fmt.Errorf("job %s has no room to report to", job.ID)
	}
	to := name
	if session := r.sessionName(job.Session); session != "" {
		to = name + ":" + session
	}
	_, err := rooms.PostEventInput(room.PostEventInput{
		RoomID: job.RoomID, Producer: room.ProducerJobs, To: []string{to},
		SessionIDs: map[string]string{to: job.Session},
		Event:      r.event(job),
	})
	return err
}

func (r jobsReporter) JobQueued(ctx context.Context, job jobs.Job) error   { return r.post(job) }
func (r jobsReporter) JobStarted(ctx context.Context, job jobs.Job) error  { return r.post(job) }
func (r jobsReporter) JobFinished(ctx context.Context, job jobs.Job) error { return r.post(job) }

// runJobInput keeps the engine's tests and handler concise while the RPC
// boundary shares one typed request with workflow coordinators and the CLI.
type runJobInput = callspec.RunJobInput

func (s services) runJob(ctx context.Context, in runJobInput) (jobs.Job, error) {
	if s.jobs == nil {
		return jobs.Job{}, fmt.Errorf("this engine runs no jobs")
	}
	actor := authority.Of(ctx)
	resources, err := machines.ParseResourceNames(in.Resources)
	if err != nil {
		return jobs.Job{}, err
	}
	labels, err := machines.ParseLabels(in.Labels)
	if err != nil {
		return jobs.Job{}, err
	}
	var idle *time.Duration
	if in.Idle != nil {
		duration := time.Duration(*in.Idle) * time.Second
		idle = &duration
	}
	input := jobs.Input{
		Node: in.Node, Labels: labels, Command: in.Command,
		Repo: in.Repo, Ref: in.Ref, Host: in.Host, Image: in.Image,
		CPUs: in.CPUs, Memory: in.Memory, Timeout: time.Duration(in.Timeout) * time.Second, Idle: idle,
		Artifacts: in.Artifacts, Heavy: in.IO == "heavy", Resources: resources, Workspace: in.Workspace,
	}
	if in.IO != "" && in.IO != "heavy" && in.IO != "light" {
		return jobs.Job{}, fmt.Errorf("io must be heavy or light, not %q", in.IO)
	}
	if actor.Room != "" {
		input.RoomID = actor.Room
	}
	if actor.Role == authority.Worker && actor.Run != "" {
		input.Run = actor.Run
		meta, err := runstore.ReadMeta(actor.Run)
		if err != nil {
			return jobs.Job{}, fmt.Errorf("read workflow run %s: %w", actor.Run, err)
		}
		input.Session = meta.ParentSession
		if input.Session == "" {
			input.Session = meta.Session
		}
	} else {
		input.Session = actor.Session
	}
	return s.jobs.Submit(ctx, input)
}

func (s services) jobShow(ctx context.Context, id string) (jobs.Job, error) {
	if s.jobs == nil {
		return jobs.Job{}, fmt.Errorf("this engine runs no jobs")
	}
	return s.jobs.Store.Get(ctx, id)
}

func (s services) jobList(ctx context.Context, open bool) ([]jobs.Job, error) {
	if s.jobs == nil {
		return nil, fmt.Errorf("this engine runs no jobs")
	}
	return s.jobs.Store.List(ctx, open)
}

// waitJob waits for committed jobs events, never for a timer or a polling
// interval. The initial read after subscribing closes the missed-event race.
func (s services) waitJob(ctx context.Context, id string) (jobs.WaitResult, error) {
	if s.jobs == nil {
		return jobs.WaitResult{}, fmt.Errorf("this engine runs no jobs")
	}
	if s.events == nil {
		return jobs.WaitResult{}, fmt.Errorf("this engine has no job events")
	}
	waitCtx, cancel := context.WithTimeout(ctx, callspec.JobWaitBound)
	defer cancel()
	changes, _, unsubscribe := s.events.Subscribe()
	defer unsubscribe()
	for {
		job, err := s.jobs.Store.Get(waitCtx, id)
		if err != nil {
			return jobs.WaitResult{}, err
		}
		if job.State.Finished() {
			return jobs.WaitResult{Job: job, Done: true}, nil
		}
	wait:
		for {
			select {
			case <-waitCtx.Done():
				if ctx.Err() != nil {
					return jobs.WaitResult{}, ctx.Err()
				}
				return jobs.WaitResult{}, nil
			case event, ok := <-changes:
				if !ok {
					return jobs.WaitResult{}, fmt.Errorf("the job event stream ended")
				}
				if event.Topic == jobs.Topic {
					break wait
				}
			}
		}
	}
}

func (s services) cancelJob(ctx context.Context, id string) (jobs.Job, error) {
	if s.jobs == nil {
		return jobs.Job{}, fmt.Errorf("this engine runs no jobs")
	}
	job, err := s.jobs.Store.Get(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	// The owner, the session or workflow run that submitted the job, and the
	// root of the job's own room may stop it; nobody else.
	actor := authority.Of(ctx)
	submitter := actor.Session != "" && actor.Session == job.Session ||
		actor.Role == authority.Worker && actor.Run != "" && actor.Run == job.Run
	if actor.Role != authority.Owner && !submitter && !(actor.Role == authority.Root && actor.Room == job.RoomID) {
		return jobs.Job{}, fmt.Errorf("only the owner, the session or run that submitted job %s, or the root of its room may cancel it", job.ID)
	}
	return s.jobs.Cancel(ctx, id)
}

// jobLogsView is what JobLogs returns.
type jobLogsView struct {
	ID  string `json:"id"`
	Log string `json:"log"`
}

func (s services) jobLogs(ctx context.Context, id string) (jobLogsView, error) {
	job, err := s.jobShow(ctx, id)
	if err != nil {
		return jobLogsView{}, err
	}
	data, err := os.ReadFile(job.LogPath)
	if err != nil {
		return jobLogsView{}, err
	}
	const tail = 64 << 10
	if len(data) > tail {
		data = data[len(data)-tail:]
	}
	return jobLogsView{ID: job.ID, Log: string(data)}, nil
}
