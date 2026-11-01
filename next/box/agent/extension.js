// Dyna's Pi extension. The box agent replaces the placeholders at startup.
import { readFile } from "node:fs/promises";

const callIDArgument = "__DYNA_CALL_ID_ARGUMENT__";
const dynaURL = "__DYNA_MCP_URL__";
const postTool = "mcp__dyna__post";

// What one model request can carry in images. The model route takes 32 MiB in all, and the rest is for the conversation.
const imageLimit = 20;
const imageBytesLimit = 20 << 20;
// An image the engine's frame shows the model: see attachedFiles in engine/input_frame.go.
const framedImage = /(\/\S+\/\d+\/\S+) \((image\/\S+), [^,)]+, shown to you\)/g;
const attachments = "__DYNA_ATTACHMENTS__/";

// Pi's own MCP client numbers its requests on this server, so these IDs are strings.
let requests = 0;

async function dynaRPC(method, params, signal) {
	const response = await fetch(dynaURL, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ jsonrpc: "2.0", id: `dyna-extension-${++requests}`, method, params }),
		signal,
	});
	const body = await response.json();
	if (body.error) throw new Error(body.error.message);
	return body.result;
}

// The same result schema Pi gives every MCP tool, so scripts see one shape.
const resultSchema = {
	type: "object",
	properties: { content: { type: "array", items: {} }, structuredContent: {}, isError: { type: "boolean" } },
	required: ["content"],
	additionalProperties: true,
};

// The image for one attachment, shrunk to the model's limit, or the note that takes its place.
async function loadImage(path, media, model) {
	try {
		const { resizeImage, formatDimensionNote } = await import("@earendil-works/pi-coding-agent");
		const resized = await resizeImage(await readFile(path), media, model?.inputLimits?.images?.resize);
		if (!resized) return { note: `[Dyna could not shrink ${path} to fit the model. Read the file with your tools.]` };
		return { image: { type: "image", data: resized.data, mimeType: resized.mimeType }, note: formatDimensionNote(resized) };
	} catch (error) {
		return { note: `[Dyna could not show ${path}: ${error.code ?? error.message}]` };
	}
}

// Pi's session file keeps only each input's frame, so an image adds nothing to the transcript and stays
// visible across a restart. Each request takes the images back from their files, newest first, up to the
// limits. A request is the only place the bytes appear, and an image left out can still be read as a file.
function imageContext() {
	let loaded = new Map();
	return async (event, ctx) => {
		const kept = new Map();
		let images = 0;
		let bytes = 0;
		for (const message of event.messages.toReversed()) {
			if (message.role !== "user" || !Array.isArray(message.content)) continue;
			const frame = message.content[0]?.text?.split("\n", 1)[0];
			if (!frame?.startsWith("[Dyna] ")) continue;
			for (const [, path, media] of frame.matchAll(framedImage)) {
				if (!path.startsWith(attachments)) continue;
				const room = images < imageLimit;
				const entry = room ? (loaded.get(path) ?? (await loadImage(path, media, ctx?.model))) : {};
				const size = entry.image?.data.length ?? 0;
				if (!room || bytes + size > imageBytesLimit) {
					message.content.push({ type: "text", text: `[${path} is left out of this request to save space. Read the file to see it.]` });
					continue;
				}
				kept.set(path, entry);
				if (entry.image) {
					message.content.push(entry.image);
					images++;
					bytes += size;
				}
				if (entry.note) message.content.push({ type: "text", text: entry.note });
			}
		}
		loaded = kept;
		return { messages: event.messages };
	};
}

export default async function (pi) {
	pi.on("context", imageContext());
	const roots = new Map();
	pi.on("turn_start", () => roots.clear());
	pi.on("tool_call", (event) => {
		// Nested calls are not transcript items; link their posts to the outer call.
		const root = event.parentToolCallId ? (roots.get(event.parentToolCallId) ?? event.parentToolCallId) : event.toolCallId;
		roots.set(event.toolCallId, root);
		if (event.toolName === "mcp__shell__shell" || event.toolName.startsWith("mcp__dyna__")) {
			event.input[callIDArgument] = root;
		}
	});

	// Only a tool's own result can carry terminate: MCP results and the tool_result hook drop it. So this
	// tool takes the post tool's place. Pi keeps the first registration of a name, and extensions from
	// --extension load before its MCP extension. If the tool cannot be read here, Pi's plain MCP tool stays,
	// and the call IDs above still link posts to their tool calls.
	let post;
	try {
		const { tools } = await dynaRPC("tools/list", {});
		post = tools.find((tool) => tool.name === "post");
		if (!post) throw new Error("the dyna server lists no post tool");
	} catch (error) {
		console.error(`dyna extension: keeping the plain post tool: ${error.message}`);
		return;
	}
	pi.registerTool({
		name: postTool,
		label: "dyna/post",
		description: post.description,
		parameters: post.inputSchema,
		outputSchema: resultSchema,
		exposure: "direct",
		namespace: { name: "mcp__dyna", aliases: ["dyna"] },
		annotations: post.annotations,
		async execute(toolCallId, params, signal) {
			const result = await dynaRPC("tools/call", { name: "post", arguments: params }, signal);
			const failed = result.isError === true;
			const asked = params.endTurn === true && !failed;
			// A script's nested result never reaches the agent loop, so only a direct call can end the turn.
			const direct = roots.get(toolCallId) === toolCallId;
			let { content, structuredContent } = result;
			if (asked && !direct) {
				structuredContent = { ...structuredContent, turnContinues: true };
				content = [{ type: "text", text: JSON.stringify(structuredContent) }];
			}
			// The turn ends only with a post the harness can report as its reply.
			const ends = asked && direct && typeof structuredContent?.event === "string";
			return {
				content,
				details: { server: "dyna", tool: "post", ...(ends ? { endPost: structuredContent.event } : {}) },
				structuredContent: { content, structuredContent, ...(failed ? { isError: true } : {}) },
				...(failed ? { isError: true } : {}),
				...(ends ? { terminate: true } : {}),
			};
		},
	});
}
