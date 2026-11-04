/**
 * System prompt construction and project context loading
 */

import { getSystemMessageText } from "@earendil-works/pi-ai";
import { getDocsPath, getExamplesPath, getReadmePath } from "../config.ts";
import { formatSkillsForPrompt, type Skill } from "./skills.ts";

export interface BuildSystemPromptOptions {
	/** Custom system prompt (replaces the default prefix). */
	customPrompt?: string;
	/** Exact full prompt replacement set by a before_agent_start handler. */
	forceSystemPrompt?: string;
	/** Tools to include in prompt. Default: [read, bash, edit, write]. */
	selectedTools?: string[];
	/**
	 * Selected tools whose declarations requests leave out (`prepareLoadout` hidden declarations).
	 * They are reachable only through another tool, so the tool list and rules leave them out too.
	 */
	hiddenTools?: string[];
	/** Optional one-line tool snippets keyed by tool name. */
	toolSnippets?: Record<string, string>;
	/** Guideline bullets contributed by each tool, keyed by tool name. */
	toolGuidelines?: Record<string, string[]>;
	/** Additional guideline bullets appended to the default system prompt rules. */
	promptGuidelines?: string[];
	/** Text appended from user configuration before project context, skills, and cwd. */
	appendSystemPrompt?: string;
	/** Additional XML-wrapped prompt sections keyed by tag name. */
	sections?: Record<string, string>;
	/** Leave out the `docs` section, which points at Pi's own documentation and examples. */
	noDocs?: boolean;
	/** Working directory. */
	cwd: string;
	/** Pre-loaded context files. */
	contextFiles?: Array<{ path: string; content: string }>;
	/** Pre-loaded skills. */
	skills?: Skill[];
}

export type NormalizedBuildSystemPromptOptions = BuildSystemPromptOptions & {
	selectedTools: string[];
	hiddenTools: string[];
	toolSnippets: Record<string, string>;
	toolGuidelines: Record<string, string[]>;
	promptGuidelines: string[];
	appendSystemPrompt: string;
	sections: Record<string, string>;
	noDocs: boolean;
	contextFiles: Array<{ path: string; content: string }>;
	skills: Skill[];
};

/** Name of the section that names the tools `tool_search` or a codemode script can reach but are not declared. */
export const DEFERRED_TOOLS_SECTION = "deferred_tools";
/** Tools listed per namespace, and actions per tool; the rest are counted. */
export const MAX_DEFERRED_NAMES = 80;

/** A tool for the `deferred_tools` section. `actions` is the `action` enum of its schema, empty without one. */
export interface DeferredTool {
	name: string;
	namespace?: string;
	actions: readonly string[];
}

/**
 * The `deferred_tools` section: the tools not declared yet, by name only, grouped by namespace. A
 * namespace's line names its tools that take no action; each tool with actions gets its own line
 * below it. Undefined when nothing is listed or neither `tool_search` nor `codemode` is active.
 */
export function renderDeferredToolsSection(
	tools: readonly DeferredTool[],
	reach: { toolSearch: boolean; codemode: boolean },
): string | undefined {
	if (tools.length === 0 || (!reach.toolSearch && !reach.codemode)) return undefined;
	const script = "a codemode script calls one as tools.<prefix><name>.";
	const how = !reach.codemode
		? "tool_search loads one."
		: reach.toolSearch
			? `tool_search loads one, and ${script}`
			: `A${script.slice(1)}`;
	// The first MAX_DEFERRED_NAMES names, and a count of the rest.
	const capped = (names: readonly string[]) =>
		names.length > MAX_DEFERRED_NAMES
			? [...names.slice(0, MAX_DEFERRED_NAMES), `+${names.length - MAX_DEFERRED_NAMES} more; tool_search finds them`]
			: [...names];
	const groups = new Map<string, DeferredTool[]>();
	for (const tool of tools) {
		const prefix = tool.namespace && tool.name.startsWith(`${tool.namespace}__`) ? `${tool.namespace}__` : "";
		groups.set(prefix, [...(groups.get(prefix) ?? []), tool]);
	}
	const lines = [`Tools not declared to you yet, by name prefix, with a tool's actions after it. ${how}`];
	for (const prefix of [...groups.keys()].sort()) {
		const sorted = (groups.get(prefix) ?? [])
			.map((tool) => ({ name: tool.name.slice(prefix.length), actions: tool.actions }))
			.sort((a, b) => a.name.localeCompare(b.name));
		// Past the cap the namespace line counts the rest, whether or not they take actions.
		const listed = capped(sorted.map((tool) => tool.name));
		const kept = sorted.slice(0, MAX_DEFERRED_NAMES);
		const plain = [
			...kept.filter((tool) => tool.actions.length === 0).map((tool) => tool.name),
			...listed.slice(MAX_DEFERRED_NAMES),
		].join(", ");
		if (prefix) lines.push(plain ? `${prefix}: ${plain}` : `${prefix}:`);
		else if (plain) lines.push(plain);
		for (const tool of kept) {
			if (tool.actions.length > 0) lines.push(`- ${tool.name}: ${capped(tool.actions).join(", ")}`);
		}
	}
	return lines.join("\n");
}

