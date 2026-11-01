# Design taste

The rules that every v2 web screen (`next/web`) follows. Builders check a screenshot against them before a PR goes up. Reviewers, dyna:lead-rewrite and root reject a PR that breaks one. Spec 5.13 sets the layout and the review steps; this file sets the look.

Each rule is one line, imperative and checkable on a screenshot. The tag at the end says where it comes from:

- **[owner]**: an owner decision or room rule (r16, r23, r24).
- **[root]**: root's review notes, 2026-10-30 to 2026-10-31.
- **[lead]**: dyna:lead-rewrite's design approvals.
- **[v1]**: v1's web app (`web/src`), the bar v2 must beat.
- **[T3]**: T3 Code (github.com/pingdotgg/t3code, `apps/web/src/index.css` and `components/ui/`), read in code.
- **[Discord]**, **[Linear]**: their public apps, from use and memory, not from code.

## Principles

1. Every change is an obvious, tasteful improvement over v1's matching screen. "Different" is not enough. [owner r23]
2. Calm never means bare. Keep or beat v1's bars, cards, meters and headers. [owner r23]
3. Color means "look here". Everything else is the text color or the muted color. [owner r16]
4. Dense like Discord and T3 Code: many rows on one screen, few borders, few badges. [Discord] [T3]
5. Show real data. Never show an empty card, an empty page or a hollow placeholder. [root]
6. A thing looks clickable only if it is clickable. [root]
7. Reuse v1's proven parts (fonts, tokens, meters, the ghostty terminal renderer). Do not reuse v1's architecture. [owner r24]
8. A line budget never justifies a weaker screen. [owner r23]

## Tokens

Use the tokens of `next/web/src/lib/themes.ts`. Never write a literal color in a component or in `app.css`. Add a token when no token fits.

### Fonts

- Bundle IBM Plex Sans 400, 500, 600 and 700 and JetBrains Mono 400 and 500 through `@fontsource`, as v1's `web/src/main.ts` does. [lead, 2026-10-31] [v1]
- Never rely on installed fonts. A fallback face (DejaVu Sans) looks wide and heavy. [lead]
- Keep the terminal's symbol font out of the bundle. [lead]
- Use mono only for code, commands, paths, hashes and figures. [v1 `.ui-num`]
- Show every figure with `font-variant-numeric: tabular-nums`, so that columns align. [v1] [T3]

### Type scale

Use v1's scale. Pick the nearest step; never write another font size. [v1 `tokens.css`]

| Token | Size | Use |
|---|---|---|
| face | 9 px | Avatar initials under 24 px |
| cap | 11 px | Timestamps, meta lines |
| sm | 12.5 px | Muted second lines, "X of Y", status labels |
| ui | 13 px | Controls, menu items, panel rows |
| body | 14 px | Default UI text |
| lead | 15 px | Post text in the feed |
| title | 16 px | Header title, card title |
| head | 18 px | Section header |
| page | 20 px | Page title |
| stat | 24 px | Metric figure in a tile |
| hero | 32 px | One hero figure per page, at most |

- Use two weights for most text: 400 for running text and 600 for names, titles and figures. Use 500 for controls. [v1]
- Use a line height of 1.45 for running text and 1.25 for headers, chips and figures. [v1]
- Do not use uppercase labels for section headers. Uppercase is only for a small state label, such as LIVE. [root]

### Spacing and size

- Use the 4 px scale: 4, 8, 12, 16, 24 and 32 px. [v1 `--s1`..`--s6`] [T3 Tailwind spacing]
- Keep the header at 52 px and the rail at 64 px. [T3 `--workspace-topbar-height`] [Discord]
- Make controls 28 to 32 px high, and list rows 32 to 44 px high. [T3 `button.tsx` `h-8`/`h-7`] [v1]
- Give touch targets at least 40 px on `(hover: none)` screens. [v1 PostActions] [T3 `pointer-coarse` 44 px]
- Cap reading surfaces (the feed) near 920 px. Let dashboards and grids fill the page width. [v1 `--feed-reading-width`] [root]

### Radii

