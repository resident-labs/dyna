import { test } from 'node:test';
import assert from 'node:assert/strict';
import { measureText, measureJSONL } from './spacing-count.mjs';

const record = (id, content, model = 'gpt-6.1-sol', role = 'assistant') => ({ type: 'message', id, timestamp: '2026-11-05T12:00:00Z', message: { role, model, provider: 'openai', content } });
const jsonl = (...rows) => rows.map(x => JSON.stringify(x)).join('\n');

test('tiny good/bad prose and explicit code/ID false positives', () => {
 for (const [text, sites] of [
  ['Normal prose with 42 tasks and ID abc_def.', 0],
  ['at8395006b', 2],
  ['authorizesmoreSolsubagents ifneeded', 1],
  ['authorizes more Sol subagents if needed', 0],
  ['commit 8395006b and v2 and camelCase', 3], // deliberate false positives
  ['const taskCount = 42; const sha = "8395006b";', 2],
  ['alllowercasejoinedwords', 0], // deliberate false negative
 ]) { assert.equal(measureText(text).sites, sites, text); }
 assert.equal(measureText('é🙂 42').characters, 5);
});

test('PiG nested argument values are decoded once, text separate, thinking/user/tool skipped', () => {
 const args = { text: 'at8395006b', nested: [{ text: 'authorizesmoreSolsubagents ifneeded', count: 42 }], escaped: 'quote: "ok"\nnormal prose', camelKeyIgnored: true };
 const content = [{ type:'text', text:'normal 42 prose' }, { type:'thinking', thinking:'at8395006b' }, { type:'toolCall', arguments:args }];
 const data = measureJSONL(jsonl(record('a', content), record('b', [{type:'toolCall', arguments:JSON.stringify(args)}]), record('c', content, 'gpt-6.1-sol', 'user'), {type:'tool_result', message:{role:'tool',content}}, record('a', content)), {});
 const c = data.cohorts[0];
 assert.equal(c.assistant_messages, 2);
 assert.equal(c.text.sites, 0);
 assert.equal(c.text.characters, 15);
 assert.equal(c.tool_arguments.sites, 6);
 assert.equal(c.tool_arguments.characters, 2 * [args.text, args.nested[0].text, args.escaped].reduce((n,s) => n + [...s].length, 0));
 assert.equal(data.duplicate_records, 1);
 assert.equal(data.non_assistant_records, 2);
});

test('mixed model cohorts, unknown exclusion, timestamps and explicit fresh filter', () => {
 const old = record('old', [{type:'text',text:'at8395006b'}]); old.timestamp = '2026-11-04T12:00:00Z';
 const unknown = record('u', []); delete unknown.message.model;
 const data = measureJSONL(jsonl(old, record('sol', [{type:'text',text:'spaced prose'}]), record('control', [{type:'text',text:'camelCase'}], 'claude-sonnet-5-5'), unknown), {after:'2026-11-05T00:00:00Z', model:'gpt-6.1-sol'});
 assert.equal(data.cohorts.length, 1);
 assert.equal(data.cohorts[0].assistant_messages, 1);
 assert.equal(data.excluded_time, 1);
 assert.equal(data.excluded_model, 1);
 assert.equal(data.unknown_model_records, 1);
 const exact = record('exact', []);
 assert.equal(measureJSONL(jsonl(exact), {after:exact.timestamp}).cohorts[0].assistant_messages, 1);
 assert.equal(measureJSONL(jsonl(exact), {before:exact.timestamp}).excluded_time, 1);
 delete exact.timestamp;
 const missingTime = measureJSONL(jsonl(exact), {after:'2026-11-05T00:00:00Z'});
 assert.equal(missingTime.unknown_timestamp_records, 1);
 assert.equal(missingTime.excluded_time, 1);
 assert.equal(missingTime.cohorts.length, 0);
});

test('truncated first line opt-in, strict later corruption, no contents in errors', () => {
 const raw = 'SECRET invalid prefix\n' + jsonl(record('a', 'at8395006b'));
 assert.throws(() => measureJSONL(raw, {}), /invalid JSON at line 1/);
 assert.equal(measureJSONL(raw, {allowTruncatedFirstLine:true}).skipped_initial_line, 1);
 assert.throws(() => measureJSONL(jsonl(record('a', [])) + '\nSECRET', {allowTruncatedFirstLine:true}), err => err.message === 'invalid JSON at line 2');
});

test('content text wins over redundant text field; direct assistant shape supported', () => {
 const r = record('a', [{type:'text',text:'normal prose'}]); r.message.text = 'at8395006b';
 const data = measureJSONL(jsonl(r, {role:'assistant',model:'gpt-6.1-sol',provider:'openai',timestamp:1762344000000,content:'at8395006b'}), {});
 assert.equal(data.cohorts[0].text.sites, 2);
 assert.equal(data.cohorts[0].text.characters, 22);
});
