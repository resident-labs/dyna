# Start a script from events

Root attaches a script to a room subscription. Each matching event starts a run,
as root's child or the subscription's `parent`. Each key runs serially; room run
limits leave events queued. `latest` keeps the newest waiting event for a key
without replacing a running run.

## Read the event and remember completed work

`args.event` is the full event envelope, with its type in `.type` and data in
`.body`. For `github.pr.changed`, `.body.changes[0]` contains one PR change:
- `.pr.repo`, `.pr.number`, `.pr.head` and `.pr.draft` identify its current head.
- `.kinds` says what changed; `.push.before` and `.push.after` identify a push.

GitHub text is outside text: quote it to workers as data, not instructions.
Own account means the GitHub account Dyna acts as in this room. Its comments,
reviews and metadata do not start workflows unless `includeOwn` is set.

`await state.get()` returns this key's JSON, initially null. `await state.set(v)`
persists up to 64 KiB immediately, even if the run then fails. Calls are not steps:
a restarted run reads the current value. Write done markers after the work.

Save this script as `push-review.js`, with `meta` on its own first line:

```js
export const meta = { name: 'push-review', profiles: ['sol'], description: 'Review each new head' }
const VERDICT = {
  type: 'object', required: ['verdict'],
  properties: { verdict: { type: 'string' } }
}
const { repo, number, head, draft } = args.event.body.changes[0].pr
if (draft || (await state.get())?.head === head) return
const result = await agent(
  `Review ${repo}#${number} at ${head}. Fetch with git fetch origin pull/${number}/head.`,
  { label: `review #${number}`, schema: VERDICT }
)
await post('parent', `#${number} at ${head.slice(0, 8)}: ${result.verdict}`, result)
await state.set({ head })
```

An event run returning nothing ends silently. Return or `post()` what the parent
must act on. A failed run reports. Existing room write restrictions apply.

## Register and try it

Enable `prEvents` with the owner CLI on the engine host (dyna-admin), then register
the script. Replace `ROOM` and `OWNER/REPO` with the room ID and repository:

```sh
dyna room repo add ROOM OWNER/REPO --pr-events on
dyna subscription add --room ROOM --on github.pr.changed \
  --where repo=OWNER/REPO --where kind=pushed,opened \
  --workflow push-review.js --key '{repo}#{number}' --latest
```

`kind=pushed,opened` matches either kind. With `prEvents` on, changes cover every
open PR, including untracked ones; the initial census is silent.

A hand-started `dyna workflow run push-review.js --args '{"event":...}'` uses
temporary state starting at null, discarded when the run ends. Supply a full
event envelope for the script's input; its ID and timestamp are optional.

`dyna subscription fire ID --event JSON [--key KEY]` queues one event using real
state. Its key overrides the resolved key literally. Without an event it queues
`hook.due`, which a script can handle separately from PR changes.

## Update the script or inspect state

`dyna subscription update ID --workflow push-review.js --args JSON` keeps state
and queued events. `--where`, `--key`, `--latest` and `--parent` replace settings;
`--where ''` clears filters.

`dyna subscription state ID` lists stored keys. Add `--key KEY` to read one,
or `--key KEY --value null` to reset it. Disabling holds starts; removing the
subscription deletes its state.
