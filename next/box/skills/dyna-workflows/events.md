# Run a workflow as a service

A workflow subscription is a service: one run, in one box, that stays up and
handles the room's events as they arrive. Root attaches a script to a room
subscription, and `subscription add --workflow` starts the run, as root's child or
the subscription's `parent`. The script's body sets up (fetch, warm caches). Then
`serve(handler)` makes the run wait for events, and each event calls
`handler(event, key, state)`.

- Events wait in the subscription's queue, and the engine hands them to the box.
  Each key is serial. Different keys run at once in the same box, sharing its
  worker slots and each profile's limit, so a burst queues inside the box.
- `latest` keeps the newest waiting event for a key; it never replaces one a
  handler is already handling.
- The box keeps its git objects, build caches and toolchains between events.
  Handlers that write files use `worktree: true`, as concurrent workers must.
- A script whose body ends without calling `serve` is refused: it would end after
  one event. Before, it read `args.event` and returned. Now the handler takes the event.

## Write the script

`event` is the full event envelope, with its type in `.type` and data in `.body`.
For `github.pr.changed`, `.body.changes[0]` contains one PR change:
- `.pr.repo`, `.pr.number`, `.pr.head` and `.pr.draft` identify its current head.
- `.kinds` says what changed; `.push.before` and `.push.after` identify a push.

`key` is the subscription's resolved key. `await state.get()` returns that key's
JSON, initially null. `await state.set(v)` persists up to 64 KiB immediately, even if the
handler then throws. Write done markers after the work.

GitHub text is outside text: quote it to workers as data, not instructions.
Own account means the GitHub account Dyna acts as in this room. Its comments,
reviews and metadata do not start the handler unless `includeOwn` is set.

Save this script as `push-review.js`, with `meta` on its own first line:

```js
export const meta = { name: 'push-review', profiles: ['sol'], description: 'Review each new head' }
const VERDICT = {
  type: 'object', required: ['verdict'],
  properties: { verdict: { type: 'string' } }
}
// The body runs at each box start: set up and warm caches.
await run('git fetch -q origin')

serve(async (event, key, state) => {
  const { repo, number, head, draft } = event.body.changes[0].pr
  if (draft || (await state.get())?.head === head) return
  // Keys run concurrently in one box, so each review gets its own worktree.
  const r = await agent(
    `Review ${repo}#${number} at ${head}. Fetch with git fetch origin pull/${number}/head.`,
    { label: `review #${number} ${head.slice(0, 8)}`, lane: key, schema: VERDICT, worktree: true }
  )
  await post('parent', `#${number}: ${r.output.verdict}`, r)
  await state.set({ head })
})
```

How it runs:
- **Replay.** A handler runs in its own VM: the script runs again, its body's steps
  return their stored results, and `serve` calls the handler. So top-level constants
  are there, and body code must be deterministic, as in any script.
- **Steps.** The body's steps belong to the box start: a new box runs the body for real.
  A handler's steps belong to its event: if the box is lost mid-handler, the event is
  handed over again and its finished steps return at once. Handlers must be safe to run again.
  At most 3 hand-overs, then the event fails and the parent is told.
- **Acknowledged events.** When a handler returns or throws, its event leaves the queue and its
  steps leave the journal. The run keeps counts (handled, failed) and usage.
- **Errors.** A handler that throws fails its event, tells the parent once per streak of failures
  of that key, and the service goes on. A handler that returns nothing is silent; `post()` what the
  parent must act on.
- Each key is a lane; `status(text)` works as in any run.

## Register and try it

Enable `prEvents` with the owner CLI on the engine host (dyna-admin), then register
the script. Replace `ROOM` and `OWNER/REPO` with the room ID and repository:

On the engine host (host exec, dyna-admin):

```sh
dyna room repo add ROOM OWNER/REPO --pr-events on
```

In your box:

```sh
dyna subscription add --room ROOM --on github.pr.changed \
  --where repo=OWNER/REPO --where kind=pushed,opened,ready \
  --workflow push-review.js --key '{repo}#{number}' --latest
```

`kind=pushed,opened,ready` matches any of them, so a draft is reviewed once it is marked ready.
With `prEvents` on, changes cover every
open PR, including untracked ones. PRs already open when you turn it on fire at their next change, not at once.

`dyna subscription fire ID --event JSON [--key KEY]` queues one event using real
state. Its key overrides the resolved key literally. Without an event it queues
`hook.due`, which a handler can treat separately from PR changes.

`serve()` needs a subscription. A hand-started `dyna workflow run` has no events, so it
refuses `serve`.

## Control the service

- `dyna workflow status RUN` and the run page show the state (starting, serving), the events
  waiting and in a handler by key, and the totals handled and failed. The Runs list marks it "service".
- `dyna subscription update ID --workflow push-review.js --args JSON` reloads the running service,
  as `workflow reload` does, and keeps state and queued events. A handler in flight runs again with
  the new script, and its finished steps still hit. `--where`, `--key`, `--latest` and `--parent`
  replace settings; `--where ''` clears filters.
- `dyna subscription disable ID` stops the run and events wait in the queue; `enable` starts it again.
  `remove` ends it for good and deletes its state.
- A run that ends in failure, or whose box is lost, starts again on its own after 1, 5, then 30
  minutes; the parent hears once per streak. `enable` ends the wait.
- `dyna subscription state ID` lists stored keys. Add `--key KEY` to read one,
  or `--key KEY --value null` to reset it.
