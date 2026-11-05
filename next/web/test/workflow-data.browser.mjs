// Run against Vite: node test/workflow-data.browser.mjs /path/to/playwright/index.mjs [output-directory]
import assert from "node:assert/strict";
import { mkdir, readFile } from "node:fs/promises";
const { chromium } = await import(process.argv[2]);
const out = process.argv[3] ?? "/box/shared/design/built";
await mkdir(out, {recursive:true});
const browser = await chromium.launch({executablePath:"/usr/bin/chromium",args:["--no-sandbox"]});

async function checkClamp(page, region) {
 const body=region.locator(".value").first();
 const before=await body.evaluate(node=>({height:node.clientHeight,line:parseFloat(getComputedStyle(node).lineHeight)}));
 assert.ok(before.height<=3*before.line+1,"string starts capped at three lines");
 const show=region.getByRole("button",{name:"Show all",exact:true});await show.waitFor();
 assert.equal(await show.getAttribute("aria-expanded"),"false");
 const id=await show.getAttribute("aria-controls");
 await show.focus();await page.keyboard.press("Enter");
 await page.waitForFunction(({id,height})=>document.getElementById(id).clientHeight>height,{id,height:before.height});
 const less=region.getByRole("button",{name:"Show less",exact:true});assert.equal(await less.getAttribute("aria-expanded"),"true");
 await less.focus();await page.keyboard.press("Enter");
 await page.waitForFunction(({id,line})=>document.getElementById(id).clientHeight<=line*3+1,{id,line:before.line});
}

