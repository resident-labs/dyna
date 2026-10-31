import { diffLines } from "diff";
import type { CustomMessage } from "./messages.ts";
import type { SessionEntry } from "./session-manager.ts";

/** Ordered sections last delivered in a head or context update. */
export type ContextSections = Record<string, string>;

/** Derive the last delivered state from the active session branch. */
export function deliveredSections(branch: SessionEntry[]): ContextSections | undefined {
	for (let i = branch.length - 1; i >= 0; i--) {
		const entry = branch[i];
		if (entry.type === "custom_message" && entry.customType === "context_update") {
			if (isContextDetails(entry.details)) return entry.details.sections;
		}
		const head =
			entry.type === "compaction"
				? entry.systemMessage
				: entry.type === "message" && entry.message.role === "system" && entry.message.sections
					? entry.message
					: undefined;
		if (head) {
			return Object.fromEntries(
				Object.entries(head.sections ?? {}).filter((item): item is [string, string] => item[1] !== null),
			);
		}
	}
	return undefined;
}

// Custom message details come from session files and other extensions.
function isContextDetails(details: unknown): details is { sections: ContextSections } {
	if (typeof details !== "object" || details === null || !("sections" in details)) return false;
	const sections = details.sections;
	return (
		typeof sections === "object" &&
		sections !== null &&
		!Array.isArray(sections) &&
		Object.values(sections).every((value) => typeof value === "string")
	);
}

/**
 * Render only changed sections. The full state travels in details, not in a separate
 * session entry, so a restart can distinguish a delivered update from an interrupted one.
 */
export function createContextUpdate(previous: ContextSections, current: ContextSections): CustomMessage | undefined {
	const changes: string[] = [];
	for (const [key, text] of Object.entries(current)) {
		const old = Object.hasOwn(previous, key) ? previous[key] : undefined;
		if (old === text) continue;
		if (old === undefined) {
			changes.push(`New section ${key}:\n${text}`);
			continue;
		}
		const diff = diffLines(old, text)
			.map((part) => {
				const prefix = part.added ? "+" : part.removed ? "-" : " ";
				return part.value
					.split("\n")
					.filter((line, i, lines) => i < lines.length - 1 || line !== "")
					.map((line) => prefix + line)
					.join("\n");
			})
			.join("\n");
		changes.push(`Changed section ${key}:\n${diff}`);
	}
	for (const key of Object.keys(previous)) {
		if (!Object.hasOwn(current, key)) changes.push(`Removed section ${key}.`);
	}
	if (changes.length === 0) return undefined;
	return {
		role: "custom",
		customType: "context_update",
		content: `[Context update]\n${changes.join("\n\n")}\n[/Context update]`,
		display: false,
		details: { sections: { ...current } },
		timestamp: Date.now(),
	};
}
