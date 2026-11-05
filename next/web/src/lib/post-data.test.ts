import { test } from "node:test";
import { strict as assert } from "node:assert";
import { postData } from "./post-data.ts";
import { feedRows } from "./feed.ts";
import { decodeEvent } from "./event.ts";

test("encoded byte boundary is inclusive and Unicode is bytes", () => {
 for (const bytes of [299, 300, 301]) {
  const model = postData("x".repeat(bytes - 2));
  assert.equal(model.bytes, bytes);
  assert.equal(model.inline, bytes <= 300);
 }
 assert.equal(postData("雪🙂").bytes, 9);
 assert.equal(postData("雪".repeat(100)).inline, false);
});
test("typed summaries distinguish arrays, objects, scalars and null", () => {
 assert.equal(postData({ a: 1, b: 2 }).summary, "2 keys");
 assert.equal(postData([1,2,3]).summary, "3 items");
 assert.equal(postData(null).summary, "null");
 assert.equal(postData(true).summary, "boolean");
 assert.equal(postData(42).summary, "number");
 assert.equal(postData("text").summary, "string");
 const hostile = '</script><img src=x onerror=alert(1)>';
 assert.equal(JSON.parse(postData({ hostile }).json).hostile, hostile);
});
test("post event and row retain typed data and explicit null; text fences remain text", () => {
 for (const data of [null, [1,2], {groups:[{conflicts:["file.go"]}]}, "text", 42]) {
  const text = 'summary\n```json\n{"not":"typed"}\n```';
  const event = decodeEvent({seq:1,id:"post",at:"2026-11-05T09:46:00Z",room:"r",from:{kind:"session",session:"crew"},type:"post.created",body:{text,data,home:{kind:"feed"}}});
  assert.ok(event);
  const row=feedRows([event],{kind:"feed"}).find(row=>row.kind==="post");
  assert.ok(row?.kind==="post");assert.equal(row.text,text);assert.deepEqual(row.data,data);
 }
});
