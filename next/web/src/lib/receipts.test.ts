import { test } from 'node:test';
import assert from 'node:assert/strict';
import { receiptSummary } from './receipts.ts';
test('all recipients must take delivery; countdown is future only', () => {
 const pending = { session: 'a', name: 'builder', state: 'pending', batchDeadline: '2026-11-07T12:01:00Z' };
 const taken = { session: 'b', name: 'lead', state: 'taken' };
 const now = Date.parse('2026-11-07T12:00:20Z');
 assert.equal(receiptSummary([pending, taken], now).label, 'Queued');
 assert.equal(receiptSummary([pending, taken], now).seconds, 40);
 assert.match(receiptSummary([pending, taken], now).details, /lead: Delivered/);
 assert.equal(receiptSummary([pending], now + 60000).seconds, undefined);
 assert.equal(receiptSummary([taken, { ...taken, state: 'read' }], now).label, 'Delivered');
 assert.equal(receiptSummary([{ ...pending, batchDeadline: undefined }], now).seconds, undefined);
});
