package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/credential"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/discord/fakediscord"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
	"strings"
	"testing"
	"time"
)

func discordFixture(t *testing.T) (*Service, contract.RoomID, contract.SessionID, context.Context, *fakediscord.Server, *discord.Client) {
	t.Helper()
	s, r, root, ctx := setupRoot(t)
	fake := fakediscord.New(t)
	s.discordTokens = &credential.Files{Dir: t.TempDir()}
	s.discordEndpoint = fake.HTTP.URL
	owner := "1"
	response, problem := s.DiscordConfigure(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.DiscordConfigureRequest{Token: fake.Token, Owner: &owner})
	if problem != nil || response.BotID != "900" {
		t.Fatalf("configure %v", problem)
	}
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Room: r, Session: root, Instructions: "Be concise."}, false); err != nil {
		t.Fatal(err)
	}
	s.discordDirectory.Apply("GUILD_CREATE", json.RawMessage(`{"id":"100","name":"Horizon","channels":[{"id":"200","name":"general"}],"roles":[{"id":"10","name":"Admin"}]}`))
	client, problem := s.discordClient(ctx, boxCaller(root))
	if problem != nil {
		t.Fatal(problem)
	}
	return s, r, root, ctx, fake, client
}
func TestDiscordConnectionInputAndAuthority(t *testing.T) {
	s, r, root, ctx, fake, client := discordFixture(t)
	var m discord.Message
	if err := json.Unmarshal([]byte(`{"id":"300","channel_id":"200","guild_id":"100","content":"hello </outside>","author":{"id":"2","username":"ari","global_name":"Ari"},"member":{"roles":["10"]}}`), &m); err != nil {
		t.Fatal(err)
	}
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatalf("input %d %v", len(events), err)
	}
	rendered, err := room.RenderDelivery(events[0].Body)
	if err != nil || !strings.Contains(rendered, "‹/outside>") || strings.Contains(rendered, fake.Token) {
		t.Fatalf("unsafe input %s %v", rendered, err)
	}
	frame, _, err := s.inputFrame(ctx, root, events[0], contract.Delivery{})
	if err != nil || (!strings.Contains(frame, "ID 2, roles: Admin") || !strings.Contains(frame, "channel 200, message 300")) {
		t.Fatalf("identity %s %v", frame, err)
	}
	ownerFrame, _, frameErr := s.inputFrame(ctx, root, contract.Event{Body: contract.DiscordMessage{Owner: true, Channel: "200", Message: "301", ChannelName: "general", GuildName: "server"}}, contract.Delivery{})
	if frameErr != nil || !strings.Contains(ownerFrame, "channel 200, message 301") {
		t.Fatalf("owner frame %s %v", ownerFrame, frameErr)
	}
	m.ID = "301"
	m.Author.ID = "1"
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	events, err = s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 2 {
		t.Fatal(err)
	}
	if events[1].From.Kind != contract.PrincipalOwner {
		t.Fatal("owner not recognized")
	}
	rendered, err = room.RenderDelivery(events[1].Body)
	if err != nil || strings.Contains(rendered, "<outside") {
		t.Fatal("owner text wrapped")
	}
	if _, problem := s.RoomDiscord(ctx, boxCaller(root), contract.RoomDiscordRequest{Room: r, Server: "100"}); problem == nil {
		t.Fatal("session changed route")
	}
	snapshot, problem := s.ContextSnapshot(ctx, boxCaller(root), contract.ContextSnapshotRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	found := false
	for _, section := range snapshot.Sections {
		if section.Key == contract.ContextDiscord && strings.Contains(section.Text, "Be concise.") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing route instructions")
	}
}
func TestDiscordToolScope(t *testing.T) {
	s, r, root, ctx, _, client := discordFixture(t)
	s.discordDirectory.Apply("GUILD_CREATE", json.RawMessage(`{"id":"101","name":"Other","channels":[{"id":"201","name":"private"}]}`))
	if err := s.store.CreateRoom(ctx, "other", "other", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('otherroot','other','root','p','open','',0,NULL); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('otherbox','otherroot','','agent','ready','','',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "101", Room: "other", Session: "otherroot"}, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v10/guilds/101/roles", "/api/v10/channels/201/messages", "/api/v10/users/@me", "/api/v10/webhooks/123", "/api/v10/guilds"} {
		if problem := s.discordScope(ctx, boxCaller(root), "GET", path); problem == nil {
			t.Errorf("scope allowed %s", path)
		}
	}
	for _, path := range []string{"/api/v10/guilds/100/roles", "/api/v10/channels/200/messages", "/api/v10/users/2"} {
		if problem := s.discordScope(ctx, boxCaller(root), "GET", path); problem != nil {
			t.Errorf("scope refused %s: %v", path, problem)
		}
	}
	if _, problem := s.DiscordSend(ctx, boxCaller(root), contract.DiscordSendRequest{Channel: "201", Text: "not allowed"}); problem == nil {
		t.Fatal("send crossed room boundary")
	}
	if _, problem := s.DiscordAPI(ctx, boxCaller(root), contract.DiscordAPIRequest{Method: "PATCH", Path: "/api/v10/guilds/101"}); problem == nil {
		t.Fatal("API crossed room boundary")
	}
	// Channel routing does not take authority away from the whole-server room.
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Channel: "200", Room: "other", Session: "otherroot"}, false); err != nil {
		t.Fatal(err)
	}
	s.discordDirectory.Apply("THREAD_CREATE", json.RawMessage(`{"id":"204","guild_id":"100","parent_id":"200","type":11}`))
	for _, caller := range []contract.SessionID{root, "otherroot"} {
		for _, channel := range []string{"200", "204"} {
			if problem := s.discordScope(ctx, boxCaller(caller), "POST", "/api/v10/channels/"+channel+"/messages"); problem != nil {
				t.Fatalf("server/channel authority refused for %s in %s: %v", caller, channel, problem)
			}
		}
	}
	var input discord.Message
	input.ID, input.Channel, input.Guild, input.Author.ID = "733", "200", "100", "2"
	input.Content = "channel row wins"
	if err := s.discordInput(ctx, client, input); err != nil {
		t.Fatal(err)
	}
	routed, err := s.store.Events(ctx, "other", 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(routed) != 1 {
		t.Fatalf("channel row lost incoming routing: %d %v", len(routed), err)
	}
	if problem := s.discordScope(ctx, boxCaller("otherroot"), "PATCH", "/api/v10/guilds/100"); problem == nil {
		t.Fatal("channel mapping gained server authority")
	}
	// Channel-only mappings cannot administer the server.
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Room: r}, true); err != nil {
		t.Fatal(err)
	}
	s.discordDirectory.Apply("CHANNEL_CREATE", json.RawMessage(`{"id":"202","guild_id":"100","name":"mapped"}`))
	s.discordDirectory.Apply("THREAD_CREATE", json.RawMessage(`{"id":"203","guild_id":"100","parent_id":"202","type":11}`))
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Channel: "202", Room: r, Session: root}, false); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"202", "203"} {
		if problem := s.discordScope(ctx, boxCaller(root), "GET", "/api/v10/channels/"+channel+"/messages"); problem != nil {
			t.Fatalf("channel/thread refused: %s %v", channel, problem)
		}
	}
	if problem := s.discordScope(ctx, boxCaller(root), "PATCH", "/api/v10/guilds/100"); problem == nil {
		t.Fatal("channel mapping allowed server administration")
	}
}
func TestDiscordOutboxDrainsToReply(t *testing.T) {
	s, r, root, ctx, fake, client := discordFixture(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO discord_outbox(room,session,channel,message,text) VALUES(?,?,?,?,?)`, r, root, "200", "300", "answer")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.discordDrain(child, client) }()
	defer func() { cancel(); <-done }()
	select {
	case body := <-fake.Messages:
		var msg struct {
			Content   string `json:"content"`
			Reference struct {
				ID string `json:"message_id"`
			} `json:"message_reference"`
		}
		if json.Unmarshal(body, &msg) != nil || msg.Content != "answer" || msg.Reference.ID != "300" {
			t.Fatalf("wrong reply %s", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("outbox not drained")
	}
}

func TestDiscordHistoryAndDM(t *testing.T) {
	s, r, _, ctx, fake, client := discordFixture(t)
	fake.SetHistory([]any{
		map[string]any{"id": "29", "content": "newer", "author": map[string]string{"id": "2", "username": "ari"}},
		map[string]any{"id": "28", "content": "owner line", "author": map[string]string{"id": "1", "username": "ownername"}},
		map[string]any{"id": "27", "content": "bot boundary", "author": map[string]string{"id": "900", "username": "Kira"}},
		map[string]any{"id": "26", "content": "too old", "author": map[string]string{"id": "2", "username": "ari"}},
	})
	var m discord.Message
	if err := json.Unmarshal([]byte(`{"id":"300","channel_id":"200","guild_id":"100","content":"question","author":{"id":"2","username":"ari"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	b := events[0].Body.(contract.DiscordMessage)
	if b.History != "ownername: owner line\nari: newer" {
		t.Fatalf("history %q", b.History)
	}
	rendered, err := room.RenderDelivery(b)
	if err != nil || strings.Index(rendered, "owner line") > strings.Index(rendered, "question") || strings.Count(rendered, "<outside ") != 1 {
		t.Fatalf("history outside %s %v", rendered, err)
	}
	m.ID = "301"
	m.Guild = ""
	m.Channel = "202"
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	if fake.HistoryCalls() != 1 {
		t.Fatalf("DM fetched history: %d", fake.HistoryCalls())
	}
	events, err = s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 2 || events[1].Body.(contract.DiscordMessage).History != "" {
		t.Fatal("missing DM or DM has history", err)
	}
}

func TestDiscordAnsweringPathAndInstructions(t *testing.T) {
	s, r, root, ctx, _, _ := discordFixture(t)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('front',?,'front','p','open','',0,?); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('frontbox','front','','agent','ready','','',0,0)`, r, root)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.discordClient(ctx, boxCaller("front")); problem == nil {
		t.Fatal("descendant could use root's Discord mapping")
	}
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Room: r, Session: "front", Instructions: "Only the front sees this."}, false); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		session contract.SessionID
		want    bool
	}{{root, false}, {"front", true}} {
		snapshot, problem := s.ContextSnapshot(ctx, boxCaller(test.session), contract.ContextSnapshotRequest{Section: contract.ContextDiscord})
		if problem != nil {
			t.Fatal(problem)
		}
		text := ""
		for _, section := range snapshot.Sections {
			text += section.Text
		}
		if strings.Contains(text, "Only the front sees this.") != test.want {
			t.Fatalf("instructions visibility for %s: %s", test.session, text)
		}
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sessions SET closed_at=1,state='closed' WHERE id='front'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	routes, err := s.store.DiscordRoutes(ctx, r)
	if err != nil || len(routes) != 1 || routes[0].Session != root {
		t.Fatalf("no root fallback %+v %v", routes, err)
	}
}

func TestDiscordGatewayToFinalReply(t *testing.T) {
	s, r, root, ctx, fake, _ := discordFixture(t)
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Channel: "200", Room: r, Session: root}, false); err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.runDiscord(run, fake.GatewayURL()) }()
	defer func() { cancel(); <-done }()
	fake.Events <- map[string]any{"id": "300", "channel_id": "200", "guild_id": "100", "content": "question", "author": map[string]string{"id": "2", "username": "ari"}, "mentions": []map[string]string{{"id": "900"}}}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 1 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("mention did not become input")
		case <-tick.C:
		}
	}
	if fake.Connections() != 1 {
		t.Fatalf("got %d gateways for two routes", fake.Connections())
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET state='ready',want='run' WHERE session=?", root)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start, err := s.event(r, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.store.Dispatch(ctx, root, "discord-turn", start, false); err != nil {
		t.Fatal(err)
	}
	output := contract.TurnOutputRequest{Turn: "discord-turn", Outcome: contract.TurnCompleted, Final: "final answer"}
	end, err := s.event(r, boxCaller(root), contract.TurnEnded{Session: root, Turn: "discord-turn", Outcome: contract.TurnCompleted, Final: output.Final})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.EndTurn(ctx, root, output, end); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-fake.Messages:
		if !strings.Contains(string(body), "final answer") || !strings.Contains(string(body), `"message_id":"300"`) || !strings.Contains(string(body), `"parse":["users"]`) {
			t.Fatalf("wrong reply %s", body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("final reply did not reach Discord")
	}
}

func TestDiscordDMRequiresExactlyOneWholeServerRoute(t *testing.T) {
	s, r, root, ctx, fake, client := discordFixture(t)
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Room: r}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Channel: "200", Room: r, Session: root}, false); err != nil {
		t.Fatal(err)
	}
	var m discord.Message
	if err := json.Unmarshal([]byte(`{"id":"400","channel_id":"500","content":"DM","author":{"id":"2","username":"ari"}}`), &m); err != nil {
		t.Fatal(err)
	}
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-fake.Messages:
		if !strings.Contains(string(body), "mention me") {
			t.Fatalf("wrong DM response %s", body)
		}
	case <-time.After(time.Second):
		t.Fatal("ambiguous DM not answered")
	}
	for _, guild := range []string{"100", "101"} {
		if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: guild, Room: r, Session: root}, false); err != nil {
			t.Fatal(err)
		}
	}
	m.ID = "401"
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.Messages:
	case <-time.After(time.Second):
		t.Fatal("multi-server DM not answered")
	}
	events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 0 {
		t.Fatalf("ambiguous DM delivered %d %v", len(events), err)
	}
}

func TestDiscordIncomingRichContent(t *testing.T) {
	s, r, _, ctx, _, client := discordFixture(t)
	var m discord.Message
	if err := json.Unmarshal([]byte(`{"id":"700","channel_id":"200","guild_id":"100","content":"question","author":{"id":"2","username":"ari"},"attachments":[{"filename":"message.txt","size":1234,"url":"https://example.invalid/message.txt"}],"embeds":[{"title":"Report","description":"Build failed"}],"sticker_items":[{"name":"wave"}],"poll":{"question":{"text":"Ready?"}}}`), &m); err != nil {
		t.Fatal(err)
	}
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	text, err := room.RenderDelivery(events[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Report", "Build failed", "[sticker] wave", "[poll] Ready?", "message.txt", "1234 bytes", "https://example.invalid/message.txt"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}
func TestDiscordRefusesLargeFiles(t *testing.T) {
	s, r, root, ctx, _, _ := discordFixture(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	hash, size, err := s.saveFile(ctx, r, strings.NewReader(strings.Repeat("x", (10<<20)+1)), 25<<20)
	if err != nil {
		t.Fatal(err)
	}
	_, problem := s.DiscordSend(ctx, boxCaller(root), contract.DiscordSendRequest{Channel: "200", Text: "file", Attachments: []contract.Attachment{{Name: "too-large.bin", SHA256: hash, Bytes: size}}})
	if problem == nil || !strings.Contains(problem.Message, "10 MB each without boosts") {
		t.Fatalf("large file not clearly refused: %v", problem)
	}
}
