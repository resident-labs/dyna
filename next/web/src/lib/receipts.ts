import type { PostReceipt } from './contract.gen.ts';
export function receiptSummary(receipts: PostReceipt[], now: number) {
 const remaining = receipts.filter(r => r.state !== 'closed' && !(r.closed && r.state === 'pending'));
 const delivered = receipts.length > 0 && remaining.every(r => r.state === 'taken' || r.state === 'read');
 const countdown = (r: PostReceipt) => !r.closed && r.state === 'pending' && r.batchDeadline && Date.parse(r.batchDeadline) > now ? Math.ceil((Date.parse(r.batchDeadline) - now) / 1000) : undefined;
 const waits = receipts.flatMap(r => countdown(r) ?? []);
 return { label: delivered ? 'Delivered' : 'Queued', seconds: waits.length ? Math.max(...waits) : undefined,
 details: receipts.map(r => `${r.name}: ${r.closed || r.state === 'closed' ? 'closed' : r.state === 'taken' || r.state === 'read' ? 'Delivered' : 'Queued'}${countdown(r) ? ` · in ${countdown(r)} s` : ''}`).join('\n') };
}
