#!/usr/bin/env node
// Offline heuristic only. Never log assistant text or tool argument contents.
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';

const empty = () => ({ characters: 0, letter_to_digit: 0, digit_to_letter: 0, lower_to_upper: 0, sites: 0 });
export function measureText(text) {
 const result = empty();
 result.characters = [...text].length;
 for (let i = 1; i < text.length; i++) {
  const pair = text.slice(i - 1, i + 1);
  if (/[A-Za-z][0-9]/.test(pair)) result.letter_to_digit++;
  else if (/[0-9][A-Za-z]/.test(pair)) result.digit_to_letter++;
  else if (/[a-z][A-Z]/.test(pair)) result.lower_to_upper++;
 }
 result.sites = result.letter_to_digit + result.digit_to_letter + result.lower_to_upper;
 return result;
}
function add(target, text) {
 const count = measureText(text);
 for (const key of Object.keys(count)) target[key] += count[key];
}
function argumentStrings(value, target) {
 if (typeof value === 'string') add(target, value);
 else if (Array.isArray(value)) for (const child of value) argumentStrings(child, target);
 else if (value && typeof value === 'object') for (const child of Object.values(value)) argumentStrings(child, target);
}
function timestamp(value) {
 const ms = typeof value === 'number' ? value : typeof value === 'string' ? Date.parse(value) : NaN;
 const date = new Date(ms);
 return Number.isFinite(date.getTime()) ? date.toISOString() : null;
}

export function measureJSONL(input, options = {}) {
 const result = { assistant_records: 0, non_assistant_records: 0, duplicate_records: 0, skipped_initial_line: 0, unknown_model_records: 0, unknown_timestamp_records: 0, excluded_model: 0, excluded_time: 0, invalid_argument_json: 0, cohorts: [] };
 const seen = new Set(), cohorts = new Map();
 const after = options.after ? timestamp(options.after) : null;
 const before = options.before ? timestamp(options.before) : null;
 if ((options.after && !after) || (options.before && !before)) throw new Error('invalid timestamp filter');
 const lines = input.split('\n');
 for (let i = 0; i < lines.length; i++) {
  if (!lines[i].trim()) continue;
  let record;
  try { record = JSON.parse(lines[i]); }
  catch {
   if (i === 0 && options.allowTruncatedFirstLine) { result.skipped_initial_line++; continue; }
   throw new Error(`invalid JSON at line ${i + 1}`);
  }
  if (!record || typeof record !== 'object') { result.non_assistant_records++; continue; }
  // PiG writes {type:"message", message:{role,...}}; a decoded assistant message is also accepted.
  const message = record.type === 'message' ? record.message : record.role === 'assistant' ? record : null;
  if (!message || message.role !== 'assistant') { result.non_assistant_records++; continue; }
  if (typeof record.id === 'string') {
   if (seen.has(record.id)) { result.duplicate_records++; continue; }
   seen.add(record.id);
  }
  result.assistant_records++;
  const model = typeof message.model === 'string' && message.model ? message.model : null;
  if (!model) { result.unknown_model_records++; continue; }
  if (options.model && options.model !== model) { result.excluded_model++; continue; }
  const time = timestamp(record.timestamp ?? message.timestamp);
  if (!time) result.unknown_timestamp_records++;
  if ((after || before) && (!time || (after && time < after) || (before && time >= before))) { result.excluded_time++; continue; }
  const provider = typeof message.provider === 'string' ? message.provider : 'unknown';
  const key = `${provider}/${model}`;
  if (!cohorts.has(key)) cohorts.set(key, { provider, model, assistant_messages: 0, first_timestamp: null, last_timestamp: null, text: empty(), tool_arguments: empty() });
  const cohort = cohorts.get(key);
  cohort.assistant_messages++;
  if (time) {
   if (!cohort.first_timestamp || time < cohort.first_timestamp) cohort.first_timestamp = time;
   if (!cohort.last_timestamp || time > cohort.last_timestamp) cohort.last_timestamp = time;
  }
  if (typeof message.content === 'string') add(cohort.text, message.content);
  else if (Array.isArray(message.content)) {
   for (const block of message.content) {
    if (!block || typeof block !== 'object') continue;
    if (block.type === 'text' && typeof block.text === 'string') add(cohort.text, block.text);
    else if (block.type === 'toolCall') {
     let args = block.arguments;
     // Serialized tool arguments are decoded once at their owning boundary, not again at every leaf.
     if (typeof args === 'string') {
      try { args = JSON.parse(args); }
      catch { result.invalid_argument_json++; }
     }
     argumentStrings(args, cohort.tool_arguments);
    }
   }
  } else if (typeof message.text === 'string') add(cohort.text, message.text);
 }
 result.cohorts = [...cohorts.values()].sort((a,b) => `${a.provider}/${a.model}`.localeCompare(`${b.provider}/${b.model}`));
 for (const cohort of result.cohorts) for (const field of ['text', 'tool_arguments']) {
  const count = cohort[field];
  count.sites_per_10000_characters = count.characters ? Number((10000 * count.sites / count.characters).toFixed(4)) : null;
 }
 return result;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
 const { values, positionals } = parseArgs({ allowPositionals: true, options: { model: {type:'string'}, after: {type:'string'}, before: {type:'string'}, 'allow-truncated-first-line': {type:'boolean'}, help: {type:'boolean'} } });
 if (values.help || !positionals.length) {
  console.log('Usage: node spacing-count.mjs [--model ID] [--after ISO] [--before ISO] [--allow-truncated-first-line] FILE.jsonl...');
  if (!values.help) process.exitCode = 1;
 } else {
  const files = positionals.map(path => {
   const bytes = readFileSync(path);
   return { path, bytes:bytes.length, sha256:createHash('sha256').update(bytes).digest('hex'), ...measureJSONL(bytes.toString('utf8'), {model:values.model, after:values.after, before:values.before, allowTruncatedFirstLine:values['allow-truncated-first-line']}) };
  });
  console.log(JSON.stringify({ heuristic_version:1, filters:{ model:values.model ?? null, after:values.after ?? null, before:values.before ?? null }, files }, null, 2));
 }
}
