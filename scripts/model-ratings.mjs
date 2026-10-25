// model-ratings.mjs suggests Dyna profile ratings from Artificial Analysis
// pages, relative to a baseline model. It is a personal, subjective tool;
// Dyna itself never reads its output.
//
//   node scripts/model-ratings.mjs [--baseline claude-opus-5-5] <slug|url> ...
//
// For each model it opens the Artificial Analysis comparison page against the
// baseline in the Dyna browser and reads both models at their top reasoning
// level. Suggested ratings:
//   intelligence = 10 * avg(AutomationBench, Terminal-Bench) / baseline avg
//   thrift (API) = 4 + log3(baseline cost / cost), so the baseline is 4 and
//                  each 3x cheaper is +1
//   speed        = 5 + log2(baseline time / time), so the baseline is 5 and
//                  each 2x faster is +1
// All three are rounded and clamped to 1..10. Missing data prints "n/a" and
// the report names the field or column that was not found.
import { execFileSync } from 'node:child_process';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { num, pickColumns, slugOf } from './model-ratings-columns.mjs';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const dyna = (...args) => execFileSync('dyna', args, { encoding: 'utf8', cwd: repo });

const args = process.argv.slice(2);
let baseline = 'claude-opus-5-5';
const models = [];
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--baseline') baseline = args[++i];
  else models.push(args[i]);
}
if (!models.length) {
  console.error('usage: node scripts/model-ratings.mjs [--baseline <slug>] <slug|url> ...');
  process.exit(2);
}

const TRAITS = ['Truthful', 'Admits Uncertainty', 'Flags Nuances', 'Sycophantic', 'Constructive Pushback', 'Slop'];
const TABS = ['Transparency', 'Conviction', 'Slop'];

const [endpoint, importLine] = dyna('browser').split('\n');
const { chromium } = await import(JSON.parse(importLine.match(/import\((.*)\)/)[1]));
const browser = await chromium.connectOverCDP(endpoint);
const page = await browser.contexts()[0].newPage();

// The comparison table has one column per model and reasoning level, sorted
// by score, and one row per metric. read(label) returns [model, baseline]
// values for a row, each from that model's top-scoring column.
async function table(slug) {
  const rows = await page.evaluate(() => {
    const t = document.querySelector('table');
    return t ? [...t.rows].map((r) => [...r.cells].map((c) => c.innerText.trim().replace(/\s+/g, ' '))) : [];
  });
  if (!rows.length) return { read: () => [null, null], missing: ['the comparison table'] };
  const head = rows[0];
  const byLabel = new Map(rows.slice(1).map((r) => [r[0], r]));
  const index = byLabel.get('Intelligence Index') || [];
  const { model: mine, baseline: base } = pickColumns(head, index, slug, baseline);
  const missing = [];
  if (mine < 0) missing.push(`the column for ${slug}`);
  if (base < 0) missing.push(`the baseline column for ${baseline}`);
  return {
    missing,
    read: (label) => {
      const r = [...byLabel.entries()].find(([k]) => k.startsWith(label))?.[1];
      return r ? [num(r[mine]), num(r[base])] : [null, null];
    },
  };
}

async function read(slug) {
  for (const kind of ['releases/comparisons', 'comparisons']) {
    const url = `https://artificialanalysis.ai/models/${kind}/${slug}-vs-${baseline}`;
    const res = await page.goto(url, { waitUntil: 'domcontentloaded' }).catch(() => null);
    if (!res || res.status() >= 400) continue;
    await page.waitForTimeout(2500);
    const t = await table(slug);
    const out = {
      url,
      intelligence: t.read('Intelligence Index'),
      automation: t.read('AutomationBench'),
      terminal: t.read('Terminal-Bench'),
      cost: t.read('Cost per Task'),
      time: t.read('Time per Task'),
      traits: {},
      missing: [...t.missing],
    };
    // Name each metric row that was not found, so an n/a cell says why.
    for (const [name, pair] of [
      ['AutomationBench', out.automation],
      ['Terminal-Bench', out.terminal],
      ['Cost per Task', out.cost],
      ['Time per Task', out.time],
    ]) {
      if (!t.missing.length && (pair[0] == null || pair[1] == null)) out.missing.push(name);
    }
    for (const tab of TABS) {
      const btn = page.getByRole('button', { name: tab, exact: true }).or(page.getByRole('tab', { name: tab, exact: true })).first();
      if (!(await btn.click({ timeout: 4000 }).then(() => true, () => false))) continue;
      await page.waitForTimeout(1000);
      const box = await page.evaluate(() => {
        let el = [...document.querySelectorAll('div,span,h2,h3,h4')].find((e) => /PersonalityBench:/.test(e.textContent || '') && e.textContent.length < 200);
        for (let i = 0; i < 6 && el && el.parentElement; i++) el = el.parentElement;
        return (el?.innerText || '').replace(/\s+/g, ' ');
      });
      for (const t of TRAITS) {
        const m = box.match(new RegExp(`${t} (\\d+)% (\\d+)%`));
        if (m) out.traits[t] = Number(m[1]);
      }
    }
    return out;
  }
  return null;
}

const clamp = (n) => (Number.isFinite(n) ? Math.min(10, Math.max(1, Math.round(n))) : null);
const avg = (a, b) => (a != null && b != null ? (a + b) / 2 : null);
const show = (v, unit = '') => (v == null ? 'n/a' : `${v}${unit}`);

const rows = [];
const notes = [];
for (const arg of models) {
  const slug = slugOf(arg);
  const d = await read(slug);
  if (!d) {
    rows.push(`| ${slug} | no comparison page | | | | | | | |`);
    notes.push(`${slug}: no comparison page for the baseline ${baseline}`);
    continue;
  }
  if (d.missing.length) notes.push(`${slug}: ${d.missing.join(', ')}`);
  const mine = avg(d.automation[0], d.terminal[0]);
  const base = avg(d.automation[1], d.terminal[1]);
  const intelligence = mine && base ? clamp((10 * mine) / base) : null;
  const thrift = d.cost[0] && d.cost[1] ? clamp(4 + Math.log(d.cost[1] / d.cost[0]) / Math.log(3)) : null;
  const speed = d.time[0] && d.time[1] ? clamp(5 + Math.log(d.time[1] / d.time[0]) / Math.log(2)) : null;
  const traits = TRAITS.map((t) => show(d.traits[t], '%')).join(' | ');
  rows.push(`| ${slug} | ${show(d.intelligence[0])} | ${show(mine?.toFixed(1), '%')} | ${show(d.cost[0], ' $')} | ${show(d.time[0], ' s')} | ${show(intelligence)} | ${show(thrift)} | ${show(speed)} | ${traits} |`);
}
await page.close();
await browser.close();
dyna('browser', 'stop');

console.log(`Baseline: ${baseline} (intelligence 10, thrift 4, speed 5). Personality columns: chance the model shows the trait more than the baseline.\n`);
console.log(`| model | AA index | agentic avg | cost/task | time/task | intelligence | thrift (API) | speed | ${TRAITS.join(' | ')} |`);
console.log(`|${' --- |'.repeat(8 + TRAITS.length)}`);
console.log(rows.join('\n'));
if (notes.length) console.log(`\nFields that were not found (those cells print n/a):\n${notes.map((n) => `- ${n}`).join('\n')}`);
console.log(`
Thrift (API) uses Artificial Analysis' pay-per-token cost. Subscription plans (Claude Max, ChatGPT, coding plans) cost by quota, not tokens, so set real thrift from the plan you pay for.
Ratings are suggestions. Taste and diagnostics are judgment calls; the personality columns inform them but do not compute them.`);
