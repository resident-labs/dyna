<script lang="ts" module>
  let flows = 0;
</script>

<script lang="ts">
  import type { WorkflowStep } from "../contract.gen.ts";
  import { count } from "../feed.ts";
  import { FAILED, minutesLabel, runningFor, stepTime, stepTitle, type FlowEdge, type FlowNode } from "../workflow.ts";

  // The run as its process: a node per stage with its counts and its running step, and the moves its lanes made between
  // stages, counted. A row of nodes, with skips arched above and loops back below; a column on a narrow page, with skips on the
  // right and loops on the left. A failed node joins the end only when a step failed. A node opens its steps under the chart.
  let { nodes, edges, failed, laned, now, selected, onselect }: {
    nodes: FlowNode[]; edges: FlowEdge[]; failed: WorkflowStep[]; laned: boolean; now: number; selected?: string; onselect: (key: string) => void;
  } = $props();
  const id = `flow-${++flows}`;
  const GAP = 34, MIN = 112, MAX = 200, HIGH = 112, LEVEL = 16, VGAP = 36;
  let width = $state(0);

  type Placed = { key: string; x: number; y: number; w: number };
  type Arc = FlowEdge & { from_: number; to_: number; side: "adjacent" | "over" | "under"; level: number };
  let order = $derived([...nodes.map(node => node.key), ...failed.length ? [FAILED] : []]);
  let vertical = $derived(width > 0 && (width - (order.length - 1) * GAP) / order.length < MIN);

  // Each edge's side, and a level per side, so arcs that span the same nodes stack instead of crossing.
  let arcs = $derived.by((): Arc[] => {
    const at = new Map(order.map((key, index) => [key, index]));
    const placed: Arc[] = [];
    const sorted = edges.flatMap(edge => {
      const from = at.get(edge.from), to = at.get(edge.to);
      if (from === undefined || to === undefined || from === to) return [];
      const side = edge.to === FAILED || to < from ? "under" : to === from + 1 ? "adjacent" : "over";
      return [{ ...edge, from_: from, to_: to, side, level: 0 } as Arc];
    }).sort((a, b) => Math.abs(a.to_ - a.from_) - Math.abs(b.to_ - b.from_));
    for (const arc of sorted) {
      if (arc.side !== "adjacent") {
        const lo = Math.min(arc.from_, arc.to_), hi = Math.max(arc.from_, arc.to_);
        let level = 1;
        while (placed.some(other => other.side === arc.side && other.level === level && Math.min(other.from_, other.to_) <= hi && Math.max(other.from_, other.to_) >= lo)) level++;
        arc.level = level;
      }
      placed.push(arc);
    }
    return placed;
  });
  let over = $derived(Math.max(0, ...arcs.filter(arc => arc.side === "over").map(arc => arc.level)));
  let under = $derived(Math.max(0, ...arcs.filter(arc => arc.side === "under").map(arc => arc.level)));
  // Room beside the nodes for the arcs on each side and their counts.
  const margin = (levels: number) => levels ? 18 + levels * LEVEL + 10 : 8;

  let layout = $derived.by(() => {
    const n = order.length;
    if (vertical) {
      const left = margin(under), right = margin(over), w = Math.max(80, width - left - right);
      const places: Placed[] = order.map((key, index) => ({ key, x: left, y: 8 + index * (HIGH + VGAP), w }));
      return { places, height: 16 + n * HIGH + (n - 1) * VGAP };
    }
    const top = margin(over), w = Math.min(MAX, (width - (n - 1) * GAP) / n), start = (width - (n * w + (n - 1) * GAP)) / 2;
    const places: Placed[] = order.map((key, index) => ({ key, x: start + index * (w + GAP), y: top, w }));
    return { places, height: top + HIGH + margin(under) };
  });

  // An arc's path and where its count sits.
  function draw(arc: Arc): { d: string; lx: number; ly: number; anchor: "start" | "middle" | "end" } {
    const a = layout.places[arc.from_], b = layout.places[arc.to_], h = 14 + arc.level * LEVEL, nudge = Math.min((arc.level - 1) * 18, 32);
    if (vertical) {
      if (arc.side === "adjacent") {
        const x = a.x + a.w / 2, y1 = a.y + HIGH, y2 = b.y;
        return { d: `M${x} ${y1} L${x} ${y2 - 2}`, lx: x + 8, ly: (y1 + y2) / 2 + 4, anchor: "start" };
      }
      const right = arc.side === "over", x = right ? a.x + a.w : a.x, out = right ? h : -h;
      const y1 = a.y + HIGH * (right ? 0.62 : 0.38) + nudge, y2 = b.y + HIGH * (right ? 0.38 : 0.62) - nudge;
      return { d: `M${x} ${y1} C${x + out} ${y1} ${x + out} ${y2} ${x + (right ? 2 : -2)} ${y2}`, lx: x + out * 0.75 + (right ? 4 : -4), ly: (y1 + y2) / 2 + 4, anchor: right ? "start" : "end" };
    }
    if (arc.side === "adjacent") {
      const y = a.y + HIGH / 2, x1 = a.x + a.w, x2 = b.x;
      return { d: `M${x1} ${y} L${x2 - 2} ${y}`, lx: (x1 + x2) / 2, ly: y - 6, anchor: "middle" };
    }
    const up = arc.side === "over", y = up ? a.y : a.y + HIGH, out = up ? -h : h;
    const x1 = a.x + a.w * (up ? 0.62 : 0.38) + nudge, x2 = b.x + b.w * (up ? 0.38 : 0.62) - nudge;
    return { d: `M${x1} ${y} C${x1} ${y + out} ${x2} ${y + out} ${x2} ${y + (up ? -2 : 2)}`, lx: (x1 + x2) / 2, ly: y + out * 0.75 + (up ? -4 : 12), anchor: "middle" };
  }

  // A node is narrow, so a running step's time is in minutes once it passes one.
  const took = (step: WorkflowStep) => { const ms = runningFor(step.started, step.ended, now); return ms < 60_000 ? stepTime(step, now) : minutesLabel(ms); };
  const title = (key: string) => key === FAILED ? "Failed" : nodes.find(node => node.key === key)?.title ?? key;
  let described = $derived(arcs.map(arc => `${title(arc.from)} to ${title(arc.to)}${arc.count ? `, ${laned && arc.to !== FAILED ? count(arc.count, "lane") : count(arc.count, "step")}` : ""}`));
