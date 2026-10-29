package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/room"
	"dyna-agent/internal/roomsvc"
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
	exit := 0
	if job.ExitCode != nil {
		exit = *job.ExitCode
	} else if state != room.JobRunning && state != room.JobQueued {
		exit = -1
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
		ExitCode:   exit,
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

// runJobInput is the RunJob call's argument.
type runJobInput struct {
	Node    string   `json:"node"`
	Command []string `json:"command"`
	Repo    string   `json:"repo"`
	Ref     string   `json:"ref"`
	Host    bool     `json:"host"`
	Image   string   `json:"image"`
	CPUs    int      `json:"cpus"`
	Memory  string   `json:"memory"`
	Timeout int      `json:"timeoutSec"`
	Idle    *int     `json:"idleSec"`
	// Artifacts are paths of the job's work folder to keep after it ends.
	Artifacts []string `json:"artifacts"`
	// IO is heavy or light: an IO-heavy job shares a node with only a few
	// other IO-heavy ones.
	IO string `json:"io"`
	// Resources are named units the job holds for its whole run.
	Resources []string `json:"resources"`
}

func (s services) runJob(ctx context.Context, in runJobInput) (jobs.Job, error) {
	if s.jobs == nil {
		return jobs.Job{}, fmt.Errorf("this engine runs no jobs")
	}
	actor := authority.Of(ctx)
	resources, err := machines.ParseResourceNames(in.Resources)
	if err != nil {
		return jobs.Job{}, err
	}
	var idle *time.Duration
	if in.Idle != nil {
		duration := time.Duration(*in.Idle) * time.Second
		idle = &duration
	}
	input := jobs.Input{
		Node: in.Node, Command: in.Command,
		Repo: in.Repo, Ref: in.Ref, Host: in.Host, Image: in.Image,
		CPUs: in.CPUs, Memory: in.Memory, Timeout: time.Duration(in.Timeout) * time.Second, Idle: idle,
		Artifacts: in.Artifacts, Heavy: in.IO == "heavy", Resources: resources,
	}
	if in.IO != "" && in.IO != "heavy" && in.IO != "light" {
		return jobs.Job{}, fmt.Errorf("io must be heavy or light, not %q", in.IO)
	}
	if actor.Room != "" {
		input.RoomID = actor.Room
	}
	if actor.Session != "" {
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

func (s services) cancelJob(ctx context.Context, id string) (jobs.Job, error) {
	if s.jobs == nil {
		return jobs.Job{}, fmt.Errorf("this engine runs no jobs")
	}
	job, err := s.jobs.Store.Get(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	// The owner, the session that submitted the job and the root of the job's
	// own room may stop it; nobody else.
	actor := authority.Of(ctx)
	if actor.Role != authority.Owner && actor.Session != job.Session && !(actor.Role == authority.Root && actor.Room == job.RoomID) {
		return jobs.Job{}, fmt.Errorf("only the owner, the session that submitted job %s or the root of its room may cancel it", job.ID)
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
