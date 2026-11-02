package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The driver loads the extension with a fake Pi and a fake dyna server, then prints each call's result.
const extensionDriver = `
const posts = [];
const ids = new Set();
globalThis.fetch = async (_url, init) => {
	const { id, method, params } = JSON.parse(init.body);
	ids.add(id);
	if (method === "tools/list") return { json: async () => ({ result: { tools: [{ name: "post", description: "Post.", inputSchema: { type: "object" }, annotations: {} }] } }) };
	posts.push(params.arguments);
	const failed = params.arguments.text === "refused";
	const output = failed ? { code: "invalid" } : { event: "post_1", seq: 7 };
	return { json: async () => ({ result: { content: [{ type: "text", text: JSON.stringify(output) }], structuredContent: output, ...(failed ? { isError: true } : {}) } }) };
};
const handlers = {};
const registered = {};
const { default: load } = await import(process.argv[2]);
await load({ on: (name, handler) => { handlers[name] = handler; }, registerTool: (definition) => { registered[definition.name] = definition; } });
const tool = registered.mcp__dyna__post;
const call = async (id, parent, input) => {
	const event = { toolName: tool.name, toolCallId: id, parentToolCallId: parent, input };
	handlers.tool_call(event);
	return tool.execute(id, event.input);
};
console.log(JSON.stringify({
	tool: { name: tool.name, exposure: tool.exposure, description: tool.description },
	direct: await call("a", undefined, { text: "done", endTurn: true }),
	plain: await call("b", undefined, { text: "update" }),
	failed: await call("c", undefined, { text: "refused", endTurn: true }),
	nested: await call("d/1", "d", { text: "from a script", endTurn: true }),
	calls: posts.map((post) => post._dynaCallId),
	ids: [...ids],
}));
`

// The server fails tools/list, so the extension must keep its call IDs and leave Pi's plain tool in place.
const extensionFailureDriver = `
globalThis.fetch = async () => ({ json: async () => ({ error: { message: "down" } }) });
const handlers = {};
const registered = {};
const { default: load } = await import(process.argv[2]);
await load({ on: (name, handler) => { handlers[name] = handler; }, registerTool: (definition) => { registered[definition.name] = definition; } });
const event = { toolName: "mcp__dyna__post", toolCallId: "a", input: {} };
handlers.tool_call(event);
console.log(JSON.stringify({ registered: Object.keys(registered), call: event.input._dynaCallId }));
`

// The bash and job tools with a fake job server. Each call prints what the server was asked and what the tool returned.
const extensionBashDriver = `
const asked = [];
const answers = {
	done: { output: "ok\n", job: "job_1", running: false, status: "exit 0", exit_code: 0 },
	failed: { output: "no\n", job: "job_2", running: false, status: "exit 2", exit_code: 2 },
	stopped: { output: "", job: "job_3", running: false, status: "stopped" },
	running: { output: "", job: "job_4", running: true, status: "running" },
};
globalThis.fetch = async (url, init) => {
	if (url.endsWith("/dyna/")) return { json: async () => ({ error: { message: "down" } }) };
	const body = JSON.parse(init.body);
	asked.push({ url, body });
	if (body.command === "refused") return { ok: false, json: async () => ({ code: "no_grant", message: "no matching host-exec grant" }) };
	if (body.action === "list") return { ok: true, json: async () => ({ jobs: [{ job: "job_1", status: "running", command: "make" }, { job: "job_2", status: "exit 0", command: "uptime", host: "h" }] }) };
	return { ok: true, json: async () => answers[body.command ?? body.job] };
};
const handlers = {};
const registered = {};
const { default: load } = await import(process.argv[2]);
await load({ on: (name, handler) => { handlers[name] = handler; }, registerTool: (definition) => { registered[definition.name] = definition; } });
const call = async (name, id, parent, input) => {
	handlers.tool_call({ toolName: name, toolCallId: id, parentToolCallId: parent, input });
	try {
		return await registered[name].execute(id, input, undefined, undefined, { cwd: "/box/work/repo" });
	} catch (error) {
		return { thrown: error.message };
	}
};
console.log(JSON.stringify({
	done: await call("bash", "a", undefined, { command: "done", description: "title" }),
	failed: await call("bash", "b", undefined, { command: "failed", cwd: "src" }),
	stopped: await call("job", "c", undefined, { action: "stop", job: "stopped" }),
	running: await call("job", "d", undefined, { action: "output", job: "running" }),
	nested: await call("bash", "e/1", "e", { command: "done", cwd: "/tmp" }),
	host: await call("bash", "f", undefined, { command: "done", host: "h", cwd: "logs", timeout: 60 }),
	refused: await call("bash", "g", undefined, { command: "refused", host: "h" }),
	list: await call("job", "h", undefined, { action: "list" }),
	searches: [{ query: "goal" }, { query: "goal", limit: 8 }].map((input) => {
		handlers.tool_call({ toolName: "tool_search", toolCallId: "s", input });
		return input.limit;
	}),
	moved: await call("bash", "i", undefined, { command: "running" }),
	asked,
	exposure: [registered.bash.exposure, registered.job.exposure],
	names: Object.keys(registered),
}));
`

func runExtension(t *testing.T, driverSource string) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	extension, driver := filepath.Join(dir, "extension.mjs"), filepath.Join(dir, "driver.mjs")
	if err := os.WriteFile(extension, []byte(RenderExtension()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driver, []byte(driverSource), 0600); err != nil {
		t.Fatal(err)
	}
	var out, problems bytes.Buffer
	command := exec.Command(node, driver, extension)
	command.Stdout, command.Stderr = &out, &problems
	if err := command.Run(); err != nil {
		t.Fatalf("%v: %s", err, problems.String())
	}
	return out.Bytes()
}

