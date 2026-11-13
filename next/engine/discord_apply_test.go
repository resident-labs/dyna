package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
)

// emptyServer is a server with only @everyone and the bot's managed role.
func emptyServer() fakediscord.Configuration {
	return fakediscord.Configuration{
		Guild:    fakediscord.Guild{ID: "100", Name: "Horizon"},
		Roles:    []fakediscord.Role{{ID: "100", Name: "@everyone", Permissions: "1024"}, {ID: "11", Name: "Kira", Managed: true, Position: 50, Permissions: "0"}},
		BotRoles: []string{"11"},
	}
}

const applyServerYAML = `settings: {verification: medium, notifications: mentions, community: {rules: rules, updates: news}}
categories: [Community, Staff]
roles:
  - {name: Kira, managed: true, id: "11"}
  - {name: Moderator, permissions: [manage_messages], color: "#e67e22", hoist: true}
  - {name: Helper, permissions: [kick_members]}
  - {name: "@everyone", id: "100", permissions: [view_channel]}
automod:
  - {name: no-invites, keywords: ["discord.gg/*"], block: true, alert: mod-log}
`

var applyFolder = map[string]string{
	"server.yaml":                           applyServerYAML,
	"policy.yaml":                           "front: {roles: [Admin], quiet: [general], delete: yes}\n",
	"channels/Community/category.yaml":      "sync: true\nchannels: [rules, news, help]\n",
	"channels/Community/news/channel.yaml":  "topic: Updates\n",
	"channels/Community/help/channel.yaml":  "type: forum\ntags: [question, bug]\nslowmode: 10s\n",
	"channels/Staff/category.yaml":          "sync: true\nchannels: [mod-log]\nallow: {Moderator: [view_channel]}\ndeny: {everyone: [view_channel]}\n",
	"channels/Staff/mod-log/channel.yaml":   "allow: {Moderator: [view_channel]}\ndeny: {everyone: [view_channel]}\n",
	"channels/Community/rules/rules.md":     "---\ntitle: Rules\ncolor: \"#e67e22\"\npin: true\n---\nBe kind. See #help, ask @Moderator.\n\n## Respect\nNo harassment.\n",
	"channels/Community/rules/channel.yaml": "topic: Read first\n",
}

type applyTest struct {
	discordTestFixture
	t    *testing.T
	root string
}

func newApplyTest(t *testing.T, server fakediscord.Configuration, files map[string]string) applyTest {
	f := discordFixture(t)
	f.s.layout.Data = t.TempDir()
	f.fake.SetConfiguration(server)
	_, root, problem := f.s.discordFolderRoute(f.ctx, contract.Principal{Kind: contract.PrincipalOwner}, f.room, "")
	if problem != nil {
		t.Fatal(problem)
	}
	a := applyTest{f, t, root}
	for name, content := range files {
		a.write(name, content)
	}
	return a
}

func (a applyTest) write(name, content string) {
	a.t.Helper()
	path := filepath.Join(a.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		a.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		a.t.Fatal(err)
	}
}

func (a applyTest) read(name string) string {
	data, err := os.ReadFile(filepath.Join(a.root, name))
	if err != nil {
		a.t.Fatal(err)
	}
	return string(data)
}

func (a applyTest) apply(prune bool) string {
	a.t.Helper()
	response, problem := a.s.DiscordApply(a.ctx, boxCaller(a.rootSession()), contract.DiscordApplyRequest{Prune: prune})
	if problem != nil {
		a.t.Fatalf("apply refused: %s", problem.Message)
	}
	return response.Text
}

func (a applyTest) rootSession() contract.SessionID { return a.discordTestFixture.root }

func (a applyTest) plan() string {
	a.t.Helper()
	response, problem := a.s.DiscordPlan(a.ctx, boxCaller(a.rootSession()), contract.DiscordPlanRequest{})
	if problem != nil {
		a.t.Fatalf("plan refused: %s", problem.Message)
	}
	return response.Text
}