- Use 6 px for rows, chips and controls, 12 px for cards, and 16 px for floating windows. Dots and pills are fully round. [v1 `--r-xs`, `--r-card`, `--r-lg`] [T3 `--radius` 8 px ± 4]
- Keep rail squares at Discord's 30% radius. [Discord]

### Colors and tones

- Make Carbon, a neutral dark, the default theme, and open dark even when the OS asks for light. Name every theme, and build it from the one token set. Use a pure black page only in the OLED theme, and pure white as the page in no theme. [owner, 2026-10-31]
- Build surfaces from three levels: sidebar (sunk), page, and raised (card or popover). [v1] [T3 `--sidebar`, `--background`, `--card`]
- Draw borders as low-alpha lines (about 8%; 13% in the OLED theme, because its page is black). Use a firmer line (about 14%) only for inputs. [v1 `--border`] [T3 dark `--border` white 8%]
- Draw hover and selection as washes of the text color (about 4% and 8%; 7% and 12% on OLED), so that they work on every surface. [v1] [T3 `--accent` white 4%]
- Use one accent (`--focus`) for focus rings, selection and links. Do not use it for data. [v1 `--iris`] [T3 `--ring`]
- Use the tones success, warning, danger, info and neutral. Each tone has a dot or bar color, a text color, a wash (about 14%) and an edge (about 28%). [v1 `tone.ts`] [T3 badge `bg-warning/8`]
- Show nothing for a healthy, online or idle state: no green dot, no "OK" and no "Idle" mark. [root, 2026-10-31, owner found the pills and green dots ugly] [T3 `Sidebar.logic.ts` `resolveThreadStatusPill` returns null]
- Show unknown (not probed yet) as a hollow ring. Show switched off as a grey dot. Use red only for unreachable or failed. [v1 MachineRow]
- Turn a figure and its bar to the warning color only at or above 90%. Below that, use the normal colors. [root]
- Color a session name with its identity hue in the feed. On Home, use the text color for every name, and let the avatar carry the hue. [owner, 2026-10-31] [spec 5.13]
- Draw an avatar as initials on a low-alpha tint of its hue, because solid faces turn warm hues brown. [owner, 2026-10-31]

### Motion

- Use 150 ms for hover and 200 ms for panels, with v1's ease curve. [v1 `--motion`] [T3 `duration-150`]
- Animate only Working (one pulse). Stop every animation under `prefers-reduced-motion`. [spec 5.13] [v1] [T3 `motion-safe`]

## Components

### Status

