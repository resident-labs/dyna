import { test } from "node:test";
import { strict as assert } from "node:assert";
import { postData, dataChildren, dataText, displayJSONStrings } from "./post-data.ts";
import { feedRows, visibleAttachments } from "./feed.ts";
import { decodeEvent } from "./event.ts";

const canonical = (value: unknown) => JSON.stringify(value).replace(/[<>&\u2028\u2029]/g,char=>`\\u${char.charCodeAt(0).toString(16).padStart(4,"0")}`);

test("encoded byte boundary is inclusive and Unicode is bytes", () => {
 for (const bytes of [299, 300, 301]) {
  const model = postData(canonical("x".repeat(bytes - 2)));
  assert.equal(model.bytes, bytes);
  assert.equal(model.inline, bytes <= 300);
 }
 assert.equal(postData(canonical("雪🙂")).bytes, 9);
 assert.equal(postData(canonical("雪".repeat(100))).inline, false);
 for (const bytes of [299, 300, 301]) {
  const model=postData(canonical("<".repeat(49)+"x".repeat(bytes-296)));
  assert.equal(model.bytes,bytes); assert.equal(model.inline,bytes<=300);
 }
 assert.equal(postData(canonical("\u2028")).bytes,8);
});
test("typed summaries distinguish arrays, objects, scalars and null", () => {
 assert.equal(postData('{"a":1,"b":2}').summary, "2 keys");
 assert.equal(postData("[1,2,3]").summary, "3 items");
 assert.equal(postData("null").summary, "null");
 assert.equal(postData("true").summary, "boolean");
 assert.equal(postData("42").summary, "number");
 assert.equal(postData('"text"').summary, "string");
 const hostile = '</script><img src=x onerror=alert(1)>';
 assert.equal(JSON.parse(postData(canonical({ hostile })).json).hostile, hostile);
});
test("post event and row retain typed data and explicit null; text fences remain text", () => {
 for (const value of [null, [1,2], {groups:[{conflicts:["file.go"]}]}, "text", 42]) {
  const data=canonical(value);
  const text = 'summary\n```json\n{"not":"typed"}\n```';
  const event = decodeEvent({seq:1,id:"post",at:"2026-11-05T09:46:00Z",room:"r",from:{kind:"session",session:"crew"},type:"post.created",body:{text,data,home:{kind:"feed"}}});
  assert.ok(event);
  const row=feedRows([event],{kind:"feed"}).find(row=>row.kind==="post");
  assert.ok(row?.kind==="post");assert.equal(row.text,text);assert.deepEqual(row.data,data);
 }
});

test("pending forwarded data stays typed until the stored post replaces it", () => {
 const data=canonical({groups:[{conflicts:["file.go"]}]});
 const row=feedRows([],{kind:"feed"},[{id:1,at:"2026-11-05T09:46:00Z",text:"summary",data,attachments:[],to:[],failed:false}]).find(row=>row.kind==="post");
 assert.ok(row?.kind==="post");assert.deepEqual(row.data,data);
});

test("only the typed associated attachment is hidden; names do not decide and media positions survive", () => {
 const files=[{name:"data.json",sha256:"other",media:"application/json",bytes:17},{name:"data.json",sha256:"associated",media:"application/json",bytes:1025},{name:"notes.txt",sha256:"notes",media:"text/plain",bytes:42}];
 assert.deepEqual(visibleAttachments(files,1),[{file:files[0],position:0},{file:files[2],position:2}]);
 assert.deepEqual(visibleAttachments(files).map(({file})=>file),files);
 assert.deepEqual(visibleAttachments(files,null).map(({file})=>file),files);
 const event=decodeEvent({seq:1,id:"post",at:"2026-11-05T09:46:00Z",room:"r",from:{kind:"session",session:"crew"},type:"post.created",body:{text:"summary",data:canonical({large:"x".repeat(1100)}),dataAttachment:1,attachments:files,home:{kind:"feed"}}});
 assert.ok(event);const row=feedRows([event],{kind:"feed"}).find(row=>row.kind==="post");
 assert.ok(row?.kind==="post");assert.equal(row.dataAttachment,1);assert.equal(row.attachments.length,3,"agent/stored attachment contract is untouched");
});

test("canonical numeric and string token spellings survive the model unchanged", () => {
 for (const raw of ['{"n":9007199254740993,"e":1e0,"zero":-0}', '1e400', '"\\u0061"']) {
  assert.equal(postData(raw).json,raw);
  assert.equal(postData(raw).bytes,new TextEncoder().encode(raw).byteLength);
 }
});

test("canonical exponent byte boundaries are not reconstructed from JS numbers", () => {
 const base='{"n":1e0,"pad":""}';
 for (const bytes of [299,300,301]) {
  const raw='{"n":1e0,"pad":"'+'x'.repeat(bytes-base.length)+'"}';
  const model=postData(raw);assert.equal(model.bytes,bytes);assert.equal(model.inline,bytes<=300);assert.equal(model.json,raw);
 }
});

test("encoded API data survives native envelope decode, feed and forward round trips", () => {
 const raw='{"n":9007199254740993,"e":1e0}';
 const event=decodeEvent(JSON.parse(JSON.stringify({seq:1,id:"post",at:"2026-11-05T09:46:00Z",room:"r",from:{kind:"session",session:"crew"},type:"post.created",body:{text:"summary",data:raw,home:{kind:"feed"}}})));
 assert.ok(event);const row=feedRows([event],{kind:"feed"}).find(row=>row.kind==="post");assert.ok(row?.kind==="post");
 const forwarded=JSON.parse(JSON.stringify({data:row.data}));assert.equal(forwarded.data,raw);assert.equal(postData(forwarded.data).json,raw);
});

test("7000-deep canonical JSON is counted without recursive parse or serialization", () => {
 const raw='['.repeat(7000)+'0'+']'.repeat(7000);
 const parse=JSON.parse;JSON.parse=()=>{throw Error("eager JSON.parse");};
 try { assert.doesNotThrow(()=>postData(raw));const model=postData(raw);assert.equal(model.summary,"1 items");assert.equal(model.json,raw);assert.equal(model.inline,false); }
 finally {JSON.parse=parse;}
});

test("lazy ranges preserve number tokens, escaped keys, strings and duplicate members", () => {
 const raw='{"\\u0061":9007199254740993,"nested":[1e0,-0,{"brace":"[}","quote":"\\\\\\\""}],"same":true,"same":null}';
 assert.doesNotThrow(()=>JSON.parse(raw));
 const root=postData(raw);assert.equal(root.summary,"4 keys");
 const entries=dataChildren(root.value);assert.equal(entries.length,4);
 assert.equal(entries[0].label,'"\\u0061"');assert.equal(dataText(entries[0].value),"9007199254740993");
 const nested=dataChildren(entries[1].value);
 assert.deepEqual(nested.map(entry=>entry.label),["0","1","2"]);
 assert.equal(dataText(nested[0].value),"1e0");assert.equal(dataText(nested[1].value),"-0");
 assert.equal(dataChildren(nested[2].value).length,2);
 assert.equal(displayJSONStrings('{"n":9007199254740993,"e":1e0,"s":"\\u003c"}'),'{"n":9007199254740993,"e":1e0,"s":"<"}');
 assert.equal(dataChildren(root.value,2).length,2);
 const deep=postData('['.repeat(7000)+'0'+']'.repeat(7000));
 assert.equal(dataChildren(deep.value).length,1);
 assert.equal(dataChildren(dataChildren(deep.value)[0].value).length,1);
});
