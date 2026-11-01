package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
let tool;
const { default: load } = await import(process.argv[2]);
await load({ on: (name, handler) => { handlers[name] = handler; }, registerTool: (definition) => { tool = definition; } });
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
let tool;
const { default: load } = await import(process.argv[2]);
await load({ on: (name, handler) => { handlers[name] = handler; }, registerTool: (definition) => { tool = definition; } });
const event = { toolName: "mcp__dyna__post", toolCallId: "a", input: {} };
handlers.tool_call(event);
console.log(JSON.stringify({ registered: tool !== undefined, call: event.input._dynaCallId }));
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
		Registered bool
		Call       string
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.Registered || got.Call != "a" {
		t.Fatalf("got %+v", got)
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