func TestExtensionKeepsCallIDsWhenThePostToolCannotBeRead(t *testing.T) {
	out := runExtension(t, extensionFailureDriver)
	var got struct {
		Registered []string
		Call       string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !slices.Equal(got.Registered, []string{"bash", "job"}) || got.Call != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestExtensionBashAndJobTools(t *testing.T) {
	out := runExtension(t, extensionBashDriver)
	type result struct {
		Content           []struct{ Text string }
		IsError           bool
		Thrown            string
		StructuredContent struct {
			Output   string
			Job      string
			ExitCode *int `json:"exit_code"`
			Jobs     []struct{ Job, Host string }
		}
	}
	var got struct {
		Done, Failed, Stopped, Running, Nested, Host, Refused, List, Moved result
		Asked                                                              []struct {
			URL  string
			Body map[string]any
		}
		Exposure []*string
		Searches []int
		Names    []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !slices.Equal(got.Searches, []int{3, 8}) {
		t.Fatalf("a search's limit: %v", got.Searches)
	}
	if !slices.Equal(got.Names, []string{"bash", "job"}) {
		t.Fatalf("tools %v", got.Names)
	}
	for _, exposure := range got.Exposure {
		if exposure != nil {
			t.Fatalf("bash and job are direct, as a tool is by default: %v", *exposure)
		}
	}
	if got.Done.Content[0].Text != "ok\n[job job_1: exit 0]" || got.Done.IsError || got.Done.StructuredContent.ExitCode == nil || got.Done.StructuredContent.Job != "job_1" {
		t.Fatalf("done %+v", got.Done)
	}
	if got.Failed.IsError || got.Failed.Content[0].Text != "no\n[job job_2: exit 2]" || *got.Failed.StructuredContent.ExitCode != 2 {
		t.Fatalf("failed %+v", got.Failed)
	}
	if got.Stopped.IsError || got.Stopped.Content[0].Text != "[job job_3: stopped]" {
		t.Fatalf("stopped %+v", got.Stopped)
	}
	if got.Running.IsError || got.Running.Content[0].Text != "[job job_4 is running in the background]" {
		t.Fatalf("running %+v", got.Running)
	}
	if want := "[job job_4] The job is in the background. You can end your turn and Dyna will wake you when it completes."; got.Moved.IsError || got.Moved.Content[0].Text != want {
		t.Fatalf("moved %+v", got.Moved)
	}
	if got.Refused.Thrown != "no_grant: no matching host-exec grant" {
		t.Fatalf("refused %+v", got.Refused)
	}
	if text := got.List.Content[0].Text; text != "job_1\trunning\tmake\njob_2\texit 0\ton h: uptime" || got.List.StructuredContent.Jobs[1].Host != "h" {
		t.Fatalf("list %+v", got.List)
	}
	body := func(i int, key string) any { return got.Asked[i].Body[key] }
	if !strings.HasSuffix(got.Asked[0].URL, "/shell/run") || !strings.HasSuffix(got.Asked[2].URL, "/shell/job") {
		t.Fatalf("asked %+v", got.Asked)
	}
	if body(0, "cwd") != "/box/work/repo" || body(0, "callId") != "a" || body(0, "description") != "title" {
		t.Fatalf("first call %+v", got.Asked[0])
	}
	if body(1, "cwd") != "/box/work/repo/src" {
		t.Fatalf("a relative cwd starts from Pi's: %+v", got.Asked[1])
	}
	if body(4, "cwd") != "/tmp" || body(4, "callId") != "e" {
		t.Fatalf("a nested call links to its outer call: %+v", got.Asked[4])
	}
	if body(5, "cwd") != "logs" || body(5, "host") != "h" || body(5, "timeout") != 60.0 {
		t.Fatalf("a host's cwd is its own: %+v", got.Asked[5])
	}
}

func TestExtensionEndsTheTurnOnADirectPost(t *testing.T) {
	out := runExtension(t, extensionDriver)
	type result struct {
		Terminate bool `json:"terminate"`
		IsError   bool `json:"isError"`
		Details   struct {
			EndPost string `json:"endPost"`
		} `json:"details"`
		StructuredContent struct {
			StructuredContent struct {
				Event         string `json:"event"`
				TurnContinues bool   `json:"turnContinues"`
			} `json:"structuredContent"`
		} `json:"structuredContent"`
	}
	var got struct {
		Tool struct {
			Name, Exposure, Description string
		}
		Direct, Plain, Failed, Nested result
		Calls, IDs                    []string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.Tool.Name != "mcp__dyna__post" || got.Tool.Exposure != "direct" || got.Tool.Description != "Post." {
		t.Fatalf("tool %+v", got.Tool)
	}
	if !got.Direct.Terminate || got.Direct.Details.EndPost != "post_1" || got.Direct.StructuredContent.StructuredContent.TurnContinues {
		t.Fatalf("direct %+v", got.Direct)
	}
	if got.Plain.Terminate || got.Plain.Details.EndPost != "" {
		t.Fatalf("plain %+v", got.Plain)
	}
	if got.Failed.Terminate || got.Failed.Details.EndPost != "" || !got.Failed.IsError {
		t.Fatalf("failed %+v", got.Failed)
	}
	if n := got.Nested; n.Terminate || n.Details.EndPost != "" || !n.StructuredContent.StructuredContent.TurnContinues || n.StructuredContent.StructuredContent.Event != "post_1" {
		t.Fatalf("nested %+v", n)
	}
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(got.Calls, want) {
		t.Fatalf("call IDs %v, want %v", got.Calls, want)
	}
	if len(got.IDs) != 5 {
		t.Fatalf("request IDs must differ: %v", got.IDs)
	}
}
