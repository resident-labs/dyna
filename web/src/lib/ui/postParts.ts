import type { Attachment } from '../api/room.ts'
import { userText } from '../sessions/userText.ts'

// Every view that shows a post splits it the same way: the text a person
// reads and the files to show as cards. A stored post names its files in
// attachments. Text that an agent received has Dyna's file list inside it, so
// the list becomes the cards and its agent-only lines stay hidden.
export function postParts(post?: { text?: string; attachments?: Attachment[] } | null) {
  const t = userText(post?.text)
  const stored = post?.attachments?.length ? post.attachments : []
  return { text: t.text, attachments: stored.length ? stored : t.items, names: t.files }
}