func (a applyTest) refusal() string {
	a.t.Helper()
	_, problem := a.s.DiscordApply(a.ctx, boxCaller(a.rootSession()), contract.DiscordApplyRequest{})
	if problem == nil {
		a.t.Fatal("apply was not refused")
	}
	return problem.Message
}

func (a applyTest) writes() int { return len(a.fake.Writes()) }

func (a applyTest) channelNamed(name string) (fakediscord.Channel, bool) {
	for _, channel := range a.fake.Configuration().Channels {
		if channel.Name == name {
			return channel, true
		}
	}
	return fakediscord.Channel{}, false
}

func TestApplyThenPlanIsEmptyAndRepeatWritesNothing(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	if report := a.apply(false); strings.Contains(report, "Failed") || !strings.Contains(report, "all done") {
		t.Fatalf("first apply:\n%s", report)
	}
	if plan := a.plan(); !strings.Contains(plan, "0 changes") {
		t.Fatalf("plan after apply:\n%s", plan)
	}
	before := a.writes()
	if report := a.apply(false); a.writes() != before || !strings.Contains(report, "0 of 0 steps") {
		t.Fatalf("second apply wrote %d times:\n%s", a.writes()-before, report)
	}
	copies, _ := os.ReadDir(filepath.Join(filepath.Dir(a.root), "discord-applied", "100"))
	if len(copies) != 1 {
		t.Fatalf("expected one applied copy, got %d", len(copies))
	}
}

func TestApplyAfterExportWritesNothing(t *testing.T) {
	a := newApplyTest(t, fakediscord.CommunityConfiguration(), nil)
	if _, problem := a.s.DiscordExport(a.ctx, boxCaller(a.rootSession()), contract.DiscordExportRequest{}); problem != nil {
		t.Fatal(problem.Message)
	}
	report := a.apply(false)
	if a.writes() != 0 {
		t.Fatalf("apply after export wrote %v:\n%s", a.fake.Writes(), report)
	}
}

func TestApplyWritesIDAndFollowsAHandMove(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	a.apply(false)
	news, _ := a.channelNamed("news")
	if !strings.Contains(a.read("channels/Community/news/channel.yaml"), `id: "`+news.ID+`"`) {
		t.Fatalf("news has no id:\n%s", a.read("channels/Community/news/channel.yaml"))
	}
	if !strings.Contains(a.read("channels/Community/rules/channel.yaml"), "id:") || !strings.Contains(a.read("server.yaml"), "name: Helper") {
		t.Fatal("ids missing")
	}
	configuration := a.fake.Configuration()
	staff, _ := a.channelNamed("Staff")
	for i := range configuration.Channels {
		if configuration.Channels[i].ID == news.ID {
			configuration.Channels[i].Parent = staff.ID
		}
	}
	a.fake.SetConfiguration(configuration)
	plan := a.plan()
	if strings.Contains(plan, "Create channel") || !strings.Contains(plan, "Parent") {
		t.Fatalf("hand move planned wrongly:\n%s", plan)
	}
}

func TestApplyRenamesByWasWithOnePatch(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	a.apply(false)
	if err := os.Rename(filepath.Join(a.root, "channels/Community/news"), filepath.Join(a.root, "channels/Community/headlines")); err != nil {
		t.Fatal(err)
	}
	a.write("channels/Community/headlines/channel.yaml", "was: news\ntopic: Updates\n")
	a.write("channels/Community/category.yaml", "sync: true\nchannels: [rules, headlines, help]\n")
	a.write("server.yaml", strings.Replace(applyServerYAML, "updates: news", "updates: headlines", 1))
	before := a.writes()
	report := a.apply(false)
	writes := a.fake.Writes()[before:]
	if len(writes) != 1 || writes[0].Method != "PATCH" || !strings.Contains(writes[0].Body, "headlines") {
		t.Fatalf("rename made %v:\n%s", writes, report)
	}
	if _, found := a.channelNamed("headlines"); !found {
		t.Fatal("not renamed")
	}
	before = a.writes()
	a.apply(false)
	if a.writes() != before {
		t.Fatal("second apply wrote")
	}
}

