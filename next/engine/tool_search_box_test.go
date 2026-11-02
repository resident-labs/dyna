//go:build linux

package engine

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// oneModel gives every profile one model.
type oneModel struct {
	fakeProfiles
	choice contract.ModelChoice
}

func (m oneModel) Model(contract.ProfileName) (contract.ModelChoice, []string, error) {
	return m.choice, []string{m.choice.ID}, nil
}

// promptSize reports what one request sends before any conversation, largest tools first.
func promptSize(t *testing.T, body string) (total, tools, system int, sizes []string) {
	t.Helper()
	var request struct {
		Tools        []json.RawMessage `json:"tools"`
		System       json.RawMessage   `json:"system"`
		Instructions json.RawMessage   `json:"instructions"`
		Input        []json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	for _, item := range request.Input {
		var declared struct {
			Type  string            `json:"type"`
			Tools []json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(item, &declared)
		if declared.Type == "additional_tools" {
			request.Tools = append(request.Tools, declared.Tools...)
		}
	}
	type sized struct {
		name string
		size int
	}
	var rows []sized
	for _, tool := range request.Tools {
		var named struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(tool, &named)
		tools += len(tool)
		rows = append(rows, sized{named.Name, len(tool)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].size > rows[j].size })
	for _, row := range rows {
		sizes = append(sizes, row.name+"="+strings.TrimSpace(jsonNumber(row.size)))
	}
	system = len(request.System) + len(request.Instructions)
	if len(request.Input) > 0 {
		system += len(request.Input[0]) // the Responses API sends the prompt as the first input
	}
	return len(body), tools, system, sizes
}

func jsonNumber(n int) string { data, _ := json.Marshal(n); return string(data) }

// Tools a session reaches through tool_search, each with an action its fake room can answer.
var deferredTools = map[string]map[string]any{
	"goal":     {"action": "list"},
	"session":  {"action": "list"},
	"pr":       {"action": "list"},
	"question": {"action": "list"},
	"workflow": {"action": "list"},
	"room":     {"action": "repos"},
}

// With the dyna server deferred, a real Pi declares only what every turn needs, and every other tool is still reached: through
// tool_search and a direct call, and from a script with no search. post stays direct, so a turn can end with it.
func TestToolSearchInARealBox(t *testing.T) {
	for _, model := range []contract.ModelChoice{
		{Provider: contract.ModelProviderAnthropic, ID: "claude-opus-5-5", Thinking: contract.ThinkingLow},
		{Provider: contract.ModelProviderAnthropic, ID: "claude-sonnet-5", Thinking: contract.ThinkingLow},
		{Provider: contract.ModelProviderOpenAI, ID: "gpt-6.1-sol", Thinking: contract.ThinkingLow},
		{Provider: contract.ModelProviderOpenAI, ID: "gpt-6.1-terra", Thinking: contract.ThinkingLow},
	} {
		t.Run(model.ID, func(t *testing.T) { toolSearchIn(t, model) })
	}
}

func toolSearchIn(t *testing.T, model contract.ModelChoice) {
	provider := &toolUpstream{tools: map[string][]crewTool{}}
	b := newToolBox(t, oneModel{choice: model}, provider)
	for name, input := range deferredTools {
		input := maps.Clone(input)
		input["room"] = b.room
		provider.tools["task-search"+name] = []crewTool{{"tool_search", jsonOf(t, map[string]string{"query": name})}, {"mcp__dyna__" + name, jsonOf(t, input)}}
	}
	provider.tools["task-script"] = []crewTool{{"codemode", jsonOf(t, map[string]string{"code": `const sessions = await tools.mcp__dyna__session({ action: "list", room: "` + string(b.room) + `" });
const described = await describeTool("mcp__dyna__workflow");
const found = await searchTools("pull request");
return JSON.stringify({ refused: sessions.isError === true, text: sessions.content[0].text.slice(0, 100), schema: JSON.stringify(described ?? "").length, found: found.map((tool) => tool.name) });`})}}
	post := func(text string, end bool) string {
		return jsonOf(t, map[string]any{"action": "send", "room": b.room, "text": text, "endTurn": end})
	}
	provider.tools["task-post"] = []crewTool{{"mcp__dyna__post", post("the last word", true)}}
	provider.tools["task-scriptpost"] = []crewTool{{"codemode", jsonOf(t, map[string]string{"code": `return JSON.stringify(await tools.mcp__dyna__post(` + post("from a script", true) + `));`})}}
	b.start(t)

	b.task(t, "task-first")
	first := provider.firstBody()
	total, tools, system, sizes := promptSize(t, first)
	t.Logf("prompt %s: request %d bytes, tools %d bytes in %d, system %d bytes: %s", model.ID, total, tools, len(sizes), system, strings.Join(sizes, " "))
	declared := map[string]bool{}
	for _, name := range provider.declared() {
		declared[name] = true
	}
	for _, name := range []string{"bash", "job", "read", "edit", "write", "codemode", "tool_search", "mcp__dyna__post"} {
		if !declared[name] {
			t.Errorf("%s is not declared: %v", name, provider.declared())
		}
	}
	for name := range deferredTools {
		if declared["mcp__dyna__"+name] {
			t.Errorf("%s stays in the prefix", name)
		}
	}
	if !strings.Contains(first, "mcp_servers") || !strings.Contains(first, "Dyna tools cover") {
		t.Errorf("the prompt does not say where the dyna tools are")
	}

	// A script reaches a tool nobody loaded.
	result := provider.lastOf(b.task(t, "task-script"))
	if !strings.Contains(result.Text, `\"refused\":false`) && !strings.Contains(result.Text, `"refused":false`) || !strings.Contains(result.Text, "mcp__dyna__pr") {
		t.Fatalf("a script: %+v", result)
	}

	listed := regexp.MustCompile(`- (mcp__\w+):`)
	loaded := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(deferredTools)) {
		results := b.task(t, "task-search"+name)
		search, called := results[len(results)-2], results[len(results)-1]
		found := listed.FindAllStringSubmatch(search.Text, -1)
		for _, row := range found {
			loaded[row[1]] = true
		}
		// A tool an earlier search loaded is no longer searched for: it is already there.
		if len(found) == 0 || len(found) > 3 || !loaded["mcp__dyna__"+name] {
			t.Fatalf("searching for %s: %+v", name, search)
		}
		t.Logf("tool_search %s: loaded %d: %v", name, len(found), found)
		if strings.Contains(called.Text, "unknown_action") || strings.Contains(called.Text, "not found: mcp__") || strings.Contains(called.Text, "Tool mcp__") {
			t.Fatalf("calling %s after its search: %+v", name, called)
		}
	}
	last := provider.lastBody()
	_, loadedTools, _, _ := promptSize(t, last)
	t.Logf("after every search: request %d bytes, tools %d bytes, tool_addition %d, additional_tools %d, tool_search_output %d, declared %v", len(last), loadedTools, strings.Count(last, `"tool_addition"`), strings.Count(last, `"additional_tools"`), strings.Count(last, `"tool_search_output"`), provider.declared())

	// A direct post with endTurn ends the turn with no further request.
	before := provider.count()
	b.task(t, "task-post")
	if got := provider.count() - before; got != 1 {
		t.Fatalf("a post with endTurn took %d requests", got)
	}
	before = provider.count()
	result = provider.lastOf(b.task(t, "task-scriptpost"))
	if got := provider.count() - before; got != 2 || !strings.Contains(result.Text, "turnContinues") {
		t.Fatalf("a script's post took %d requests: %+v", got, result)
	}
}
