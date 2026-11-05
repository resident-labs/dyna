import { test } from "node:test";
import { strict as assert } from "node:assert";
import { postData } from "./post-data.ts";
import { feedRows, feedAttachments } from "./feed.ts";
import { decodeEvent } from "./event.ts";

test("encoded byte boundary is inclusive and Unicode is bytes", () => {
 for (const bytes of [299, 300, 301]) {
  const model = postData("x".repeat(bytes - 2));
  assert.equal(model.bytes, bytes);
  assert.equal(model.inline, bytes <= 300);
 }
 assert.equal(postData("雪🙂").bytes, 9);
 assert.equal(postData("雪".repeat(100)).inline, false);
 for (const bytes of [299, 300, 301]) {
  const model=postData("<".repeat(49)+"x".repeat(bytes-296));
  assert.equal(model.bytes,bytes); assert.equal(model.inline,bytes<=300);
 }
 assert.equal(postData("\u2028").bytes,8);
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

test("pending forwarded data stays typed until the stored post replaces it", () => {
 const data={groups:[{conflicts:["file.go"]}]};
 const row=feedRows([],{kind:"feed"},[{id:1,at:"2026-11-05T09:46:00Z",text:"summary",data,attachments:[],to:[],failed:false}]).find(row=>row.kind==="post");
 assert.ok(row?.kind==="post");assert.deepEqual(row.data,data);
});

test("only the typed associated attachment is hidden; names do not decide and media positions survive", () => {
 const files=[{name:"data.json",sha256:"other",media:"application/json",bytes:17},{name:"data.json",sha256:"associated",media:"application/json",bytes:1025},{name:"notes.txt",sha256:"notes",media:"text/plain",bytes:42}];
 assert.deepEqual(feedAttachments(files,1),[{file:files[0],position:0},{file:files[2],position:2}]);
 assert.deepEqual(feedAttachments(files).map(({file})=>file),files);
 assert.deepEqual(feedAttachments(files,null).map(({file})=>file),files);
 const event=decodeEvent({seq:1,id:"post",at:"2026-11-05T09:46:00Z",room:"r",from:{kind:"session",session:"crew"},type:"post.created",body:{text:"summary",data:{large:"x".repeat(1100)},dataAttachment:1,attachments:files,home:{kind:"feed"}}});
 assert.ok(event);const row=feedRows([event],{kind:"feed"}).find(row=>row.kind==="post");
 assert.ok(row?.kind==="post");assert.equal(row.dataAttachment,1);assert.equal(row.attachments.length,3,"agent/stored attachment contract is untouched");
});