/**
 * Ordered system prompt sections, keyed by name. `preamble` is untagged text; every other
 * section is wrapped in a tag of the same name so the model can match later updates to it.
 * These become `SystemMessage.sections` in the transcript.
 */
export type SystemPromptSections = Record<string, string>;

export const RESERVED_SYSTEM_PROMPT_SECTIONS: ReadonlySet<string> = new Set([
	"preamble",
	"tools",
	"rules",
	"docs",
	"addendum",
	"skills",
	"cwd",
	DEFERRED_TOOLS_SECTION,
]);
const SYSTEM_PROMPT_SECTION_NAME = /^[a-z][a-z0-9_-]*$/;
/** Normalize prompt input into the mutable, collection-complete shape exposed to extensions. */
export function normalizeBuildSystemPromptOptions(input: BuildSystemPromptOptions): NormalizedBuildSystemPromptOptions {
	return {
		customPrompt: input.customPrompt,
		forceSystemPrompt: input.forceSystemPrompt,
		selectedTools: [...(input.selectedTools ?? ["read", "bash", "edit", "write"])],
		hiddenTools: [...(input.hiddenTools ?? [])],
		toolSnippets: { ...(input.toolSnippets ?? {}) },
		toolGuidelines: Object.fromEntries(
			Object.entries(input.toolGuidelines ?? {}).map(([name, guidelines]) => [name, [...guidelines]]),
		),
		promptGuidelines: [...(input.promptGuidelines ?? [])],
		appendSystemPrompt: input.appendSystemPrompt ?? "",
		sections: { ...(input.sections ?? {}) },
		noDocs: input.noDocs ?? false,
		cwd: input.cwd,
		contextFiles: (input.contextFiles ?? []).map((file) => ({ ...file })),
		skills: (input.skills ?? []).map((skill) => ({ ...skill })),
	};
}

function renderProjectContext(contextFiles: Array<{ path: string; content: string }>): string {
	return [
		"Project-specific instructions and guidelines:",
		...contextFiles.map(
			({ path, content }) => `<project_instructions path="${path}">\n${content}\n</project_instructions>`,
		),
	].join("\n\n");
}

function buildRules(
	selectedTools: string[],
	toolGuidelines: Record<string, string[]>,
	promptGuidelines: string[],
): string {
	const rules: string[] = [];
	const seen = new Set<string>();
	const addRule = (rule: string): void => {
		const normalized = rule.trim();
		if (!normalized || seen.has(normalized)) return;
		seen.add(normalized);
		rules.push(normalized);
	};

	const hasBash = selectedTools.includes("bash");
	const hasPowerShell = selectedTools.includes("powershell");
	const hasGrep = selectedTools.includes("grep");
	const hasFind = selectedTools.includes("find");
	const hasLs = selectedTools.includes("ls");

	if ((hasBash || hasPowerShell) && !hasGrep && !hasFind && !hasLs) {
		if (hasBash && hasPowerShell) {
			addRule("Use bash or PowerShell for file operations like listing, searching, and finding files");
		} else if (hasPowerShell) {
			addRule("Use PowerShell for file operations like listing, searching, and finding files");
		} else {
			addRule("Use bash for file operations like ls, rg, find");
		}
	}

	for (const name of selectedTools) {
		for (const rule of toolGuidelines[name] ?? []) addRule(rule);
	}
	for (const rule of promptGuidelines) addRule(rule);
	addRule("Be concise in your responses");
	addRule("Show file paths clearly when working with files");
	return rules.map((rule) => `- ${rule}`).join("\n");
}

