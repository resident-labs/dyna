// Run against Vite with Node 24: node test/workflow-data-integrity.browser.mjs /path/to/playwright/index.mjs
import assert from "node:assert/strict";
import {readFile} from "node:fs/promises";
const {chromium}=await import(process.argv[2]);
const browser=await chromium.launch({executablePath:"/usr/bin/chromium",args:["--no-sandbox"]});
const context=await browser.newContext({permissions:["clipboard-read","clipboard-write"]});
const page=await context.newPage();const errors=[];page.on("pageerror",error=>errors.push(String(error)));
await page.goto("http://localhost:5173/test/workflow-data.html?integrity");
const numeric='{"n":9007199254740993,"e":1e0,"zero":-0}';
const small=page.getByRole("region",{name:"Numeric integrity",exact:true});
await small.locator("code").waitFor();assert.equal(await small.locator("code").innerText(),"data: "+numeric,"inline numbers retain every token");
await small.getByRole("button",{name:"Copy JSON",exact:true}).click();assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),numeric);
const base='{"n":1e0,"pad":""}',exponent='{"n":1e0,"pad":"'+"x".repeat(301-base.length)+'"}';
const boundary=page.getByRole("region",{name:"Exponent integrity",exact:true});
assert.equal(await boundary.locator("section.data > details").count(),1,"canonical 301 bytes remains collapsed, not reconstructed as 299 bytes");
assert.match(await boundary.locator("summary").innerText(),/301 B/);
await boundary.locator("summary").first().click();assert.match(await boundary.locator(".leaf").first().innerText(),/1e0$/,"tree number retains exponent spelling");
await boundary.getByRole("button",{name:"Copy JSON",exact:true}).click();assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),exponent);
let downloading=page.waitForEvent("download");await boundary.getByRole("button",{name:"Download",exact:true}).click();assert.equal(await readFile(await (await downloading).path(),"utf8"),exponent);
const raw="[".repeat(7000)+"0"+"]".repeat(7000),deep=page.getByRole("region",{name:"Deep integrity",exact:true});
assert.equal(await deep.locator("details details").count(),0,"deep JSON has no eager descendants");
await deep.locator("summary").first().click();await deep.getByRole("region",{name:"JSON data tree"}).waitFor();assert.equal(await deep.locator(".tree-scroll details").count(),2,"7000-deep value opens only the root and its folded child");
assert.equal(await deep.locator(".tree-scroll details[open]").count(),1);
await deep.getByRole("button",{name:"Copy JSON",exact:true}).click();assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),raw);
downloading=page.waitForEvent("download");await deep.getByRole("button",{name:"Download",exact:true}).click();assert.equal(await readFile(await (await downloading).path(),"utf8"),raw);
console.log("canonical large integer, exponent, 301-byte chip, numeric tree and 7000-deep lazy Copy/Download passed");

// Exercise the actual SessionThread, its real HTTP client and its steer/item/input adapters.
const data='{"n":9007199254740993,"e":1e0,"pad":"'+"x".repeat(1100)+'"}';
const files=[{name:"data.json",media:"application/json",bytes:17,sha256:"c".repeat(64)},{name:"data.json",media:"application/json",bytes:Buffer.byteLength(data),sha256:"a".repeat(64)},{name:"photo.png",media:"image/png",bytes:68,sha256:"d".repeat(64)}];
const at="2026-11-05T09:46:00Z",carried={data,dataAttachment:1,attachments:files,room:"r",at};
const response={session:"crew",turns:[{id:"turn",started:at,ended:"2026-11-05T09:47:00Z",outcome:"completed",inputs:[{...carried,id:"input",seq:1,type:"post.created",from:{kind:"owner"},text:"Input ownership"}],items:[{...carried,id:"post",event:"post-event",turn:"turn",seq:2,kind:"post",from:{kind:"session",session:"crew"},text:"Post ownership"},{...carried,id:"steer",event:"steer-event",turn:"turn",seq:3,kind:"steer",type:"post.created",from:{kind:"owner"},text:"Steer ownership"}]}]};
await page.route("**/v1/transcript.read",route=>route.fulfill({contentType:"application/json",body:JSON.stringify(response)}));
await page.route("**/v1/events.list",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({events:[],names:{},nextSeq:0})}));
await page.route("**/v1/conversation.see",route=>route.fulfill({contentType:"application/json",body:"{}"}));
await page.route("**/v1/files/"+"d".repeat(64),route=>route.fulfill({contentType:"image/png",body:Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX9kAAAAASUVORK5CYII=","base64")}));
await page.goto("http://localhost:5173/test/workflow-data.html?transcript");
for(const key of ["input","post","steer"]) {
 const row=page.locator(`[data-media-key="${key}"]`);
 await row.getByRole("region",{name:"Workflow data",exact:true}).waitFor();
 assert.equal(await row.locator(".file").count(),1,`${key}: only the typed owning file is suppressed, not both data.json files`);
 assert.match(await row.locator(".file").innerText(),/data\.json/);
 const chip=row.getByRole("region",{name:"Workflow data",exact:true});
 await chip.locator("summary").first().click();assert.match(await chip.locator(".leaf").first().innerText(),/9007199254740993$/);
 await chip.getByRole("button",{name:"Copy JSON",exact:true}).click();assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),data,`${key}: canonical bytes survive HTTP transcript and UI`);
 await row.locator(".images button").click();
 const dialog=page.getByRole("dialog",{name:"Media viewer"});await dialog.getByRole("img",{name:"photo.png"}).waitFor();
 assert.match(await dialog.getByRole("img",{name:"photo.png"}).getAttribute("src"),new RegExp("d".repeat(64)),`${key}: media opens original index 2 after filtering index 1`);
 await dialog.getByRole("button",{name:"Close",exact:true}).click();
}
assert.deepEqual(errors,[]);
console.log("actual transcript HTTP input/post/steer data, typed ownership, unrelated same-name files and original media positions passed");
await context.close();await browser.close();
