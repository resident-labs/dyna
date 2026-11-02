import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { AgentTool } from "@earendil-works/pi-agent-core";
import {
	fauxAssistantMessage,
	fauxToolCall,
	getCurrentSystemMessage,
	getSystemMessageText,
	type TranscriptContext,
} from "@earendil-works/pi-ai";
import { getModel } from "@earendil-works/pi-ai/compat";
import { Type } from "typebox";
import { describe, expect, test } from "vitest";
import { DefaultResourceLoader } from "../src/core/resource-loader.ts";
import { createAgentSession } from "../src/core/sdk.ts";
import { SessionManager } from "../src/core/session-manager.ts";
import { SettingsManager } from "../src/core/settings-manager.ts";
import { buildSystemPromptSections, buildSystemPromptState } from "../src/core/system-prompt.ts";
import type { ExtensionFactory } from "../src/index.ts";
import { createHarness } from "./suite/harness.ts";

describe("system prompt updates", () => {
	test("declares the prompt and tools once and reuses them across resume", async () => {
		const harness = await createHarness();
		try {
			harness.setResponses([fauxAssistantMessage("first"), fauxAssistantMessage("second")]);
			await harness.session.prompt("one");
			await harness.session.prompt("two");
			const systemEntries = harness.sessionManager
				.getEntries()
				.filter((entry) => entry.type === "message" && entry.message.role === "system");
			expect(systemEntries).toHaveLength(1);
			expect(harness.session.messages.map((message) => message.role)).toEqual([
				"system",
				"user",
				"assistant",
				"user",
				"assistant",
			]);
			const head = harness.session.messages[0];
			if (head?.role !== "system") throw new Error("expected system message");
			expect(head.content).toBe("");
			expect(Object.keys(head.sections ?? {})).toEqual(["preamble", "tools", "rules", "docs", "cwd"]);
			expect(head.toolsAdded?.map((tool) => tool.name)).toEqual(["read", "bash", "edit", "write"]);
			expect(getSystemMessageText(head)).toBe(harness.session.systemPrompt);
		} finally {
			harness.cleanup();
		}
	});

	test("opens a transcript without a system message and declares the prompt on the first request", async () => {
		const tempDir = mkdtempSync(join(tmpdir(), "pi-system-prompt-migration-"));
		try {
			const sessionManager = SessionManager.inMemory(tempDir);
			sessionManager.appendMessage({ role: "user", content: "existing", timestamp: 1 });
			const created = await createAgentSession({
				cwd: tempDir,
				agentDir: join(tempDir, "agent"),
				model: getModel("anthropic", "claude-sonnet-4-5")!,
				settingsManager: SettingsManager.inMemory(),
				sessionManager,
				noTools: "all",
			});
			try {
				// Nothing is synthesized or persisted until a request needs it.
				expect(created.session.messages.map((message) => message.role)).toEqual(["user"]);
				expect(sessionManager.buildSessionContext().messages.map((message) => message.role)).toEqual(["user"]);
				expect(getCurrentSystemMessage(created.session.messages)).toBeUndefined();
			} finally {
				created.session.dispose();
			}
		} finally {
			rmSync(tempDir, { recursive: true, force: true });
		}
	});

	test("keeps the preamble untagged and replaces it like any section", () => {
		const previous = buildSystemPromptSections({ customPrompt: "You are A.", cwd: "/tmp" });
		expect(previous.preamble).toBe("You are A.");

		expect(buildSystemPromptState({ forceSystemPrompt: "Exact prompt.", cwd: "/tmp" })).toEqual({
			content: "Exact prompt.",
		});
		expect(buildSystemPromptState({ cwd: "/tmp" })).toEqual({
			content: "",
			sections: buildSystemPromptSections({ cwd: "/tmp" }),
		});
		expect(() => buildSystemPromptSections({ cwd: "/tmp", sections: { preamble: "x" } })).toThrow(
			"Invalid system prompt section name",
		);
	});

	test("a forced prompt is delivered through context updates without changing the head", async () => {
		let turn = 0;
		const extension: ExtensionFactory = (pi) => {
			pi.on("before_agent_start", (event) => {
				if (++turn === 3) event.systemPromptOptions.sections.plan_mode = "Plan only.";
				return turn === 2 || turn === 3 ? { systemPrompt: "Exact prompt." } : undefined;
			});
		};
		const harness = await createHarness({ extensionFactories: [extension] });
		try {
			const requests: TranscriptContext[] = [];
			harness.setResponses(
				["one", "two", "three", "four"].map((text) => (providerContext: TranscriptContext) => {
					requests.push(providerContext);
					return fauxAssistantMessage(text);
				}),
			);
			for (const text of ["one", "two", "three", "four"]) await harness.session.prompt(text);
			const systemMessages = requests.map((request) =>
				request.messages.filter((message) => message.role === "system"),
			);
			expect(systemMessages.map((messages) => messages.length)).toEqual([1, 1, 1, 1]);
			for (const messages of systemMessages) expect(messages[0]).toEqual(systemMessages[0]?.[0]);
			const updates = harness.session.messages.filter(
				(message) => message.role === "custom" && message.customType === "context_update",
			);
			expect(updates).toHaveLength(2);
			expect(updates[0]?.role === "custom" && updates[0].content).toContain("+Exact prompt.");
			expect(updates[1]?.role === "custom" && updates[1].content).toContain("-Exact prompt.");
		} finally {
			harness.cleanup();
		}
	});

	test("setActiveTools emits prompt sections and tool changes before the next request", async () => {
		const extension: ExtensionFactory = (pi) => {
			for (const name of ["first", "second"]) {
				pi.registerTool({
					name,
					label: name,
					description: `${name} description`,
					promptSnippet: `${name} prompt snippet`,
					promptGuidelines: [`Use ${name} carefully.`],
					parameters: Type.Object({}),
					execute: async () => ({ content: [{ type: "text", text: name }], details: {} }),
				});
			}
		};
		const harness = await createHarness({ extensionFactories: [extension], initialActiveToolNames: ["first"] });
		try {
			// Faux response callbacks swallow thrown assertions, so capture and assert afterwards.
			const requests: TranscriptContext[] = [];
			harness.setResponses([
				(providerContext) => {
					requests.push(providerContext);
					return fauxAssistantMessage("first");
				},
				(providerContext) => {
					requests.push(providerContext);
					return fauxAssistantMessage([fauxToolCall("first", {})], { stopReason: "toolUse" });
				},
				(providerContext) => {
					requests.push(providerContext);
					return fauxAssistantMessage("second");
				},
			]);
			await harness.session.prompt("first");
			harness.session.setActiveToolsByName(["second"]);
			await harness.session.prompt("second");
			expect(requests).toHaveLength(3);

			expect(Object.keys(requests[0] ?? {})).toEqual(["messages"]);
			const initial = requests[0]?.messages[0];
			if (initial?.role !== "system") throw new Error("expected initial system message");
			expect(initial.toolsAdded?.map((value) => value.name)).toEqual(["first", "second"]);
			expect(initial.sections?.tools).toContain("first prompt snippet");

			const update = requests[1]?.messages.filter((message) => message.role === "system").at(-1);
			expect(update?.toolsRemoved).toEqual([{ name: "first" }]);
			expect(update?.sections).toBeUndefined();
			const contextUpdate = harness.session.messages.find(
				(message) => message.role === "custom" && message.customType === "context_update",
			);
			expect(contextUpdate?.role === "custom" && contextUpdate.content).toContain("second prompt snippet");

			const result = requests[2]?.messages.filter((message) => message.role === "toolResult").at(-1);
			expect(result).toMatchObject({ role: "toolResult", toolName: "first", isError: true });

			const current = getCurrentSystemMessage(harness.session.messages);
			expect(current?.toolsAdded?.map((value) => value.name)).toEqual(["second"]);
		} finally {
			harness.cleanup();
		}
	});

	test("setActiveTools in before_agent_start controls the same request", async () => {
		const extension: ExtensionFactory = (pi) => {
			for (const name of ["first", "second"]) {
				pi.registerTool({
					name,
					label: name,
					description: `${name} description`,
					parameters: Type.Object({}),
					execute: async () => ({ content: [{ type: "text", text: name }], details: {} }),
				});
			}
			let turn = 0;
			pi.on("before_agent_start", () => {
				if (turn++ === 1) pi.setActiveTools(["second"]);
			});
		};
		const harness = await createHarness({ extensionFactories: [extension], initialActiveToolNames: ["first"] });
		try {
			const requests: TranscriptContext[] = [];
			harness.setResponses([
				(providerContext) => {
					requests.push(providerContext);
					return fauxAssistantMessage("first");
				},
				(providerContext) => {
					requests.push(providerContext);
					return fauxAssistantMessage("second");
				},
			]);
			await harness.session.prompt("first");
			await harness.session.prompt("second");
			expect(requests).toHaveLength(2);
			const update = requests[1]?.messages.filter((message) => message.role === "system").at(-1);
			expect(update?.toolsRemoved).toEqual([{ name: "first" }]);
			expect(update?.toolsAdded).toBeUndefined();
			expect(harness.session.getActiveToolNames()).toEqual(["second"]);
		} finally {
			harness.cleanup();
		}
	});

	test("keeps tool declarations stable across a session JSON round-trip", async () => {
		const executableTool: AgentTool = {
			name: "plain",
			label: "Plain",
			description: "Plain tool",
			parameters: Type.Object({}),
			execute: async () => ({ content: [], details: {} }),
		};
		const harness = await createHarness({ tools: [executableTool], initialActiveToolNames: ["plain"] });
		try {
			harness.setResponses([fauxAssistantMessage("first"), fauxAssistantMessage("second")]);
			await harness.session.prompt("one");
			const head = harness.session.messages[0];
			if (head?.role !== "system") throw new Error("expected system message");
			const declaration = head.toolsAdded?.[0];
			if (!declaration) throw new Error("expected tool declaration");
			expect(Object.hasOwn(declaration, "constrainedSampling")).toBe(false);
			expect(Object.hasOwn(declaration, "execute")).toBe(false);

			// Simulate a resume: the persisted JSON must replay to the same declarations.
			harness.session.agent.state.messages = JSON.parse(JSON.stringify(harness.session.messages));
			await harness.session.prompt("two");
			expect(harness.session.messages.filter((message) => message.role === "system")).toHaveLength(1);
		} finally {
			harness.cleanup();
		}
	});
	test("an AGENTS.md or skill edit during a run reaches the next request as a context update", async () => {
		const root = mkdtempSync(join(tmpdir(), "pi-prompt-resources-"));
		const cwd = join(root, "work");
		const agentDir = join(root, "agent");
		mkdirSync(cwd, { recursive: true });
		mkdirSync(join(agentDir, "skills"), { recursive: true });
		const instructions = join(cwd, "AGENTS.md");
		writeFileSync(instructions, "Use tabs.");
		const resourceLoader = new DefaultResourceLoader({ cwd, agentDir, settingsManager: SettingsManager.inMemory() });
		await resourceLoader.reload();
		const tool = (name: string, run: () => void): AgentTool => ({
			name,
			label: name,
			description: `${name} description`,
			parameters: Type.Object({}),
			execute: async () => {
				run();
				return { content: [{ type: "text", text: name }], details: {} };
			},
		});
		const tools = [
			tool("read", () => {}),
			tool("edit_instructions", () => writeFileSync(instructions, "Use spaces.")),
			tool("add_skill", () => {
				mkdirSync(join(agentDir, "skills", "mid-run"));
				writeFileSync(
					join(agentDir, "skills", "mid-run", "SKILL.md"),
					"---\nname: mid-run\ndescription: A skill added mid-run\n---\nUse it.",
				);
			}),
		];
		const harness = await createHarness({
			resourceLoader,
			tools,
			initialActiveToolNames: tools.map((value) => value.name),
		});
		try {
			// Faux response callbacks swallow thrown assertions, and the context they see keeps growing.
			const requests: TranscriptContext[] = [];
			harness.setResponses(
				[
					fauxToolCall("edit_instructions", {}),
					fauxToolCall("add_skill", {}),
					fauxToolCall("read", {}),
					undefined,
				].map((call) => (providerContext: TranscriptContext) => {
					requests.push(JSON.parse(JSON.stringify(providerContext)));
					return call ? fauxAssistantMessage([call], { stopReason: "toolUse" }) : fauxAssistantMessage("done");
				}),
			);
			await harness.session.prompt("work");
			expect(requests).toHaveLength(4);

			const updates = requests.map((request) =>
				request.messages.flatMap((message) =>
					message.role === "user" && typeof message.content !== "string"
						? message.content.flatMap((part) =>
								part.type === "text" && part.text.startsWith("[Context update]") ? [part.text] : [],
							)
						: [],
				),
			);
			expect(updates.map((texts) => texts.length)).toEqual([0, 1, 2, 2]);
			expect(updates[1]?.[0]).toContain(`Changed section file:${instructions}`);
			expect(updates[1]?.[0]).toContain("-Use tabs.");
			expect(updates[1]?.[0]).toContain("+Use spaces.");
			expect(updates[2]?.[1]).toContain("A skill added mid-run");
			expect(updates[2]?.[1]).not.toContain("Use spaces.");

			// The head stays byte for byte the same, so the cached prefix holds.
			const heads = requests.map((request) => request.messages.filter((message) => message.role === "system"));
			for (const head of heads) expect(JSON.stringify(head)).toBe(JSON.stringify(heads[0]));
			expect(heads[0]).toHaveLength(1);
			const head = heads[0]?.[0];
			if (head?.role !== "system") throw new Error("expected system message");
			expect(getSystemMessageText(head)).toContain("Use tabs.");
			expect(getSystemMessageText(head)).not.toContain("A skill added mid-run");
		} finally {
			harness.cleanup();
			rmSync(root, { recursive: true, force: true });
		}
	});
});
