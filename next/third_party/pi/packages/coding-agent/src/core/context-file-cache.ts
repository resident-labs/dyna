import { createHash } from "node:crypto";
import { readFileSync, statSync } from "node:fs";

interface FileStamp {
	ino: number;
	size: number;
	mtimeMs: number;
	ctimeMs: number;
	isFile: boolean;
}

interface CachedFile {
	stamp: FileStamp;
	hash: string;
	content: string;
}

/** Stat files first so unchanged prompt resources require no reads or hashes. */
export class ContextFileCache {
	private readonly stamps = new Map<string, FileStamp | undefined>();
	private readonly files = new Map<string, CachedFile>();

	private readonly stat: (path: string) => FileStamp | undefined;
	private readonly read: (path: string) => string;
	private readonly hash: (text: string) => string;

	constructor(
		stat = (path: string): FileStamp | undefined => {
			const value = statSync(path, { throwIfNoEntry: false });
			return (
				value && {
					ino: value.ino,
					size: value.size,
					mtimeMs: value.mtimeMs,
					ctimeMs: value.ctimeMs,
					isFile: value.isFile(),
				}
			);
		},
		read = (path: string) => readFileSync(path, "utf8"),
		hash = (text: string) => createHash("sha256").update(text).digest("hex"),
	) {
		this.stat = stat;
		this.read = read;
		this.hash = hash;
	}

	changed(paths: string[]): boolean {
		let changed = false;
		for (const path of paths) {
			const current = this.stat(path);
			const previous = this.stamps.get(path);
			if (!this.stamps.has(path) || !sameStamp(previous, current)) changed = true;
			this.stamps.set(path, current);
		}
		return changed;
	}

	readFile(path: string): string | undefined {
		const stamp = this.stat(path);
		if (!stamp?.isFile) {
			this.files.delete(path);
			return undefined;
		}
		const previous = this.files.get(path);
		if (previous && sameStamp(previous.stamp, stamp)) return previous.content;
		const content = this.read(path);
		const hash = this.hash(content);
		const value = previous?.hash === hash ? previous.content : content;
		this.files.set(path, { stamp, hash, content: value });
		return value;
	}
}

function sameStamp(a: FileStamp | undefined, b: FileStamp | undefined): boolean {
	return (
		a === b ||
		(!!a &&
			!!b &&
			a.ino === b.ino &&
			a.size === b.size &&
			a.mtimeMs === b.mtimeMs &&
			a.ctimeMs === b.ctimeMs &&
			a.isFile === b.isFile)
	);
}
