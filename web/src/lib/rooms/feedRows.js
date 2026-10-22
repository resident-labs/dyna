import { parsePRPost } from '../prs/pr.ts'

const senderGap = 7 * 60 * 1000
const shellSender = /^shell:sh_\d{8}-\d{6}-[a-f0-9]{8}$/
const resultKinds = new Set(['completed', 'failed', 'stopped'])

// Automatic `dyna shell` exit records stay out of channel feeds; the
// shell-jobs view lists those jobs. They still wake the job's parent.
export function isShellResult(post) {
  return post.completion === true && resultKinds.has(post.kind) &&
    shellSender.test(post.from) && !post.owner && !post.human && !post.replyTo && !post.attachments?.length
}

// callsInWindow keeps the call requests that belong between the loaded
// posts: a window that stops short of older or newer posts shows no request
// from beyond its edge.
export function callsInWindow(calls, posts, earlier = 0, newer = 0) {
  const first = posts[0]?.ts ?? -Infinity
  const last = posts[posts.length - 1]?.ts ?? Infinity
  return calls.filter((c) => {
    const at = Date.parse(c.askedAt)
    return (!earlier || at >= first) && (!newer || at <= last)
  })
}

// feedRows adds day and "new" dividers, and marks a post that continues the
// previous sender's run. Root and the human share the owner address, so the
// human flag is part of the sender. A call request is a row at the time root
// asked, between the posts.
export function feedRows(posts, unreadFrom = '', calls = []) {
  const out = []
  let prev = null
  let prevDay = ''
  const waiting = calls.map((call) => ({ call, ts: Date.parse(call.askedAt) })).sort((a, b) => a.ts - b.ts)
  const items = []
  for (const post of posts) {
    while (waiting.length && waiting[0].ts <= post.ts) items.push(waiting.shift())
    items.push({ post, ts: post.ts })
  }
  items.push(...waiting)
  for (const { post, call, ts } of items) {
    const day = new Date(ts).toDateString()
    if (day !== prevDay) {
      out.push({ kind: 'day', key: 'd' + ts, ts })
      prevDay = day
      prev = null
    }
    if (call) {
      out.push({ kind: 'call', key: `call:${call.room}/${call.id}`, call })
      prev = null
      continue
    }
    if (post.id === unreadFrom) {
      out.push({ kind: 'new', key: 'new' })
      prev = null
    }
    const gap = prev ? post.ts - prev.ts : Infinity
    const cont = prev && !post.completion && !prev.completion && post.kind !== 'pr' && prev.kind !== 'pr' &&
      !post.replyTo && prev.from === post.from && !!prev.human === !!post.human && gap < senderGap &&
      (prev.to || []).join() === (post.to || []).join()
    const last = out[out.length - 1]
    const pr = post.kind === 'pr' ? parsePRPost(post.text, post.notice) : null
    const plain = (p) => !p.replyTo && !p.forward && !p.attachments?.length
    if (pr?.url && last?.post?.kind === 'pr' && plain(post) && plain(last.post) &&
      parsePRPost(last.post.text, last.post.notice).url === pr.url) {
      last.posts.push(post)
      last.post = post
    } else {
      out.push({ kind: 'post', key: post.id, post, cont, ...(pr ? { posts: [post] } : {}) })
    }
    prev = post
  }
  return out
}

// Use the same parser as the PR screen. Noise counts belong in expanded detail.
export function feedPRDetail(post) {
  const pr = parsePRPost(post.text, post.notice)
  return { ...pr, events: pr.events.filter((event) => event.kind !== 'noise'),
    body: pr.body.replace(/(?<![\w/])([a-f0-9]{40})(?![\w/])/gi, (sha) => sha.slice(0, 7)) }
}

export function feedPRRepos(posts) {
  return new Set(posts.filter((post) => post.kind === 'pr').map((post) => parsePRPost(post.text, post.notice).repo).filter(Boolean))
}

// A generic watcher update must not erase a known review of the same head.
// CI can change in an update, so only review state carries forward.
export function feedPREvents(details) {
  const last = details[details.length - 1]
  if (!last) return []
  if (!last.head || last.events.some((event) => event.kind !== 'update')) return last.events
  for (let i = details.length - 2; i >= 0; i--) {
    if (details[i].head !== last.head) break
    const review = details[i].events.filter((event) => ['approved', 'changes', 'review'].includes(event.kind))
    if (review.length) return review
  }
  return last.events
}
