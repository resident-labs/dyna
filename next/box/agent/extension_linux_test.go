package agent

import (
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
globalThis.fetch = async (_url, init) => {
	const { method, params } = JSON.parse(init.body);
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
	direct: await call("a", undefined, { text: "done", end_turn: true }),
	plain: await call("b", undefined, { text: "update" }),
	failed: await call("c", undefined, { text: "refused", end_turn: true }),
	nested: await call("d/1", "d", { text: "from a script", end_turn: true }),
	calls: posts.map((post) => post._dynaCallId),
}));
`

func TestExtensionEndsTheTurnOnADirectPost(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	extension, driver := filepath.Join(dir, "extension.mjs"), filepath.Join(dir, "driver.mjs")
	if err := os.WriteFile(extension, []byte(renderExtension()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driver, []byte(extensionDriver), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, driver, extension).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
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
		Calls                         []string
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
}
