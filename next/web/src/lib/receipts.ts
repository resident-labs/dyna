import type { PostReceipt } from './contract.gen.ts';

// Taken means the engine assigned the input to a turn, not that a model read it.
export function receiptSummary(receipts: PostReceipt[], now: number) {
 const remaining = receipts.filter(r => r.state !== 'closed' && !(r.closed && r.state === 'pending'));
 const read = remaining.length > 0 && remaining.every(r => r.state === 'read');
 const taken = remaining.length > 0 && remaining.every(r => r.state === 'taken' || r.state === 'read');
 const closedOnly = receipts.length > 0 && remaining.length === 0;
 const countdown = (r: PostReceipt) => !r.closed && r.state === 'pending' && r.batchDeadline && Date.parse(r.batchDeadline) > now ? Math.ceil((Date.parse(r.batchDeadline) - now) / 1000) : undefined;
 const waits = receipts.flatMap(r => countdown(r) ?? []);
 return { label: read ? 'Read' : taken ? 'Awaiting read' : closedOnly ? 'Not delivered · recipient closed' : 'Queued', seconds: waits.length ? Math.max(...waits) : undefined,
 details: receipts.map(r => `${r.name}: ${r.state === 'read' ? 'Read' : r.state === 'taken' ? 'Awaiting read' : r.closed || r.state === 'closed' ? 'closed' : 'Queued'}${countdown(r) ? ` · in ${countdown(r)} s` : ''}`).join('\n') };
}