/** Build the ordered, independently replaceable sections of the structured system prompt. */
export function buildSystemPromptSections(input: BuildSystemPromptOptions): SystemPromptSections {
	const options = normalizeBuildSystemPromptOptions(input);
	if (options.forceSystemPrompt !== undefined) return { preamble: options.forceSystemPrompt };
	const {
		customPrompt,
		selectedTools,
		hiddenTools,
		toolSnippets,
		toolGuidelines,
		promptGuidelines,
		appendSystemPrompt,
		sections: customSections,
		noDocs,
		cwd,
		contextFiles,
		skills,
	} = options;

	for (const name of Object.keys(customSections)) {
		if (!name || name === "preamble") {
			throw new Error(`Invalid system prompt section name: ${name}`);
		}
	}

	const declaredTools = selectedTools.filter((name) => !hiddenTools.includes(name));
	const promptSections: Record<string, string> = Object.create(null);
	if (customPrompt) {
		promptSections.preamble = customPrompt;
	} else {
		promptSections.preamble =
			"You are an expert coding assistant operating inside pi, a coding agent harness. You help users by reading files, executing commands, editing code, and writing new files.";
		const visibleTools = declaredTools.filter((name) => !!toolSnippets[name]);
		const tools =
			visibleTools.length > 0 ? visibleTools.map((name) => `- ${name}: ${toolSnippets[name]}`).join("\n") : "(none)";
		promptSections.tools = `${tools}\n\nIn addition to the tools above, you may have access to other custom tools depending on the project.`;
		promptSections.rules = buildRules(declaredTools, toolGuidelines, promptGuidelines);
		if (!noDocs) {
			promptSections.docs = `Pi documentation (read only when the user asks about pi itself, its SDK, extensions, themes, skills, or TUI):
- Main documentation: ${getReadmePath()}
- Additional docs: ${getDocsPath()}
- Examples: ${getExamplesPath()} (extensions, custom tools, SDK)
- When reading pi docs or examples, resolve docs/... under Additional docs and examples/... under Examples, not the current working directory
- When asked about: extensions (docs/extensions.md, examples/extensions/), themes (docs/themes.md), skills (docs/skills.md), prompt templates (docs/prompt-templates.md), TUI components (docs/tui.md), keybindings (docs/keybindings.md), SDK integrations (docs/sdk.md), custom providers (docs/custom-provider.md), adding models (docs/models.md), pi packages (docs/packages.md), environment variables (docs/environment-variables.md), MCP servers (docs/mcp.md), codemode scripts and non-LLM models such as classifiers and image models (docs/codemode.md)
- When working on pi topics, read the docs and examples, and follow .md cross-references before implementing
- Always read pi .md files completely and follow links to related docs (e.g., tui.md for TUI API details)`;
		}
	}

	if (appendSystemPrompt) promptSections.addendum = appendSystemPrompt;
	for (const file of contextFiles) {
		promptSections[`file:${file.path}`] = renderProjectContext([file]);
	}
	// A hidden reader is still reachable through another tool, so skills stay but the hint names no tool.
	const readers = ["read", "bash"] as const;
	const skillFileReadTool =
		readers.find((tool) => declaredTools.includes(tool)) ??
		(readers.some((tool) => selectedTools.includes(tool)) ? "indirect" : undefined);
	if (skillFileReadTool && skills.length > 0) {
		const skillsPrompt = formatSkillsForPrompt(skills, skillFileReadTool).trim();
		if (skillsPrompt) promptSections.skills = skillsPrompt;
	}
	promptSections.cwd = cwd.replace(/\\/g, "/");
	for (const [name, content] of Object.entries(customSections)) {
		promptSections[name] = content;
	}

	const sections: SystemPromptSections = Object.create(null);
	sections.preamble = promptSections.preamble;
	for (const [name, content] of Object.entries(promptSections)) {
		if (name !== "preamble") {
			const tag = SYSTEM_PROMPT_SECTION_NAME.test(name) ? name : "context_section";
			sections[name] = `<${tag}>\n${content}\n</${tag}>`;
		}
	}
	return sections;
}

/**
 * The complete prompt state for `input`. A forced prompt is opaque and lives in `content`
 * with no sections; otherwise `content` is empty and the structured sections carry the prompt.
 */
export function buildSystemPromptState(input: BuildSystemPromptOptions): {
	content: string;
	sections?: SystemPromptSections;
} {
	if (input.forceSystemPrompt !== undefined) return { content: input.forceSystemPrompt };
	return { content: "", sections: buildSystemPromptSections(input) };
}

/** Build the complete system prompt text. */
export function buildSystemPrompt(input: BuildSystemPromptOptions): string {
	return getSystemMessageText({ role: "system", ...buildSystemPromptState(input), timestamp: 0 });
}
