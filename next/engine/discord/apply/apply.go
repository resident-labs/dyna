// Package apply makes a Discord server match its private folder.
//
// It writes only what the folder owns: objects it created, and objects the
// folder adopts by "id:" or "was:". Every successful effect is recorded before
// the next call, so a second run resumes and never repeats a create.
package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/folder"
	"github.com/resident-labs/dyna/engine/store"
)

// Owned is a recorded object: Kind and Path name it, ID is its Discord ID, and an
// empty ID means a create was started whose outcome is not recorded.
type Owned = store.DiscordOwned

// Ownership is the room's record of what apply owns on one server.
type Ownership interface {
	Owned(ctx context.Context) ([]Owned, error)
	Record(ctx context.Context, owned Owned) error
	Forget(ctx context.Context, kind, path string) error
}

type Target struct {
	Client        *discord.Client
	Guild         string
	Folder        string // the private folder on the engine host
	Owned         Ownership
	CurrentPolicy string
	SetPolicy     func(ctx context.Context, text string) error
}

type Report struct {
	Lines  []string
	Done   int
	Failed bool
}

func (report Report) String() string { return strings.Join(report.Lines, "\n") }

// errUnknownOutcome marks a write that may have reached Discord.
var errUnknownOutcome = errors.New("outcome unknown")

type step struct {
	text string
	do   func() error
}

type run struct {
	ctx     context.Context
	target  Target
	desired folder.Folder
	live    folder.Snapshot
	plan    folder.Plan
	owned   []Owned
	ids     map[folder.Subject]string // live IDs of folder objects, including those created in this run
	entries map[string]folder.Entry   // by folder path
	roles   map[string]folder.Role    // by name
	phases  [phaseCount][]step
	byHand  []string
	dryRun  bool                    // plan only: claims are checked, never recorded
	claimed map[folder.Subject]bool // objects whose id: a claim restores
}

// Run refuses before any write if the folder cannot be applied safely. Otherwise
// it executes the plan and reports each change; a failure stops the run at once.
func Run(ctx context.Context, target Target, prune bool) (Report, error) {
	r, err := load(ctx, target)
	if err != nil {
		return Report{}, err
	}
	if r.desired, err = r.claimRecorded(); err != nil {
		return Report{}, err
	}
	r.hydrateMessages()
	if r.plan, err = folder.BuildPlan(r.desired, r.live); err != nil {
		return Report{}, err
	}
	r.index()
	if err := r.refuseUnadopted(); err != nil {
		return Report{}, err
	}
	if err := r.recordAdopted(); err != nil {
		return Report{}, err
	}
	r.addChanges()
	if prune {
		if err := r.addPrune(); err != nil {
			return Report{}, err
		}
	}
	return r.execute(), nil
}

func (r *run) index() {
	r.ids = r.plan.Resolved
	if r.ids == nil {
		r.ids = map[folder.Subject]string{}
	}
	r.entries = map[string]folder.Entry{}
	for _, entry := range r.desired.Entries {
		r.entries[entry.Path] = entry
	}
	r.roles = map[string]folder.Role{}
	for _, role := range r.desired.Server.Roles {
		r.roles[role.Name] = role
	}
}

func (r *run) execute() Report {
	report := Report{Lines: []string{r.plan.String()}}
	var steps []step
	for _, phase := range r.phases {
		steps = append(steps, phase...)
	}
	for index, current := range steps {
		if err := current.do(); err != nil {
			report.Failed = true
			report.Lines = append(report.Lines, fmt.Sprintf("Failed: %s: %v", current.text, err))
			for _, remaining := range steps[index+1:] {
				report.Lines = append(report.Lines, "Not done: "+remaining.text)
			}
			break
		}
		report.Done++
		report.Lines = append(report.Lines, "Done: "+current.text)
	}
	for _, text := range r.byHand {
		report.Lines = append(report.Lines, "By hand: "+text)
	}
	outcome := "all done"
	if report.Failed {
		outcome = "stopped on a failure"
	}
	report.Lines = append(report.Lines, fmt.Sprintf("%d of %d steps done, %d left for you by hand; %s.", report.Done, len(steps), len(r.byHand), outcome))
	return report
}

