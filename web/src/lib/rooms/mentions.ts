// The composer mention choices: one hint
// per choice, and the prefix match both lists use.
export interface MentionOption {
  value: string
  hint: string
}

// The fields the mention list reads from a member.
export interface MentionMember {
  name: string
  owner?: boolean
  closed?: boolean
}

export function mentionOptions(choices: readonly string[]): MentionOption[] {
  return choices.map((c) => ({ value: c, hint: c === '@root' ? 'external orchestrators watching this room' : c === '@team' ? 'your top-level sessions' : 'member' }))
}

export function matchMentions(options: readonly MentionOption[], text: string, limit = 8): MentionOption[] {
  const q = text.toLowerCase()
  return options.filter((o) => o.value.toLowerCase().startsWith(q)).slice(0, limit)
}

// Live member items contain the open roster needed for mention choices.
export function memberChoices(members: readonly MentionMember[]): string[] {
  return ['@team', '@root', ...members.filter((member) => !member.owner && !member.closed && member.name !== 'root' && member.name !== 'team').map((member) => '@' + member.name).sort()]
}
