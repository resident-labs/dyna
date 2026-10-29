package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// eventsFile is the TypeScript the web app imports. It is generated from the
// Go registry, so a payload field cannot drift from its card. Regenerate it
// with DYNA_UPDATE_EVENTS=1 go test ./internal/room/ -run Events.
const eventsFile = "../../web/src/lib/rooms/events.gen.ts"

func TestEventsFileMatchesRegistry(t *testing.T) {
	want, err := generateEvents()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DYNA_UPDATE_EVENTS") != "" {
		if err := os.WriteFile(eventsFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(eventsFile)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date with the event registry; run DYNA_UPDATE_EVENTS=1 go test ./internal/room/ -run Events (read error: %v)", eventsFile, err)
	}
}

// renderersFile is the web's hand-written card table. It names one component
// per renderer of the generated registry.
const renderersFile = "../../web/src/lib/rooms/eventRenderers.ts"

// familyFacts is what the room promises about one event family: the cards that
// draw it, the services that may post it, who it wakes and where its posts
// belong. The registry states these facts per event type, so a new type can
// quietly widen its family; this table is the second copy that fails instead.
//
// A new family needs an entry here, which forces the author to say who posts
// it, who it wakes, where it belongs and which card draws it.
type familyFacts struct {
	Family    EventFamily
	Renderers []string
	Producers []EventProducer
	Wakes     []WakeRule
	Audiences []Audience
}