for (const theme of ["light","carbon"]) for (const width of [1280,390]) {
 const context = await browser.newContext({viewport:{width,height:1000},permissions:["clipboard-read","clipboard-write"]});
 const page = await context.newPage();
 const errors=[];page.on("pageerror",error=>errors.push(String(error)));
 await page.addInitScript(theme=>localStorage.setItem("dyna.theme",theme),theme);
 await page.goto("http://localhost:5173/test/workflow-data.html");
 await page.getByRole("heading",{name:"Room feed"}).waitFor();await page.evaluate(()=>document.fonts.ready);
 const all=page.getByRole("region",{name:"Workflow data"}), data=all.first();
 assert.equal(await page.locator(".post").first().locator(".file").count(),0,"typed associated file is not a second feed card");
 assert.equal(await page.getByText("CONFLICT_SENTINEL",{exact:false}).count(),0,"no JSON wall before expansion");
 assert.equal(await data.locator("details details").count(),0,"no recursive initial tree");
 assert.equal(await page.locator("img[src=x]").count(),0,"hostile string is not HTML");
 for (let i=1;i<=6;i++) {
  assert.equal(await all.nth(i).locator("pre").count(),0,"small data is not a JSON block");
  assert.equal(await all.nth(i).getByRole("button",{name:"Download",exact:true}).count(),0,"no small-data Download");
 }
 assert.equal(await all.nth(1).locator("code").innerText(),'data: {"merged":9,"failed":false}');
 assert.equal(await all.nth(2).locator("code").innerText(),"data: null");assert.equal(await all.nth(3).locator("code").innerText(),"data: 42");
 assert.ok(await all.nth(1).locator("code").evaluate(node=>node.clientHeight<=parseFloat(getComputedStyle(node).lineHeight)+1),"small object is one quiet line");
 const smallCopy=all.nth(1).getByRole("button",{name:"Copy JSON",exact:true});
 await page.mouse.move(0,0);assert.equal(await smallCopy.evaluate(node=>getComputedStyle(node).opacity),"0","small Copy hidden until hover/focus");
 await smallCopy.hover();assert.equal(await smallCopy.evaluate(node=>getComputedStyle(node).opacity),"1","hover reveals Copy");
 await page.mouse.move(0,0);assert.equal(await smallCopy.evaluate(node=>getComputedStyle(node).opacity),"0","Copy quiet again after hover");
 await smallCopy.focus();assert.equal(await smallCopy.evaluate(node=>getComputedStyle(node).opacity),"1","keyboard focus reveals Copy");
 await page.keyboard.press("Enter");assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),'{"merged":9,"failed":false}',"small Copy preserves complete encoded bytes");
 await checkClamp(page,all.nth(5));
 const stringCopy=all.nth(5).getByRole("button",{name:"Copy JSON",exact:true});await stringCopy.focus();await page.keyboard.press("Enter");
 assert.equal(await page.evaluate(()=>navigator.clipboard.readText()),JSON.stringify("x".repeat(297)),"clamped small string copies all bytes");
 await page.mouse.click(5,5);
 assert.equal(await all.nth(5).locator(":scope > details").count(),0,"299 bytes inline");
 assert.equal(await all.nth(6).locator(":scope > details").count(),0,"300 bytes inline");
 assert.equal(await all.nth(7).locator(":scope > details").count(),1,"301 bytes collapsed");
 const name=`workflow-data-${theme==="carbon"?"dark":theme}-${width}`;
 await page.screenshot({path:`${out}/${name}-collapsed.png`,fullPage:true});
 await data.locator("summary").first().focus();await page.keyboard.press("Enter");await data.getByText('"groups"',{exact:true}).waitFor();
 assert.equal(await data.locator("summary .index").count(),0,"descendant collections stay folded when chip opens");
 assert.equal(await page.getByText("CONFLICT_SENTINEL",{exact:false}).count(),0,"no recursively expanded conflict content");
 await data.getByText('"groups"',{exact:true}).click();
 await data.getByText('3 keys',{exact:true}).nth(1).waitFor();
 assert.ok(await data.locator("summary .index").count()>0,"array positions have unquoted index labels");
 await data.locator("summary .index").filter({hasText:/^0$/}).first().click();
 await data.getByText("38 items",{exact:true}).waitFor();assert.equal(await page.getByText("CONFLICT_SENTINEL",{exact:false}).count(),0,"long array still folded");
 await data.getByText('"conflicts"',{exact:true}).first().click();await data.locator("summary .index").filter({hasText:/^0$/}).nth(1).click();
 await data.getByText("CONFLICT_SENTINEL",{exact:false}).waitFor();
 assert.ok(await data.locator("details").count()<100,"bounded initial descendant count");
 assert.equal(await data.locator(".key").filter({hasText:/^"0"$/}).count(),1,"numeric object key remains quoted");
 assert.equal(await data.locator("summary .index").first().innerText(),"0","array position is unquoted");
 assert.equal(await data.locator("summary .index").first().evaluate(node=>getComputedStyle(node).color),await data.locator(".count").first().evaluate(node=>getComputedStyle(node).color),"array positions use muted count styling");
 await data.getByRole("button",{name:"Copy JSON",exact:true}).click();const copiedText=await page.evaluate(()=>navigator.clipboard.readText()),copied=JSON.parse(copiedText);
 assert.equal(copied.groups[0].conflicts.length,38);
 const downloadEvent=page.waitForEvent("download");await data.getByRole("button",{name:"Download",exact:true}).click();const download=await downloadEvent;
 assert.equal(download.suggestedFilename(),"data.json");assert.equal(await readFile(await download.path(),"utf8"),copiedText,"Download and Copy preserve the same complete bytes");
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),"no horizontal page overflow");
 const tree=data.getByRole("region",{name:"JSON data tree"});assert.ok(await tree.evaluate(node=>{node.scrollTop=node.scrollHeight;return node.scrollTop+node.clientHeight>=node.scrollHeight-1;}),"bottom reachable");
 await tree.evaluate(node=>{node.scrollTop=0;});await page.mouse.move(5,5);
 await page.screenshot({path:`${out}/${name}-expanded.png`,fullPage:true});
 // A large scalar is visually capped too, without losing one byte from Copy or Download.
 const scalar=all.nth(8);await scalar.locator("summary").click();await checkClamp(page,scalar);
 await scalar.getByRole("button",{name:"Copy JSON",exact:true}).click();const fullScalar=await page.evaluate(()=>navigator.clipboard.readText());
 assert.equal(fullScalar,JSON.stringify("LONG_STRING_SENTINEL "+"x".repeat(1600)));
 const scalarDownload=page.waitForEvent("download");await scalar.getByRole("button",{name:"Download",exact:true}).click();assert.equal(await readFile(await (await scalarDownload).path(),"utf8"),fullScalar);
 assert.deepEqual(errors,[]);console.log(`${name}: typed file suppression, quiet small data, focused Copy, unquoted array positions, 3-line strings, byte-exact copy/download, lazy keyboard tree and scroll passed`);
 await context.close();
}
// A different data.json remains visible, and the filtered card still opens its original media position.
const unrelated=await browser.newPage();await unrelated.goto("http://localhost:5173/test/workflow-data.html?unrelated");
const first=unrelated.locator(".post").first();await first.locator(".file").waitFor();assert.equal(await first.locator(".file").count(),1);await first.locator(".file").click();
assert.equal(await unrelated.locator('output[aria-label="Opened attachment"]').innerText(),"1","unrelated data.json opens original attachment index");
await unrelated.close();
const context=await browser.newContext();const page=await context.newPage();
const huge={conflicts:Array.from({length:1000},(_,i)=>({file:`conflict-${i}.go`,reason:"Both branches changed it"}))};const hugeText=JSON.stringify(huge);
let requests=0;await page.route("**/v1/files/"+"b".repeat(64),route=>{requests++;return requests===1?route.fulfill({status:500,body:"unavailable"}):route.fulfill({contentType:"application/json",body:hugeText});});
await page.goto("http://localhost:5173/test/workflow-data.html?reference");
const data=page.getByRole("region",{name:"Workflow data"});await data.locator("summary").waitFor();assert.equal(requests,0,"reference not fetched while collapsed");
await data.locator("summary").click();await data.getByRole("button",{name:"Retry"}).click();await data.getByText('"conflicts"',{exact:true}).click();
await data.locator("summary .index").filter({hasText:/^49$/}).waitFor();assert.equal(await data.locator("summary .index").filter({hasText:/^50$/}).count(),0,"wide collection paged");
await data.getByRole("button",{name:/Show next 50/}).click();await data.locator("summary .index").filter({hasText:/^99$/}).waitFor();assert.ok(await data.locator("details").count()<110,"no full thousand-item expansion");
await page.getByRole("button",{name:"Refresh same post"}).click();await page.evaluate(()=>new Promise(requestAnimationFrame));
assert.equal(await data.getByRole("region",{name:"JSON data tree"}).count(),1,"same file reference survives a refreshed transcript row");assert.equal(requests,2,"refresh does not refetch immutable data");
assert.equal(await data.locator("summary .index").filter({hasText:/^99$/}).count(),1,"refresh retains expanded paging state");
const downloading=page.waitForEvent("download");await data.getByRole("button",{name:"Download",exact:true}).click();assert.equal(await readFile(await (await downloading).path(),"utf8"),hugeText,"reference download preserves original bytes for all1000 entries");
console.log("unrelated data.json mapping and oversized transcript lazy fetch/retry/paging/exact download passed");
await context.close();await browser.close();
