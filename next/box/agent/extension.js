// Dyna's Pi extension. The box agent replaces the placeholders at startup.
import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const callIDArgument = "__DYNA_CALL_ID_ARGUMENT__";
const dynaURL = "__DYNA_MCP_URL__";
const shellURL = "__DYNA_SHELL_URL__";
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

// What bash and job give a script. A script reads the whole output here, and the model reads the text.
const jobSchema = {
	type: "object",
	properties: {
		output: { type: "string" },
		job: { type: "string" },
		running: { type: "boolean" },
		status: { type: "string" },
		exit_code: { type: "number" },
		jobs: { type: "array", items: {} },
	},
	additionalProperties: false,
};

// The box's job server. A refusal is its typed error, as the tool's error text.
async function shell(path, body, signal) {
	const response = await fetch(shellURL + path, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify(body),
		signal,
	});
	const answer = await response.json();
	if (!response.ok) throw new Error(`${answer.code}: ${answer.message}`);
	return answer;
}

// The model reads the end of a long output and is told where the rest is. Pi's own bash cuts the same way.
const outputLimit = 20 << 10;
async function modelText(text) {
	if (Buffer.byteLength(text) <= outputLimit) return text;
	const { truncateTail, formatSize } = await import("@earendil-works/pi-coding-agent");
	const cut = truncateTail(text, { maxBytes: outputLimit });
	const path = join(tmpdir(), `dyna-output-${randomUUID()}.txt`);
	await writeFile(path, text);
	return `${cut.content}\n\n[Output cut to the last ${formatSize(cut.outputBytes)} of ${formatSize(cut.totalBytes)}. Full output: ${path} (read it with offset/limit)]`;
}

// What bash adds when a command has moved to the background.
const movedNote = "The job is in the background. You can end your turn and Dyna will wake you when it completes.";

// One job's state as a tool result. A nonzero exit is not a tool error, since grep finding nothing is one: the text says the code.
async function jobResult(answer, moved = false) {
	let state = `[job ${answer.job}: ${answer.status}]`;
	if (answer.running) state = moved ? `[job ${answer.job}] ${movedNote}` : `[job ${answer.job} is running in the background]`;
	const text = await modelText(`${answer.output.trimEnd()}\n${state}`.trimStart());
	return { content: [{ type: "text", text }], details: {}, structuredContent: answer };
}

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
		if (event.toolName.startsWith("mcp__dyna__")) event.input[callIDArgument] = root;
	});

	// Dyna's bash takes the place of Pi's, which has no background jobs. An extension's tool wins over a built-in of its name.
	pi.registerTool({
		name: "bash",
		label: "bash",
		description:
			"Run a bash command. It runs in the foreground for up to 30 s, then keeps running in the background as a job.\njob output returns only text printed since the last read, and is empty when a job printed nothing.\nThe command runs as bash -c, so its text is in the process list. pkill -f with a pattern from that text also matches this shell and ends the job with SIGTERM.\nhost runs the command on that machine instead of this box, under your host grant.",
		promptSnippet: "Run bash commands, here or on a host",
		parameters: {
			type: "object",
			properties: {
				command: { type: "string" },
				description: { type: "string", description: "What the command is for, as its title in the Jobs list" },
				cwd: { type: "string" },
				host: { type: "string" },
				timeout: { type: "integer", description: "Seconds a host run may take: 600 by default, at most 7200" },
			},
			required: ["command"],
		},
		outputSchema: jobSchema,
		async execute(toolCallId, { command, description, cwd, host, timeout }, signal, _onUpdate, ctx) {
			const callId = roots.get(toolCallId) ?? toolCallId;
			// A worker's tree is Pi's working directory, so a relative cwd starts from there.
			const start = host ? cwd : resolve(ctx.cwd, cwd ?? "");
			return jobResult(await shell("run", { command, description, cwd: start, callId, host, timeout }, signal), true);
		},
	});
	pi.registerTool({
		name: "job",
		label: "job",
		description: "Background jobs that bash started.\noutput: read a job's new output.\nstop: stop a job and its whole process group.\nlist: list the jobs.",
		promptSnippet: "Read, stop or list background jobs",
		parameters: {
			type: "object",
			properties: { action: { type: "string", enum: ["output", "stop", "list"] }, job: { type: "string" } },
			required: ["action"],
		},
		outputSchema: jobSchema,
		async execute(_toolCallId, { action, job }, signal) {
			const answer = await shell("job", { action, job }, signal);
			if (action !== "list") return jobResult(answer);
			const lines = answer.jobs.map((row) => [row.job, row.status, row.host ? `on ${row.host}: ${row.command}` : row.command].join("\t"));
			return { content: [{ type: "text", text: await modelText(lines.join("\n")) }], details: {}, structuredContent: answer };
		},
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