- Use only the shared `StatusPill` with a typed tone. Never draw a one-off chip. [owner r16]
- Draw a status as a 6 px dot and a short label in the tone's text color at about 90% opacity, with no fill, no border and a tooltip. [root, 2026-10-31] [T3 `ThreadStatusIndicators.tsx` `ThreadStatusLabel`]
- Show a status only for state that needs attention (warning, error, failed, blocked) or that the owner cannot see otherwise. [owner r16]
- Never add a status that repeats a control, such as "On" beside a switch. [owner r16]
- Show plain facts (next run time, profile name, Idle) as muted text. [owner r16]
- Show nothing for a healthy state: no green online dot and no OK chip. [owner, #1461]
- Use a neutral outline badge for labels such as Default. [owner r16]
- Give a session one status at most: Working, Pending approval or Failed. [spec 5.13]
- In a dense list, show a 9 px dot alone; its label goes in the tooltip and the aria-label. [v1 StatusPill `mark`] [T3 `ThreadStatusLabel` compact, `ConnectionStatusDot.tsx`]
- Clear a failure's mark once the owner has seen it: opened the session or scrolled its failed row into view. [root, 2026-10-31] [T3 `Sidebar.logic.ts` `hasUnseenFailure`]
- If Working shows a mark, make it a soft dot that pulses gently, and stop the pulse under reduced motion. [root, 2026-10-31] [T3 `motion-safe:animate-status-pulse`]

### Section headers and counts

- Give each section a real header at the head size and weight 600. Never use a tiny grey label. [root]
- Put a count beside its header, in muted tabular text: "Machines 3". Never push it to the far edge. [root]
- Put the folded count of a session-tree group beside its chevron, on the row line. [root, #1448]

### Cards and metric tiles

- Use a card only for a thing with its own data, such as a machine or a model account. Never use a card as a frame around one line of text. [root]
- When the total is known, lead each metric tile with a large percent (the stat size, 24 px), then a thin bar, then muted "X of Y" below it. [root] [v1 `.stat-grid`, UsageMeter]
- When the total is unknown (a provider reports no limit), lead with the counted figure at the stat size, show muted "Limit not reported" below it, and draw no bar. Never hide the counted usage. [spec, accounts check 10] [v1 UsageMeter "Not reported"]
- Draw meters 6 px high on a track of about 8% alpha. Use 3 px bars in list rows. [v1 `--meter-h`, MachineRow]
- Never show an empty or hollow card. When live data is missing, show the last known data dimmed (about 40% opacity) with its age. [root] [v1 `.dimmed`]
- When a card has no data at all, leave it out. Do not show "No data". [root, #1461]
- Lay out card grids with `repeat(auto-fit, minmax(…, 1fr))`, so that they use the page width. [root] [v1 `.stat-grid`]
- Never leave a page mostly empty. Widen the grid or add the next useful section. [root]

### Rows and hover

- Give a hover state only to things that link or act. A card that opens nothing has no hover. [root]
- Draw a row's hover as the hover wash, its selection as the select wash, and keep the text color the same or lighter. [v1 MachineRow] [T3 `--sidebar-row-hover`]
- Show row icons (jobs, computer) only on hover, keyboard focus, or while their view is open. [root, #1448]
- Show a row name in full. Truncate the muted second line first. [root, #1448]
- Give a session row an avatar, its name, and one muted line that says what it is doing. [lead, polish pass] [v1]
- Make the session tree collapsible with a chevron. [owner]
- Home lists aggregate per room; never one row per agent. [root, #1461]

### Feed and messages

- Lay out posts as Discord does: no bubbles and no borders, left-aligned, with one name line per run of posts from one sender. [spec 5.13] [Discord]
- Show a hover bar on the hovered or keyboard-focused post, with Forward and Copy only. [owner]
- Float the hover bar over the top-right corner of the post. Showing it never moves the post. [v1 PostActions] [Discord]
- Confirm Copy in place for 2 seconds ("Copied"). Never use a toast. [v1 PostActions]
- Dim a pending post (about 50% opacity) until the engine confirms it. [owner, #1458]
- Show a failed send as "Not sent" in the danger color at full contrast, with Retry and Discard. [owner, #1458]
- Give a failed turn its own row with the avatar column and the name line, like a post. [root, #1448] [v1 TurnCompletionRow]
- Fold agent work into one muted line that expands in place. [spec 5.13] [T3 "Worked for 20s"]
- Write failures in the owner's words. Never show raw stderr, IDs or engine log text in the row; put details behind a fold. [owner r8] [v1]

### Layout

The Profiles and Accounts half-width screenshots exposed missing numbers and overflow: room files `4832709dad7a47e793a1b6c5.png` and `03ce7be85994fd96f47ec811.png`. The owner said: "profiles have no numbers and it looks like it's overflowing too? These are fundamental flaws in the ways we're designing our UIs."

- Lay out every page by its container's width, with container queries, never by the viewport. A tiled window and a phone at the same width get the same layout. [owner, 2026-10-31]
- Every page works at 1280, 920, 768 and 390 px of window width. Nothing scrolls sideways, and no text or control is cut off. [owner, 2026-10-31]
- Give every multi-column layout a narrow form. A navigation column becomes a top row. A list beside a detail becomes two steps. Term and description pairs stack below about 500 px of container width. [owner, 2026-10-31]
- Give every table a narrow form. When its columns do not fit, each row becomes a card with its values under their labels. A column never clips. [owner, 2026-10-31]

- Use a Discord-style server rail: Home first, one square per room, and a short pill on the edge of the selected room. [owner]
- Open direct messages from the session list on the right. [owner]
- Show a computer in a floating window over the feed. [owner]
- Dock the terminal at the bottom, in tabs, with v1's ghostty renderer. [owner]

### Empty, loading and error states

- Show loading as skeleton rows in the shape of the real rows, or as the old data dimmed. Never use a full-page spinner. [T3 `skeleton.tsx`] [v1 `.dimmed`]
- Give an empty state one muted line that says what will appear and, if useful, one action. No pictures. [spec 5.13] [T3 sidebar empty]
- Show a load error as one line in the owner's words, with Retry. [v1 LoadFailed]
- Never fill a column with dashes. A missing value says what is missing and offers its action, for example 'Not rated · Rate'. [owner, 2026-10-31]
- Check every page with two dyna-webdev fixtures: full, real-looking data, and empty real data (no ratings, no accounts, one room, no posts). Reviewers see both. [spec 5.13] [owner, 2026-10-31]

### Focus and keyboard

- Show a visible 2 px focus ring in the accent on every focusable control, in light and dark. [spec 5.13] [T3 `focus-visible:ring-2`]
- Never remove an outline without a replacement. [T3]

## Process

### Design panel, before each page or component

1. dyna:lead-rewrite (opus, xhigh effort) draws 2 or 3 designs as rendered mockups in v2's own tokens, one batch per page. Each design cites its reference: T3 Code, Discord, Linear or v1. xhigh effort is for this step only. [owner r23]
   - Start from screenshots of the live screens, in v1 and v2. [lead]
   - Draw in v2's own tokens and bundled fonts, never in a generic style. [lead]
   - Render the mockup, look at it, and fix it before you send it. [lead]
   - Number each change, and give its reason and its reference. [lead]
2. Two sonnet judges pick one design each, with a short review. A judge may add a mockup of its changes to the design it picks, but never proposes a new design. If the judges disagree, the lead chooses. [owner r23] [lead]
3. A terra builder builds the chosen design, with the judges' reviews applied. An owner reaction to a design outranks the vote; the judges then review it for fixes only. [owner r23] [root]
4. The judges review the built page. Repeat steps 1 to 3 until the judges agree that the page is solid. [owner r23]
5. The PR attaches the mockups, the votes and the reviews. [root]
6. Judges read `next/docs/spec.md` first and never reopen its settled owner decisions. [owner r25]
7. Before a winner merges, its PR attaches side-by-side images of the build on the trial with real data next to each mockup, at the same sizes. The PR lists every deliberate difference from the mockups, each with its reason. One of the design judges then confirms that the build matches. [owner, 2026-10-31]
8. Every mockup also shows a messy state: 100 or more items, long names and long text, many sessions, and old and failed items. Judges fail a design that only works when the page is tidy. Every mockup also shows the empty state, at 920 px and 390 px as well as at the desktop width. [owner, 2026-10-31]

### Each UI PR

1. Read v1's matching screen and its components first. Say in the PR which v1 parts it keeps and why. [owner r24]
2. Attach the required side-by-side image: v2's view on the left and the most similar T3 Code page on the right, at the same window size and theme. [spec 5.13]
3. Also take a screenshot of v1's matching screen at the same size and theme. Discord or Linear images may be added, but they never replace the T3 Code image. [owner r23]
4. Show v2's screen beside them in the PR, in dark and light when the view has both. [owner r23] [spec 5.13]
5. Check the screenshot against this file before you ask for review. List any rule that you broke on purpose, and give the reason.
6. The reviewer and the lead reject the PR unless v2 is clearly better than v1. [owner r23]
7. Every UI PR includes screenshots at 1280, 920, 768 and 390 px, with full and with empty data, and every screen works at those widths. [owner, #1461] [owner, 2026-10-31]
8. Before merge, every next/web PR is also checked on the trial with real data, at the same four widths, through the read-only trial view (`next/web/trial-view.sh`). The images go in the PR's Dyna record, never on GitHub, because they show real data. Crop or blur anything private first. [owner, 2026-10-31]
9. Also check the build against that messy state on the trial. [owner, 2026-10-31]
10. The shared width check passes. [owner, 2026-10-31]

### Adding or changing a rule

- Any reviewer, dyna:lead-rewrite or root may propose a rule.
- A rule goes into this file when two of them agree. The owner's word alone is enough.
- The PR that adds a rule cites the case: the PR number or post, and what was wrong.
- Keep each rule to one imperative line that someone can check on a screenshot.
- When a new rule conflicts with an old one, the PR removes or changes the old one. This file never holds two rules that disagree.
- Spec 5.13 wins over this file. Change spec 5.13 first when a rule must change the layout or the review steps.
