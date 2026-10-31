import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

// Dyna replaces the placeholder with the shared reserved argument name at startup.
const callIDArgument = "__DYNA_CALL_ID_ARGUMENT__";

export default function (pi: ExtensionAPI) {
	const roots = new Map<string, string>();
	pi.on("turn_start", () => roots.clear());
	pi.on("tool_call", (event) => {
		// Nested calls are not transcript items; link their posts to the outer call.
		const root = event.parentToolCallId ? (roots.get(event.parentToolCallId) ?? event.parentToolCallId) : event.toolCallId;
		roots.set(event.toolCallId, root);
		if (event.toolName === "mcp__shell__shell") {
			event.input[callIDArgument] = root;
		}
	});
}