// call makes one Discord REST call. It never retries; the client waits out rate limits.
func (r *run) call(method, route string, body any) (json.RawMessage, error) {
	var data json.RawMessage
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	result, err := r.target.Client.API(r.ctx, method, "/api/v10"+route, data, "discord apply")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnknownOutcome, err)
	}
	if result.Status >= 500 {
		return nil, fmt.Errorf("%w: HTTP %d", errUnknownOutcome, result.Status)
	}
	if result.Status >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", result.Status, result.Body)
	}
	return result.Body, nil
}

// create posts a new object. The create is recorded as started first, so an
// unclear answer or a crash is matched against the server by the next run
// instead of being repeated. A created role, category, channel or AutoMod rule
// gets its new ID written into the folder at once.
func (r *run) create(owned Owned, route string, body any) (string, error) {
	if kind := folder.Kind(owned.Kind); kind != folder.MessageKind {
		// The receipt keeps what existed, so the next run can tell whether anything new appeared.
		known := r.liveIDs(kind)
		for subject, id := range r.ids {
			// Categories and channels share one inventory on the server.
			if sameInventory(subject.Kind, kind) {
				known = append(known, id)
			}
		}
		data, _ := json.Marshal(known)
		owned.Content = string(data)
	}
	if err := r.target.Owned.Record(r.ctx, owned); err != nil {
		return "", err
	}
	response, err := r.call("POST", route, body)
	if err != nil {
		if !errors.Is(err, errUnknownOutcome) {
			return "", errors.Join(err, r.target.Owned.Forget(r.ctx, owned.Kind, owned.Path))
		}
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(response, &created) != nil || created.ID == "" {
		return "", fmt.Errorf("%w: no ID in the answer", errUnknownOutcome)
	}
	owned.ID = created.ID
	if folder.Kind(owned.Kind) != folder.MessageKind {
		owned.Content = "" // the receipt's list of known objects
	}
	if err := r.target.Owned.Record(r.ctx, owned); err != nil {
		return "", err
	}
	if kind := folder.Kind(owned.Kind); kind != folder.MessageKind {
		r.ids[folder.Subject{Kind: kind, Name: owned.Path}] = created.ID
		return created.ID, folder.RecordID(r.target.Folder, kind, owned.Path, created.ID)
	}
	return created.ID, nil
}

// load reads the record, the folder and the server.
func load(ctx context.Context, target Target) (*run, error) {
	r := &run{ctx: ctx, target: target}
	var err error
	if r.owned, err = target.Owned.Owned(ctx); err != nil {
		return nil, err
	}
	if r.desired, err = folder.Load(target.Folder); err != nil {
		return nil, err
	}
	if r.live, err = folder.ReadManagedSnapshot(ctx, target.Client, target.Guild, r.desired.Entries); err != nil {
		return nil, err
	}
	r.live.Policy = target.CurrentPolicy
	return r, nil
}

// PlanText is what apply would do, and with prune what prune would delete or
// refuse, from the same code apply uses. It writes nothing, to Discord or the record.
func PlanText(ctx context.Context, target Target, prune bool) (string, error) {
	r, err := load(ctx, target)
	if err != nil {
		return "", err
	}
	if prune {
		r.dryRun = true
		if _, err := r.claimRecorded(); err != nil {
			return "", err
		}
	}
	r.hydrateMessages()
	if r.plan, err = folder.BuildPlan(r.desired, r.live); err != nil {
		return "", err
	}
	lines := []string{r.plan.String()}
	if prune {
		r.index()
		if err := r.refuseUnadopted(); err != nil {
			return "", err
		}
		if err := r.addPrune(); err != nil {
			return "", err
		}
		for _, deletion := range r.phases[prunePhase] {
			lines = append(lines, strings.ToUpper(deletion.text[:1])+deletion.text[1:]+".")
		}
	}
	return strings.Join(lines, "\n"), nil
}

func sameInventory(a, b folder.Kind) bool {
	isChannel := func(kind folder.Kind) bool { return kind == folder.ChannelKind || kind == folder.CategoryKind }
	return a == b || (isChannel(a) && isChannel(b))
}