func TestApplyStopsOnFailureAndResumesWithoutDuplicates(t *testing.T) {
	for _, appliedBeforeFailure := range []bool{false, true} {
		a := newApplyTest(t, emptyServer(), applyFolder)
		a.fake.FailWrite(4, 500, appliedBeforeFailure)
		report := a.apply(false)
		if !strings.Contains(report, "Failed:") || !strings.Contains(report, "Not done:") {
			t.Fatalf("no failure reported:\n%s", report)
		}
		if appliedBeforeFailure {
			// The outcome is unknown and something new exists: apply must not guess.
			before := a.writes()
			if message := a.refusal(); !strings.Contains(message, "unclear outcome") || a.writes() != before {
				t.Fatalf("resume guessed: %s", message)
			}
			continue
		}
		if second := a.apply(false); strings.Contains(second, "Failed") {
			t.Fatalf("resume failed:\n%s", second)
		}
		if plan := a.plan(); !strings.Contains(plan, "0 changes") {
			t.Fatalf("plan after resume:\n%s", plan)
		}
		names := map[string]bool{}
		for _, role := range a.fake.Configuration().Roles {
			if names[role.Name] {
				t.Fatalf("duplicate role %s (unknown outcome applied: %v)", role.Name, appliedBeforeFailure)
			}
			names[role.Name] = true
		}
		for _, channel := range a.fake.Configuration().Channels {
			if names[channel.ID+channel.Name] {
				t.Fatalf("duplicate channel %s", channel.Name)
			}
			names[channel.ID+channel.Name] = true
		}
	}
}

func TestApplyNeverTouchesUnmanagedManagedOrUnknownBits(t *testing.T) {
	server := fakediscord.CompatibilityConfiguration()
	a := newApplyTest(t, server, nil)
	if _, problem := a.s.DiscordExport(a.ctx, boxCaller(a.rootSession()), contract.DiscordExportRequest{}); problem != nil {
		t.Fatal(problem.Message)
	}
	// Change the moderator's colour, and drop the folder's channels and roles to tempt a prune.
	yaml := a.read("server.yaml")
	a.write("server.yaml", strings.Replace(yaml, "color: '#000000'", "color: '#112233'", 1))
	report := a.apply(true)
	colored := false
	for _, write := range a.fake.Writes() {
		if strings.Contains(write.Path, "/roles/11") || strings.Contains(write.Path, "/channels/22") || strings.Contains(write.Path, "/channels/23") {
			t.Fatalf("touched something it must not: %v\n%s", write, report)
		}
		colored = colored || (strings.Contains(write.Path, "/roles/10") && strings.Contains(write.Body, "1122867"))
		if strings.Contains(write.Path, "/roles/10") && strings.Contains(write.Body, "permissions") && !strings.Contains(write.Body, "9223372036854784000") {
			t.Fatalf("role write dropped the unknown permission bits: %v", write)
		}
	}
	if !colored {
		t.Fatalf("the moderator colour was not changed:\n%s", report)
	}
}

func TestPruneDeletesOnlyRecordedObjects(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	a.write("channels/Community/old-chat/channel.yaml", "topic: soon gone\n")
	a.write("channels/Community/category.yaml", "sync: true\nchannels: [rules, news, help, old-chat]\n")
	a.apply(false)
	stranger := a.fake.Configuration()
	stranger.Channels = append(stranger.Channels, fakediscord.Channel{ID: "9001", Name: "stranger", Type: 0})
	a.fake.SetConfiguration(stranger)
	if err := os.RemoveAll(filepath.Join(a.root, "channels/Community/old-chat")); err != nil {
		t.Fatal(err)
	}
	a.write("channels/Community/category.yaml", "sync: true\nchannels: [rules, news, help]\n")
	before := a.writes()
	report := a.apply(true)
	var deletes []string
	for _, write := range a.fake.Writes()[before:] {
		if write.Method == "DELETE" {
			deletes = append(deletes, write.Path)
		}
	}
	if _, found := a.channelNamed("old-chat"); found || len(deletes) != 1 {
		t.Fatalf("deletes %v:\n%s", deletes, report)
	}
	if _, found := a.channelNamed("stranger"); !found {
		t.Fatal("an unrecorded channel was deleted")
	}
}