</script>

<div class="flow" class:vertical bind:clientWidth={width} style:height="{layout.height}px" role="group" aria-label="The run's stages">
  {#if width}
    <svg width={width} height={layout.height} aria-hidden="true">
      <defs>
        <marker id="{id}-head" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path class="head" d="M0 0 L10 5 L0 10 z" /></marker>
        <marker id="{id}-failed" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path class="head failed" d="M0 0 L10 5 L0 10 z" /></marker>
      </defs>
      {#each arcs as arc (`${arc.from}\n${arc.to}`)}
        {@const line = draw(arc)}
        <path class="edge" class:failed={arc.to === FAILED} d={line.d} marker-end="url(#{id}-{arc.to === FAILED ? 'failed' : 'head'})" />
        {#if arc.count}<text class="count" x={line.lx} y={line.ly} text-anchor={line.anchor}>{arc.count}</text>{/if}
      {/each}
    </svg>
    {#each nodes as node, index (node.key)}
      {@const place = layout.places[index]}
      {@const live = node.live[0]}
      <button type="button" class="node" style:left="{place.x}px" style:top="{place.y}px" style:width="{place.w}px" style:height="{HIGH}px" style:--colour={node.colour}
        aria-pressed={selected === node.key} aria-label={`${node.title}: ${node.done} done${node.running ? `, ${node.running} running` : ""}${node.failed ? `, ${node.failed} failed` : ""}. Show its steps`}
        onclick={() => onselect(node.key)}>
        <span class="title"><strong title={node.title}>{node.title}</strong>{#if node.failed}<span class="bad">{node.failed} failed</span>{/if}</span>
        <span class="figure"><b>{node.done}</b> done</span>
        {#if live}<span class="live"><i></i>{node.running} running{#if stepTime(live, now)}{` · ${took(live)}`}{/if}</span><span class="now" title={live.name}>{stepTitle(live)}{#if node.running > 1}{` and ${node.running - 1} more`}{/if}</span>
        {:else}<span class="sub">{node.lanes ? count(node.lanes, "lane") : count(node.steps.length, "step")}</span>{/if}
      </button>
    {/each}
    {#if failed.length}
      {@const place = layout.places[nodes.length]}
      <button type="button" class="node failed" style:left="{place.x}px" style:top="{place.y}px" style:width="{place.w}px" style:height="{HIGH}px"
        aria-pressed={selected === FAILED} aria-label={`${count(failed.length, "step")} failed. Show them`} onclick={() => onselect(FAILED)}>
        <span class="title"><strong>Failed</strong></span>
        <span class="figure"><b>{failed.length}</b> {failed.length === 1 ? "step" : "steps"}</span>
        <span class="now" title={failed.map(step => step.name).join("\n")}>{stepTitle(failed[0])}</span>
        {#if failed.length > 1}<span class="sub">and {failed.length - 1} more</span>{/if}
      </button>
    {/if}
  {/if}
  <ul class="sr">{#each described as line, index (index)}<li>{line}</li>{/each}</ul>
</div>

<style>
  .flow { position: relative; min-width: 0; }
  svg { position: absolute; inset: 0; overflow: visible; pointer-events: none; }
  .edge { fill: none; stroke: var(--firm); stroke-width: 1.5; }
  .edge.failed { stroke: var(--danger); stroke-dasharray: 3 4; opacity: 0.7; }
  .head { fill: var(--muted); }
  .head.failed { fill: var(--danger); }
  .count { fill: var(--muted); font-family: var(--sans); font-size: 11px; font-variant-numeric: tabular-nums; }
  /* A node is a card with its stage's colour as a bar down its left edge. */
  .node { all: unset; box-sizing: border-box; position: absolute; display: flex; flex-direction: column; gap: 1px; min-width: 0; padding: 10px 10px 10px 14px;
    border: 1px solid var(--line); border-radius: 12px; background: var(--raised); color: var(--text); font-size: 12.5px; line-height: 1.3; cursor: pointer;
    transition: background 150ms ease-out, border-color 150ms ease-out; }
  .node::before { content: ""; position: absolute; left: -1px; top: 12px; bottom: 12px; width: 3px; border-radius: 1.5px; background: var(--colour); }
  .node[aria-pressed="true"] { border-color: var(--firm); background: var(--selected); }
  .node:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }
  .node.failed { border-style: dashed; border-color: color-mix(in srgb, var(--danger) 55%, transparent); background: none; }
  .node.failed::before { display: none; }
  .node.failed .title, .node.failed .figure b { color: var(--danger); }
  .title { display: flex; align-items: baseline; gap: 8px; min-width: 0; font-size: 13px; }
  .title strong { min-width: 0; overflow: hidden; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
  .bad { flex: none; margin-left: auto; color: var(--danger); font-size: 12px; font-variant-numeric: tabular-nums; }
  .figure { color: var(--muted); }
  .figure b { margin-right: 2px; color: var(--text); font-size: 24px; font-weight: 600; line-height: 1.2; font-variant-numeric: tabular-nums; }
  .sub { overflow: hidden; color: var(--muted); text-overflow: ellipsis; white-space: nowrap; }
  .live { display: flex; align-items: center; gap: 6px; min-width: 0; overflow: hidden; color: var(--success); white-space: nowrap; }
  .live i { flex: none; width: 6px; height: 6px; border-radius: 50%; background: currentColor; animation: pulse 1.6s ease-in-out infinite; }
  @keyframes pulse { 50% { opacity: 0.35; } }
  .now { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
  /* A column's nodes are wide, so the running step sits beside its count. */
  .vertical .node { display: grid; grid-template-columns: auto minmax(0, 1fr); align-content: center; gap: 2px 12px; }
  .vertical .title { grid-column: 1 / -1; }
  .vertical .figure { grid-row: span 2; align-self: center; }
  .sr { position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  @media (hover: hover) { .node:hover { background: var(--selected); } .node.failed:hover { background: var(--hover); } }
  @media (prefers-reduced-motion: reduce) { .node { transition: none; } .live i { animation: none; } }
</style>
