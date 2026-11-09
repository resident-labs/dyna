import { test } from 'node:test';
import assert from 'node:assert/strict';
import { receiptSummary } from './receipts.ts';
test('all recipients must take delivery; countdown is future only', () => {
 const pending = { session: 'a', name: 'builder', state: 'pending', batchDeadline: '2026-11-07T12:01:00Z' };
 const taken = { session: 'b', name: 'lead', state: 'taken' };
 const now = Date.parse('2026-11-07T12:00:20Z');
 assert.equal(receiptSummary([pending, taken], now).label, 'Queued');
 assert.equal(receiptSummary([pending, taken], now).seconds, 40);
 assert.match(receiptSummary([pending, taken], now).details, /lead: Awaiting read/);
 assert.equal(receiptSummary([pending], now + 60000).seconds, undefined);
 assert.equal(receiptSummary([taken, { ...taken, state: 'read' }], now).label, 'Awaiting read');
 assert.equal(receiptSummary([{ ...pending, batchDeadline: undefined }], now).seconds, undefined);
});
test('closed explicit recipients are tooltip context, not queued deliveries', () => {
 const now = Date.parse('2026-11-07T12:00:20Z');
 const closed = { session: 'a', name: 'former', state: 'closed', closed: true };
 const live = { session: 'b', name: 'live', state: 'pending', batchDeadline: '2026-11-07T12:01:00Z' };
 assert.equal(receiptSummary([closed, live], now).label, 'Queued');
 assert.equal(receiptSummary([closed, live], now).seconds, 40);
 assert.match(receiptSummary([closed, live], now).details, /former: closed/);
 for (const state of ['taken', 'read']) {
  assert.equal(receiptSummary([closed, { ...live, state }], now).label, state === 'read' ? 'Read' : 'Awaiting read');
 }
 assert.equal(receiptSummary([closed], now).label, 'Not delivered · recipient closed');
 assert.equal(receiptSummary([closed, { ...closed, session: 'c', name: 'also closed' }], now).label, 'Not delivered · recipient closed');
 assert.equal(receiptSummary([{ ...closed, state: 'read' }], now).label, 'Read');
 assert.match(receiptSummary([{ ...closed, state: 'read' }], now).details, /former: Read/);
 assert.equal(receiptSummary([{ ...live, closed: true }], now).label, 'Not delivered · recipient closed');
 assert.equal(receiptSummary([{ ...live, closed: true }], now).seconds, undefined);
});