func TestPruneRefusesACategoryHoldingAnUnownedChannel(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	a.apply(false)
	configuration := a.fake.Configuration()
	staff, _ := a.channelNamed("Staff")
	configuration.Channels = append(configuration.Channels, fakediscord.Channel{ID: "9002", Name: "private-notes", Type: 0, Parent: staff.ID})
	a.fake.SetConfiguration(configuration)
	if err := os.RemoveAll(filepath.Join(a.root, "channels/Staff")); err != nil {
		t.Fatal(err)
	}
	a.write("server.yaml", strings.NewReplacer("categories: [Community, Staff]", "categories: [Community]", ", alert: mod-log", "").Replace(applyServerYAML))
	before := a.writes()
	message := a.refusalWithPrune()
	if a.writes() != before || !strings.Contains(message, "private-notes") {
		t.Fatalf("prune went ahead or did not name the channel: %s", message)
	}
}

func (a applyTest) refusalWithPrune() string {
	_, problem := a.s.DiscordApply(a.ctx, boxCaller(a.rootSession()), contract.DiscordApplyRequest{Prune: true})
	if problem == nil {
		a.t.Fatal("prune was not refused")
	}
	return problem.Message
}

func TestApplyEditsAMessageInPlaceAndRefusesUnknownNames(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	a.apply(false)
	a.write("channels/Community/rules/rules.md", "---\ntitle: Rules\ncolor: \"#e67e22\"\npin: true\n---\nBe kind and patient.\n")
	before := a.writes()
	report := a.apply(false)
	writes := a.fake.Writes()[before:]
	if len(writes) != 1 || writes[0].Method != "PATCH" || !strings.Contains(writes[0].Path, "/messages/") || !strings.Contains(writes[0].Body, "patient") {
		t.Fatalf("message edit made %v:\n%s", writes, report)
	}
	a.write("channels/Community/rules/rules.md", "Title line\n\nSee #nowhere for more.\n")
	if message := a.refusal(); !strings.Contains(message, "rules.md: line 3: unknown channel #nowhere") {
		t.Fatalf("refusal: %s", message)
	}
}

func TestApplyRefusesToAdoptByNameAlone(t *testing.T) {
	server := emptyServer()
	server.Channels = []fakediscord.Channel{{ID: "7001", Name: "lobby", Type: 0}}
	a := newApplyTest(t, server, map[string]string{"server.yaml": "settings: {}\nroles:\n  - {name: Kira, managed: true, id: \"11\"}\n  - {name: \"@everyone\", id: \"100\", permissions: [view_channel]}\n", "channels/lobby/channel.yaml": "topic: hi\n"})
	if message := a.refusal(); !strings.Contains(message, "does not adopt it") || a.writes() != 0 {
		t.Fatalf("adopted by name: %s", message)
	}
}