// eventFamilyFacts is the table of every family in eventSpecs.
var eventFamilyFacts = []familyFacts{
	{Family: FamilyWorktree, Renderers: []string{"WorktreeCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceSession}},
	{Family: FamilyHook, Renderers: []string{"HookCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyIssue, Renderers: []string{"IssueCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyIssueWatch, Renderers: []string{"IssueWatchCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeRoot}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyGoal, Renderers: []string{"GoalPost"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever, WakeTarget}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyCheckin, Renderers: []string{"CheckinCard"}, Producers: []EventProducer{ProducerParentCheckin}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceSession}},
	{Family: FamilyCIHealth, Renderers: []string{"SlowTestCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyRule, Renderers: []string{"RuleCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyMerge, Renderers: []string{"MergeCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever, WakeTarget, WakeRootIfNeeded}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyMachine, Renderers: []string{"MachineDiskCard", "MachineCacheCard", "MachineCacheCleanCard", "MachineScratchCard", "MachineLimitsCard", "RoomCapCard", "WorktreePortsCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeRootIfNeeded, WakeNever, WakeRoot}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyVoice, Renderers: []string{"VoiceSessionCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyCall, Renderers: []string{"CallCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyReview, Renderers: []string{"ReviewResultCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyPR, Renderers: []string{"PRCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilySession, Renderers: []string{"SessionNoteCard", "InstructionBatchCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever, WakeRootIfNeeded, WakeTarget}, Audiences: []Audience{AudienceSession, AudienceRoom}},
	{Family: FamilyAlert, Renderers: []string{"AlertCard"}, Producers: []EventProducer{ProducerDoctor, ProducerEngine, ProducerShell}, Wakes: []WakeRule{WakeRoot, WakeTarget}, Audiences: []Audience{AudienceRoom, AudienceSession}},
	{Family: FamilyService, Renderers: []string{"ServiceAlertCard", "ServiceWaitCard", "HelperOutcomeRow"}, Producers: []EventProducer{ProducerLibrarian, ProducerReviewer, ProducerHelper}, Wakes: []WakeRule{WakeRoot, WakeTarget}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyEngine, Renderers: []string{"EngineNoticeCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeRoot}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyQuestion, Renderers: []string{"QuestionNoteCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyApproval, Renderers: []string{"ApprovalRequestCard", "ApprovalDecisionRow", "ApprovalResultCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceRoom, AudienceSession}},
	{Family: FamilyTerminal, Renderers: []string{"TerminalRequestCard"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceRoom, AudienceSession}},
	{Family: FamilyShell, Renderers: []string{"ShellResultRow", "ShellSilenceCard"}, Producers: []EventProducer{ProducerShell}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceSession, AudienceRoom}},
	{Family: FamilyJob, Renderers: []string{"JobCard"}, Producers: []EventProducer{ProducerJobs}, Wakes: []WakeRule{WakeNever, WakeTarget}, Audiences: []Audience{AudienceSession}},
	{Family: FamilyRun, Renderers: []string{"RunResultCard"}, Producers: []EventProducer{ProducerWorkflow}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyMCP, Renderers: []string{"MCPCallRow"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeNever}, Audiences: []Audience{AudienceRoom}},
	{Family: FamilyTurn, Renderers: []string{"TurnCompletionRow"}, Producers: []EventProducer{ProducerEngine}, Wakes: []WakeRule{WakeTarget}, Audiences: []Audience{AudienceRoom}},
}

// TestEveryEventFamilyHasItsFacts fails when a family in the registry is
// missing from the table, or when a type widened a family's poster, wake rule,
// audience or card without the table saying so.
func TestEveryEventFamilyHasItsFacts(t *testing.T) {
	listed := map[EventFamily]familyFacts{}
	for _, facts := range eventFamilyFacts {
		if _, ok := listed[facts.Family]; ok {
			t.Fatalf("family %q is listed twice in eventFamilyFacts", facts.Family)
		}
		listed[facts.Family] = facts
	}
	seen := map[EventFamily]*familyFacts{}
	for _, spec := range eventSpecs {
		facts := seen[spec.Family]
		if facts == nil {
			facts = &familyFacts{Family: spec.Family}
			seen[spec.Family] = facts
		}
		facts.Renderers = appendUnique(facts.Renderers, spec.Renderer)
		producers := spec.Producers
		if len(producers) == 0 {
			// An event with no listed producer is the engine's own.
			producers = []EventProducer{ProducerEngine}
		}
		facts.Producers = appendUnique(facts.Producers, producers...)
		facts.Wakes = appendUnique(facts.Wakes, spec.Wake)
		audience := spec.Audience
		if audience == "" {
			audience = AudienceRoom
		}
		facts.Audiences = appendUnique(facts.Audiences, audience)
	}
	for family, facts := range seen {
		want, ok := listed[family]
		if !ok {
			t.Errorf("family %q is in the registry but not in eventFamilyFacts", family)
			continue
		}
		for _, field := range []struct {
			label string
			want  []string
			got   []string
		}{
			{"renderers", want.Renderers, facts.Renderers},
			{"producers", stringsOf(want.Producers), stringsOf(facts.Producers)},
			{"wake rules", stringsOf(want.Wakes), stringsOf(facts.Wakes)},
			{"audiences", stringsOf(want.Audiences), stringsOf(facts.Audiences)},
		} {
			if !slices.Equal(field.want, field.got) {
				t.Errorf("family %q %s = %v, the table says %v", family, field.label, field.got, field.want)
			}
		}
	}
	for family := range listed {
		if seen[family] == nil {
			t.Errorf("eventFamilyFacts lists %q, which no event uses", family)
		}
	}
}

// TestWebRegistryMatchesGo fails when the web card table and the generated
// registry disagree with Go: a post of a family Go does not know would draw a
// bare row, and an event whose card Go names but the web lacks would draw the
// unknown event row.
func TestWebRegistryMatchesGo(t *testing.T) {
	specs := map[string]bool{}
	for _, spec := range eventSpecs {
		specs[eventKey(string(spec.Family), string(spec.Type))] = true
	}
	known := map[string]bool{}
	for _, facts := range eventFamilyFacts {
		known[string(facts.Family)] = true
	}
	components := webEventComponents(t)
	entries := webEventSpecs(t)
	if len(entries) != len(eventSpecs) {
		t.Fatalf("the web registry has %d events and Go has %d", len(entries), len(eventSpecs))
	}
	for _, entry := range entries {
		if !known[entry.family] {
			t.Errorf("the web registry names the family %q, which Go does not know", entry.family)
		}
		if !specs[eventKey(entry.family, entry.typ)] {
			t.Errorf("the web registry names %s/%s, which is not registered in Go", entry.family, entry.typ)
		}
		if entry.renderer == "" {
			t.Errorf("the web registry names no card for %s/%s", entry.family, entry.typ)
			continue
		}
		if !components[entry.renderer] {
			t.Errorf("the web registry names the card %q for %s/%s, but eventRenderers.ts has no such component", entry.renderer, entry.family, entry.typ)
		}
	}
}

// webSpec is one entry of the generated web registry.
type webSpec struct{ family, typ, renderer string }

// webEventSpecs reads the generated registry's family, type and renderer of
// every event.
func webEventSpecs(t *testing.T) []webSpec {
	t.Helper()
	b, err := os.ReadFile(eventsFile)
	if err != nil {
		t.Fatal(err)
	}
	const start = "export const eventSpecs: readonly EventSpecEntry[] = ["
	i := bytes.Index(b, []byte(start))
	if i < 0 {
		t.Fatalf("%s has no eventSpecs table", eventsFile)
	}
	block := b[i:]
	if end := bytes.Index(block, []byte("\n]\n")); end >= 0 {
		block = block[:end]
	}
	re := regexp.MustCompile(`family: "([^"]*)",\s*\n\s*type: "([^"]*)",\s*\n\s*renderer: "([^"]*)",`)
	var out []webSpec
	for _, m := range re.FindAllStringSubmatch(string(block), -1) {
		out = append(out, webSpec{family: m[1], typ: m[2], renderer: m[3]})
	}
	return out
}

// webEventComponents reads the component names of the web card table.
func webEventComponents(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(renderersFile)
	if err != nil {
		t.Fatal(err)
	}
	const start = "export const eventComponents: Record<string, EventRenderer> = {"
	i := bytes.Index(b, []byte(start))
	if i < 0 {
		t.Fatalf("%s has no eventComponents table", renderersFile)
	}
	block := b[i+len(start):]
	if end := bytes.Index(block, []byte("\n}")); end >= 0 {
		block = block[:end]
	}
	out := map[string]bool{}
	for _, name := range strings.Split(string(block), ",") {
		name, _, _ = strings.Cut(name, ":")
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}

// eventKey names one event as family/type, as the web registry does.
func eventKey(family, typ string) string { return family + "/" + typ }

// appendUnique appends the values that the list does not hold yet.
func appendUnique[T comparable](list []T, values ...T) []T {
	for _, value := range values {
		if !slices.Contains(list, value) {
			list = append(list, value)
		}
	}
	return list
}

// stringsOf renders a list of named strings for a comparison.
func stringsOf[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func TestPostRejectsSealedFamily(t *testing.T) {
	store, rec := newEventStore(t)
	for _, spec := range eventSpecs {
		_, err := store.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Kind: string(spec.Family), Text: "sealed"})
		if err == nil || !strings.Contains(err.Error(), "PostEvent") {
			t.Fatalf("Post accepted the sealed family %q: %v", spec.Family, err)
		}
	}
}

// A run posts its start and then its result as two events of one run: the web
// folds them into one card, so both carry the same run ID and their own type.
func TestHookRunPostsStartAndResult(t *testing.T) {
	store, rec := newEventStore(t)
	run := HookEvent{RunID: "ab12", Repo: "/repo", Hook: "self-update", Event: "main-updated", Label: "Update Dyna", Command: "dyna", State: HookRunning}
	start, err := store.PostEvent(rec.ID, run, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if start.Kind != string(FamilyHook) || start.Notice.Kind != string(TypeStarted) || start.Wake || start.Deliverable() {
		t.Fatalf("stored start = %+v", start)
	}
	if !strings.Contains(start.Text, "Hook self-update started for main-updated") {
		t.Fatalf("start text = %q", start.Text)
	}
	run.State, run.DurationMS = HookPassed, 4000
	result, err := store.PostEvent(rec.ID, run, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if result.Notice.Kind != string(TypeResult) || start.ID == result.ID {
		t.Fatalf("stored result = %+v (start %s)", result, start.ID)
	}
	for _, msg := range []Message{start, result} {
		if got, ok := msg.Hook(); !ok || got.RunID != "ab12" || got.Hook != "self-update" || got.Repo != "/repo" {
			t.Fatalf("stored payload of %s = %+v, %v", msg.ID, got, ok)
		}
	}
	if got, _ := result.Hook(); got.State != HookPassed || got.DurationMS != 4000 {
		t.Fatalf("result payload = %+v", got)
	}
	if !strings.Contains(result.Text, "passed for main-updated after 4000 ms") {
		t.Fatalf("result text = %q", result.Text)
	}
	// A retried post of the same result keeps the first stored content.
	run.DurationMS = 9000
	if again, err := store.PostEvent(rec.ID, run, rec.Name); err != nil || again.ID != "" {
		t.Fatalf("repeated result = %+v, %v", again, err)
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("stored events = %+v, %v", msgs, err)
	}
}

// TestPostAcceptsEveryRegisteredKind proves that every kind the allow-list
// names can post. A caller with another kind must register it in events.go
// first, so the set stays closed on purpose.
func TestPostAcceptsEveryRegisteredKind(t *testing.T) {
	store, rec := newEventStore(t)
	for _, kind := range append(slices.Clone(voiceCallKinds), agentKinds...) {
		if _, err := store.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Kind: kind, Text: "kind " + kind}); err != nil {
			t.Errorf("kind %q: %v", kind, err)
		}
	}
}

func TestPostEventRequiresFieldsAndPostsOnce(t *testing.T) {
	store, rec := newEventStore(t)
	if _, err := store.PostEvent(rec.ID, WorktreeEvent{Owner: "s1", Path: "/w/a", Detail: " "}, rec.Name); err == nil {
		t.Fatal("PostEvent accepted an event with no reason")
	}
	ev := WorktreeEvent{Owner: "s1", Path: "/w/a", Branch: "a", Ahead: 1, Reason: WorktreeNotMerged, Detail: "HEAD is not merged into origin/main; cleanup kept it"}
	first, err := store.PostEvent(rec.ID, ev, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if first.From != EngineSender || first.Wake || first.Deliverable() || first.Kind != string(FamilyWorktree) || first.Notice.Kind != string(TypeKept) {
		t.Fatalf("stored event = %+v", first)
	}
	stored, ok := first.Worktree()
	if !ok || stored.Path != "/w/a" || stored.Ahead != 1 || stored.Reason != WorktreeNotMerged {
		t.Fatalf("stored payload = %+v, %v", stored, ok)
	}
	if !strings.Contains(first.Text, "branch a") || !strings.Contains(first.Text, "ahead 1") || !strings.Contains(first.Text, "not merged") {
		t.Fatalf("event text = %q", first.Text)
	}
	// The same tree and reason in a later pass posts nothing, even when the
	// counts changed.
	ev.Ahead = 2
	if again, err := store.PostEvent(rec.ID, ev, rec.Name); err != nil || again.ID != "" {
		t.Fatalf("duplicate event = %+v, %v", again, err)
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("stored events = %+v, %v", msgs, err)
	}
	if kept, _ := msgs[0].Worktree(); kept.Ahead != 1 {
		t.Fatalf("the first post changed: %+v", kept)
	}
}

func TestGoalEventDedupePreservesLegacyKeyWithoutCloseCount(t *testing.T) {
	event := GoalEvent{Type: GoalFinished, GoalID: "g1", Title: "Ship the contract", Summary: "Merged"}
	const legacyKey = "finished\x00Ship the contract\x00Merged\x000"
	if got := event.eventDedupe(); got != legacyKey {
		t.Fatalf("eventDedupe() = %q, want legacy key %q", got, legacyKey)
	}
}

// A PR event carries its facts in the typed payload: the card and the agent
// text read it, and the notice holds only the type.
func TestPREventStoresItsFactsAndText(t *testing.T) {
	store, rec := newEventStore(t)
	ev := PREvent{Type: PRCIFailed, Label: "PR #12", Number: 12, ChangeID: "https://github.com/a/b/pull/12", Key: "msg_0123456789abcdef",
		Location: "https://github.com/a/b/pull/12", Repo: "a/b", Head: "head1", URL: "https://github.com/a/b/pull/12", CIAttempt: 2, CIMax: 3, Summary: "unit, build",
		Detail: "the same fact line the agent already read", LogTail: "Log tail:\nboom"}
	msg, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: ev, To: []string{rec.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != string(FamilyPR) || msg.Notice.Kind != string(PRCIFailed) || !msg.Wake || msg.From != EngineSender {
		t.Fatalf("stored event = %+v", msg)
	}
	stored, ok := msg.PR()
	if !ok || stored.CIAttempt != 2 || stored.Summary != "unit, build" || stored.ChangeID != ev.ChangeID || stored.Number != 12 {
		t.Fatalf("stored payload = %+v, %v", stored, ok)
	}
	want := "https://github.com/a/b/pull/12\nHead: head1\n\nCI failed (2/3): unit, build. Fix the failure and push to this PR.\n\nthe same fact line the agent already read\n\nLog tail:\nboom"
	if msg.Text != want {
		t.Fatalf("event text =\n%q\nwant\n%q", msg.Text, want)
	}
	// A retry of the same notice keeps the same post.
	again, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: ev, To: []string{rec.Name}})
	if err != nil || again.ID != msg.ID {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	// Another comment on the same head is a second event, not a duplicate.
	second := ev
	second.CommentID, second.Key = "c1", "msg_0123456789abcdee"
	if other, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: second, To: []string{rec.Name}}); err != nil || other.ID == msg.ID {
		t.Fatalf("second comment = %+v, %v", other, err)
	}
}

// A PR event needs the facts every card reads, so an event without a label or
// a change fails instead of posting a blank card.
func TestPREventRequiresLabelAndChange(t *testing.T) {
	store, rec := newEventStore(t)
	for _, ev := range []PREvent{
		{Type: PRReady, ChangeID: "github.com/a/b#1"},
		{Type: PRReady, Label: "PR #1"},
		{Label: "PR #1", ChangeID: "github.com/a/b#1"},
	} {
		if _, err := store.PostEvent(rec.ID, ev, rec.Name); err == nil {
			t.Fatalf("PostEvent accepted %+v", ev)
		}
	}
}

// A merge trial posts its progress and its end as events of one trial: the
// web folds them into one card by the trial ID, and each event carries the
// facts the card shows.
func TestMergeTrialEventsFoldByTrial(t *testing.T) {
	store, rec := newEventStore(t)
	prs := []MergePR{{Number: 241, Title: "Port the timeline", URL: "https://github.com/a/b/pull/241"}}
	queued := MergeEvent{Type: MergeQueued, TrialID: "dyna/merge-trial/7", Repo: "a/b", Base: "main", PRs: prs}
	first, err := store.PostEvent(rec.ID, queued, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != string(FamilyMerge) || first.Notice.Kind != string(MergeQueued) || first.Wake || first.Deliverable() {
		t.Fatalf("stored queued event = %+v", first)
	}
	if !strings.Contains(first.Text, "Merge trial dyna/merge-trial/7 queued for main in a/b") {
		t.Fatalf("queued text = %q", first.Text)
	}
	landed := MergeEvent{Type: MergeLanded, TrialID: queued.TrialID, Repo: "a/b", Base: "main", PRs: prs, Commit: strings.Repeat("2", 40)}
	second, err := store.PostEvent(rec.ID, landed, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	// The landed event is a new post of the same trial, so one card folds it.
	if second.ID == first.ID || second.Notice.Kind != string(MergeLanded) {
		t.Fatalf("stored landed event = %+v", second)
	}
	for _, msg := range []Message{first, second} {
		got, ok := msg.Merge()
		if !ok || got.TrialID != queued.TrialID || len(got.PRs) != 1 || got.PRs[0].Number != 241 || got.Repo != "a/b" || got.Base != "main" {
			t.Fatalf("stored payload of %s = %+v, %v", msg.ID, got, ok)
		}
	}
	if got, _ := second.Merge(); got.Commit != landed.Commit {
		t.Fatalf("landed payload = %+v", got)
	}
	// The same fact in a later pass keeps its post, so a retried tick posts once.
	if again, err := store.PostEvent(rec.ID, landed, rec.Name); err != nil || again.ID != second.ID {
		t.Fatalf("retried landed event = %+v, %v", again, err)
	}
}

// A merge event without its trial, base or member PRs posts no blank card.
func TestMergeEventRequiresTrialBaseAndPRs(t *testing.T) {
	store, rec := newEventStore(t)
	prs := []MergePR{{Number: 1, Title: "a", URL: "https://github.com/a/b/pull/1"}}
	for _, ev := range []MergeEvent{
		{Type: MergeQueued, Repo: "a/b", Base: "main", PRs: prs},
		{Type: MergeQueued, TrialID: "t", Repo: "a/b", PRs: prs},
		{Type: MergeQueued, TrialID: "t", Repo: "a/b", Base: "main"},
	} {
		if _, err := store.PostEvent(rec.ID, ev, rec.Name); err == nil {
			t.Fatalf("PostEvent accepted %+v", ev)
		}
	}
}

// A recorded decision is one line with no trial. Each decision is its own post,
// even when the line repeats.
func TestMergeDecisionPostsOncePerDecision(t *testing.T) {
	store, rec := newEventStore(t)
	ev := MergeDecisionEvent{Note: "Keep the queue paused until the flake is fixed"}
	first, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: ev, FreshID: true, To: []string{rec.Name}, RoomService: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.PostEventInput(PostEventInput{RoomID: rec.ID, Event: ev, FreshID: true, To: []string{rec.Name}, RoomService: true})
	if err != nil || first.ID == second.ID {
		t.Fatalf("second decision = %+v, %v", second, err)
	}
	if first.Kind != string(FamilyMerge) || first.Notice.Kind != string(MergeDecision) || first.From != rec.Name {
		t.Fatalf("stored decision = %+v", first)
	}
	if first.Text != ev.Note || first.Wake {
		t.Fatalf("decision text = %q, wake %v", first.Text, first.Wake)
	}
	if got, ok := first.MergeDecision(); !ok || got.Note != ev.Note {
		t.Fatalf("stored payload = %+v, %v", got, ok)
	}
	if _, ok := first.Merge(); ok {
		t.Fatal("a decision read as a trial event")
	}
}

func TestReviewEventReadsRemovedAreaAndNote(t *testing.T) {
	m := Message{Kind: string(FamilyReview), Notice: EventNotice{Kind: string(TypeResult)}, Payload: EventPayload{raw: []byte(`{"kind":"approve-fast","changeId":"github.com/a/b#1","number":1,"label":"PR #1","location":"https://github.com/a/b/pull/1","head":"abc","fastLand":true,"area":"docs","note":"fast landing"}`)}}
	ev, ok := m.Review()
	if !ok || ev.Kind != ReviewApproveFast || ev.ChangeID != "github.com/a/b#1" || !ev.FastLand {
		t.Fatalf("old review payload = %+v, %v", ev, ok)
	}
	m.Payload.raw = []byte(`{"kind":"approve-fast","unknown":true}`)
	if _, ok := m.Review(); ok {
		t.Fatal("review reader accepted an unrelated unknown field")
	}
}

// A cache repair posts one quiet machine fact: the card and the agent text
// read the typed payload, a retry posts once, and a later repair is a new post.
func TestMachineCacheRepairPostsItsFactsOnce(t *testing.T) {
	store, rec := newEventStore(t)
	ev := MachineCacheEvent{
		Name:    "node-a",
		Caches:  []string{"Go build cache", "Go module cache"},
		Summary: "Go build cache and Go module cache",
		AtMS:    1700000000000,
	}
	msg, err := store.PostEvent(rec.ID, ev, rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Kind != string(FamilyMachine) || msg.Notice.Kind != string(TypeCacheRepaired) || msg.Wake || msg.Deliverable() {
		t.Fatalf("stored event = %+v", msg)
	}
	if !strings.Contains(msg.Text, "node-a switched linked CI caches") || !strings.Contains(msg.Text, "Go build cache and Go module cache") {
		t.Fatalf("event text = %q", msg.Text)
	}
	got, ok := msg.MachineCache()
	if !ok || got.Name != ev.Name || len(got.Caches) != 2 || got.Caches[1] != "Go module cache" || got.AtMS != ev.AtMS {
		t.Fatalf("stored payload = %+v, %v", got, ok)
	}
	if again, err := store.PostEvent(rec.ID, ev, rec.Name); err != nil || again.ID != msg.ID {
		t.Fatalf("retried repair = %+v, %v", again, err)
	}
	ev.AtMS++
	if next, err := store.PostEvent(rec.ID, ev, rec.Name); err != nil || next.ID == msg.ID {
		t.Fatalf("later repair = %+v, %v", next, err)
	}
}

func newEventStore(t *testing.T) (*Store, Record) {
	t.Helper()
	store := &Store{Root: t.TempDir()}
	rec, err := store.Create(CreateInput{Name: "events"})
	if err != nil {
		t.Fatal(err)
	}
	return store, rec
}

// generateEvents writes the TypeScript registry: one interface per payload,
// a string-literal union per named string type, a valid guard per event and
// the spec list the web renderers read.
func generateEvents() ([]byte, error) {
	var b strings.Builder
	b.WriteString("// Code generated by internal/room/events_test.go from internal/room/events.go. DO NOT EDIT.\n\n")

	for _, enum := range eventEnums {
		name := enum.Type.Name()
		fmt.Fprintf(&b, "// %s is a named string type of an event payload.\n", name)
		fmt.Fprintf(&b, "export type %s = %s\n\n", name, stringUnion(enum.Values))
	}

	// An embedded struct, such as one member PR, is its own interface, so a
	// card reads a typed object rather than a list of strings.
	for _, typ := range eventObjects {
		fields, err := payloadFields(typ)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "// %s is a struct of an event payload.\n", typ.Name())
		fmt.Fprintf(&b, "export interface %s {\n", typ.Name())
		for _, field := range fields {
			optional := ""
			if field.optional {
				optional = "?"
			}
			fmt.Fprintf(&b, "  %s%s: %s\n", field.name, optional, field.tsType)
		}
		b.WriteString("}\n\n")
	}

	// A payload can serve several event types, such as the start and the result
	// of one run, so each interface and its guard is written once.
	type payloadEntry struct {
		typ  reflect.Type
		keys []string
	}
	var order []*payloadEntry
	byName := map[string]*payloadEntry{}
	for _, spec := range eventSpecs {
		name := spec.Payload.Name()
		entry := byName[name]
		if entry == nil {
			entry = &payloadEntry{typ: spec.Payload}
			byName[name] = entry
			order = append(order, entry)
		}
		entry.keys = append(entry.keys, fmt.Sprintf("%s/%s", spec.Family, spec.Type))
	}

	var payloads []string
	for _, entry := range order {
		fields, err := payloadFields(entry.typ)
		if err != nil {
			return nil, err
		}
		name := entry.typ.Name()
		payloads = append(payloads, name)
		fmt.Fprintf(&b, "// %s is the payload of the %s events.\n", name, strings.Join(entry.keys, " and "))
		fmt.Fprintf(&b, "export interface %s {\n", name)
		for _, field := range fields {
			optional := ""
			if field.optional {
				optional = "?"
			}
			fmt.Fprintf(&b, "  %s%s: %s\n", field.name, optional, field.tsType)
		}
		b.WriteString("}\n\n")
	}

	fmt.Fprintf(&b, "// EventPayload is the payload of any registered system event.\nexport type EventPayload = %s\n\n", strings.Join(payloads, " | "))

	var familyList, typesList []string
	for _, spec := range eventSpecs {
		if !slices.Contains(familyList, string(spec.Family)) {
			familyList = append(familyList, string(spec.Family))
		}
		if !slices.Contains(typesList, string(spec.Type)) {
			typesList = append(typesList, string(spec.Type))
		}
	}
	fmt.Fprintf(&b, "export type EventFamily = %s\n", stringUnion(familyList))
	fmt.Fprintf(&b, "export type EventType = %s\n", stringUnion(typesList))
	fmt.Fprintf(&b, "export type WakeRule = %s\n\n", stringUnion([]string{string(WakeNever), string(WakeTarget), string(WakeRootIfNeeded), string(WakeRoot)}))

	fmt.Fprintf(&b, "export type Audience = %s\n\n", stringUnion([]string{string(AudienceRoom), string(AudienceSession)}))
	b.WriteString("export interface EventSpecEntry {\n  family: EventFamily\n  type: EventType\n  renderer: string\n  quiet: boolean\n  wake: WakeRule\n  once: boolean\n  audience: Audience\n  text: string\n  textNoSubject?: string\n}\n\n")
	b.WriteString("export const eventSpecs: readonly EventSpecEntry[] = [\n")
	for _, spec := range eventSpecs {
		b.WriteString("  {\n")
		fmt.Fprintf(&b, "    family: %s,\n", jsonString(string(spec.Family)))
		fmt.Fprintf(&b, "    type: %s,\n", jsonString(string(spec.Type)))
		fmt.Fprintf(&b, "    renderer: %s,\n", jsonString(spec.Renderer))
		fmt.Fprintf(&b, "    quiet: %t,\n", spec.Quiet)
		fmt.Fprintf(&b, "    wake: %s,\n", jsonString(string(spec.Wake)))
		fmt.Fprintf(&b, "    once: %t,\n", spec.Once)
		audience := spec.Audience
		if audience == "" {
			audience = AudienceRoom
		}
		fmt.Fprintf(&b, "    audience: %s,\n", jsonString(string(audience)))
		fmt.Fprintf(&b, "    text: %s,\n", jsonString(spec.Text))
		if spec.TextNoSubject != "" {
			fmt.Fprintf(&b, "    textNoSubject: %s,\n", jsonString(spec.TextNoSubject))
		}
		b.WriteString("  },\n")
	}
	b.WriteString("]\n\n")

	for _, enum := range eventEnums {
		name := enum.Type.Name()
		fmt.Fprintf(&b, "const %s: ReadonlySet<unknown> = new Set([%s])\n", enumSetName(name), quotedList(enum.Values))
	}
	b.WriteString("\n")

	for _, typ := range eventObjects {
		fields, err := payloadFields(typ)
		if err != nil {
			return nil, err
		}
		name := typ.Name()
		fmt.Fprintf(&b, "export function is%s(v: unknown): v is %s {\n", name, name)
		conditions := []string{"isRecord(v)"}
		for _, field := range fields {
			conditions = append(conditions, field.check)
		}
		fmt.Fprintf(&b, "  return %s\n}\n\n", strings.Join(conditions, " &&\n    "))
		fmt.Fprintf(&b, "export function is%sList(v: unknown): v is %s[] {\n", name, name)
		fmt.Fprintf(&b, "  return Array.isArray(v) && v.every((item: unknown) => is%s(item))\n}\n\n", name)
	}

	for _, entry := range order {
		fields, err := payloadFields(entry.typ)
		if err != nil {
			return nil, err
		}
		name := entry.typ.Name()
		fmt.Fprintf(&b, "export function is%s(v: unknown): v is %s {\n", name, name)
		conditions := []string{"isRecord(v)"}
		for _, field := range fields {
			conditions = append(conditions, field.check)
		}
		fmt.Fprintf(&b, "  return %s\n}\n\n", strings.Join(conditions, " &&\n    "))
	}

	b.WriteString("export function isEventPayload(family: string | undefined, type: string | undefined, v: unknown): v is EventPayload {\n")
	for _, spec := range eventSpecs {
		fmt.Fprintf(&b, "  if (family === %s && type === %s) return is%s(v)\n", jsonString(string(spec.Family)), jsonString(string(spec.Type)), spec.Payload.Name())
	}
	b.WriteString("  return false\n}\n\n")

	b.WriteString("function isRecord(v: unknown): v is Record<string, unknown> {\n  return typeof v === \"object\" && v !== null && !Array.isArray(v)\n}\n\n")
	b.WriteString("function isStringList(v: unknown): v is string[] {\n  return Array.isArray(v) && v.every((item: unknown) => typeof item === \"string\")\n}\n")
	b.WriteString("function isNumberList(v: unknown): v is number[] {\n  return Array.isArray(v) && v.every((item: unknown) => Number.isInteger(item))\n}\n")

	return []byte(b.String()), nil
}

// payloadField is one json field of an event payload.
type payloadField struct {
	name     string
	tsType   string
	optional bool
	check    string
}

// payloadFields reads the payload struct: a field is optional when its json
// tag says omitempty, and PostEvent rejects an empty required field.
func payloadFields(typ reflect.Type) ([]payloadField, error) { return fieldsOf(typ, "v") }

// fieldsOf reads the fields of one struct, reading each from the expression
// base. A nested struct reads its fields from the field's own expression.
func fieldsOf(typ reflect.Type, base string) ([]payloadField, error) {
	var out []payloadField
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		optional := strings.Contains(options, "omitempty")
		// A pointer says the value can be absent, so its tag must say so too.
		if field.Type.Kind() == reflect.Pointer && !optional {
			return nil, fmt.Errorf("%s.%s is a pointer without omitempty", typ.Name(), field.Name)
		}
		tsType, check, err := tsField(field.Type, base+"."+name)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", typ.Name(), field.Name, err)
		}
		if optional {
			check = fmt.Sprintf("(%s === undefined || %s)", base+"."+name, check)
		}
		out = append(out, payloadField{name: name, tsType: tsType, optional: optional, check: check})
	}
	return out, nil
}

// tsField maps one Go field type to its TypeScript type and runtime guard.
// tsField maps one Go field type to its TypeScript type and runtime guard.
func tsField(typ reflect.Type, expr string) (string, string, error) {
	switch typ.Kind() {
	case reflect.String:
		if typ.PkgPath() != "" {
			name := typ.Name()
			// An open named type holds values the web cannot list, such as a
			// session address, so it reads as a plain string.
			if hasOpenString(name) {
				return "string", fmt.Sprintf("typeof %s === \"string\"", expr), nil
			}
			if !hasEnum(name) {
				return "", "", fmt.Errorf("named string type %s has no eventEnums entry", name)
			}
			return name, fmt.Sprintf("%s.has(%s)", enumSetName(name), expr), nil
		}
		return "string", fmt.Sprintf("typeof %s === \"string\"", expr), nil
	case reflect.Int, reflect.Int64:
		return "number", fmt.Sprintf("Number.isInteger(%s)", expr), nil
	case reflect.Bool:
		return "boolean", fmt.Sprintf("typeof %s === \"boolean\"", expr), nil
	case reflect.Struct:
		// An event object gets its own interface and guard; any other struct is
		// written inline, so a payload can carry a small fact such as a close
		// origin without a registry entry.
		if hasObject(typ) {
			return typ.Name(), fmt.Sprintf("is%s(%s)", typ.Name(), expr), nil
		}
		return structField(typ, expr)
	case reflect.Pointer:
		elem := typ.Elem()
		if elem.Kind() != reflect.Struct {
			return tsField(elem, expr)
		}
		if hasObject(elem) {
			return elem.Name(), fmt.Sprintf("is%s(%s)", elem.Name(), expr), nil
		}
		return structField(elem, expr)
	case reflect.Slice:
		elem := typ.Elem()
		if elem.Kind() == reflect.Struct && hasObject(elem) {
			return elem.Name() + "[]", fmt.Sprintf("is%sList(%s)", elem.Name(), expr), nil
		}
		if elem.Kind() == reflect.Struct {
			tsType, check, err := structField(elem, "item")
			if err != nil {
				return "", "", err
			}
			return tsType + "[]", fmt.Sprintf("Array.isArray(%s) && %s.every((item: unknown) => %s)", expr, expr, check), nil
		}
		if elem.Kind() == reflect.String && elem.PkgPath() == "" {
			return "string[]", fmt.Sprintf("isStringList(%s)", expr), nil
		}
		if elem.Kind() == reflect.Int {
			return "number[]", fmt.Sprintf("isNumberList(%s)", expr), nil
		}
		return "", "", fmt.Errorf("slice type %s is not supported", typ)
	}
	return "", "", fmt.Errorf("type %s is not supported", typ)
}

// structField writes one nested struct as an inline TypeScript object type and
// a guard that checks each of its fields.
func structField(typ reflect.Type, expr string) (string, string, error) {
	fields, err := fieldsOf(typ, expr)
	if err != nil {
		return "", "", err
	}
	parts := []string{"isRecord(" + expr + ")"}
	members := make([]string, 0, len(fields))
	for _, field := range fields {
		optional := ""
		if field.optional {
			optional = "?"
		}
		members = append(members, fmt.Sprintf("%s%s: %s", field.name, optional, field.tsType))
		parts = append(parts, field.check)
	}
	return "{ " + strings.Join(members, "; ") + " }", "(" + strings.Join(parts, " && ") + ")", nil
}

func hasObject(typ reflect.Type) bool {
	return slices.ContainsFunc(eventObjects, func(object reflect.Type) bool { return object == typ })
}

func hasEnum(name string) bool {
	for _, enum := range eventEnums {
		if enum.Type.Name() == name {
			return true
		}
	}
	return false
}

// enumSetName is the runtime set of a named string type, as in WORKTREE_REASON.
func enumSetName(name string) string {
	var out strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToUpper(r))
	}
	return out.String()
}

func hasOpenString(name string) bool {
	return slices.ContainsFunc(eventOpenStrings, func(open reflect.Type) bool { return open.Name() == name })
}

func stringUnion(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = jsonString(value)
	}
	return strings.Join(quoted, " | ")
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = jsonString(value)
	}
	return strings.Join(quoted, ", ")
}

func jsonString(value string) string {
	b, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(b)
}