func TestApplyAuthorization(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	front := startChild(t, a.s, a.ctx, a.room, "front", a.rootSession())
	if _, problem := a.s.DiscordApply(a.ctx, boxCaller(front), contract.DiscordApplyRequest{}); problem == nil {
		t.Fatal("a non-root session applied")
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, problem := a.s.DiscordApply(a.ctx, owner, contract.DiscordApplyRequest{}); problem == nil {
		t.Fatal("owner without room accepted")
	}
	if _, problem := a.s.DiscordApply(a.ctx, owner, contract.DiscordApplyRequest{Room: a.room}); problem != nil {
		t.Fatalf("owner with room refused: %s", problem.Message)
	}
}

func TestApplyDoesNotAdoptAHumanObjectAfterAnUnclearCreate(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	a.fake.FailWrite(1, 500, false) // the first create fails and did not happen
	a.apply(false)
	human := a.fake.Configuration()
	human.Roles = append(human.Roles, fakediscord.Role{ID: "8001", Name: "Moderator", Permissions: "8"})
	a.fake.SetConfiguration(human)
	before := a.writes()
	if message := a.refusal(); !strings.Contains(message, "8001") || a.writes() != before {
		t.Fatalf("a human role was adopted or the refusal is unclear: %s", message)
	}
}

func TestApplyRefusesToAdoptAnAutomaticallyUnmanagedChannel(t *testing.T) {
	server := emptyServer()
	server.Channels = []fakediscord.Channel{{ID: "700", Name: "stage", Type: 13}}
	a := newApplyTest(t, server, map[string]string{"server.yaml": "settings: {}\nroles:\n  - {name: Kira, managed: true, id: \"11\"}\n  - {name: \"@everyone\", id: \"100\", permissions: [view_channel]}\n", "channels/stage/channel.yaml": "id: \"700\"\ntopic: overwritten\n"})
	if message := a.refusal(); !strings.Contains(message, "not managed by this folder") || a.writes() != 0 {
		t.Fatalf("wrote to an unmanaged channel: %s", message)
	}
}

func TestApplyDoesNotRecreateAChannelMovedUnderAnUnmanagedCategory(t *testing.T) {
	server := emptyServer()
	server.Channels = []fakediscord.Channel{{ID: "800", Name: "External", Type: 4}}
	files := map[string]string{"channels/External/UNMANAGED.md": "owned elsewhere\n"}
	for name, content := range applyFolder {
		files[name] = content
	}
	files["server.yaml"] = strings.Replace(applyServerYAML, "categories: [Community, Staff]", "categories: [Community, Staff, External]", 1)
	a := newApplyTest(t, server, files)
	a.fake.FailWrite(4, 500, true) // a channel is created, then the answer is lost
	a.apply(false)
	moved := a.fake.Configuration()
	for i := range moved.Channels {
		if moved.Channels[i].Name == "news" {
			moved.Channels[i].Parent = "800"
		}
	}
	a.fake.SetConfiguration(moved)
	a.refusal()
	count := 0
	for _, channel := range a.fake.Configuration().Channels {
		if channel.Name == "news" {
			count++
		}
	}
	if count > 1 {
		t.Fatal("news was created twice")
	}
}

func TestPruneRefusesACategoryHoldingAnUnmanagedChannel(t *testing.T) {
	a := newApplyTest(t, emptyServer(), applyFolder)
	a.apply(false)
	configuration := a.fake.Configuration()
	for i := range configuration.Channels {
		if configuration.Channels[i].Name == "mod-log" {
			configuration.Channels[i].NSFW = true // makes it unmanaged
		}
	}
	a.fake.SetConfiguration(configuration)
	a.write("server.yaml", strings.NewReplacer("categories: [Community, Staff]", "categories: [Community]", ", alert: mod-log", "").Replace(applyServerYAML))
	if err := os.RemoveAll(filepath.Join(a.root, "channels/Staff")); err != nil {
		t.Fatal(err)
	}
	before := a.writes()
	if message := a.refusalWithPrune(); !strings.Contains(message, "mod-log") || a.writes() != before {
		t.Fatalf("category with an unmanaged child was deleted: %s", message)
	}
}

func TestApplyRefusesManagedFlagOnAnOrdinaryRole(t *testing.T) {
	server := emptyServer()
	server.Roles = append(server.Roles, fakediscord.Role{ID: "30", Name: "A", Position: 2, Permissions: "0"}, fakediscord.Role{ID: "31", Name: "B", Position: 1, Permissions: "0"})
	yaml := "settings: {}\nroles:\n  - {name: Kira, managed: true, id: \"11\"}\n  - {name: B, managed: true}\n  - {name: A, managed: true}\n  - {name: \"@everyone\", id: \"100\", permissions: [view_channel]}\n"
	a := newApplyTest(t, server, map[string]string{"server.yaml": yaml})
	if message := a.refusal(); !strings.Contains(message, "managed") || a.writes() != 0 {
		t.Fatalf("reordered unadopted roles: %s", message)
	}
}
