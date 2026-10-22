// The composer mention choices: one hint
// per choice, and the prefix match both lists use.
export function mentionOptions(choices) {
  return choices.map((c) => ({ value: c, hint: c === '@root' ? 'external orchestrators watching this room' : c === '@team' ? 'your top-level sessions' : 'member' }))
}

export function matchMentions(options, text, limit = 8) {
  const q = text.toLowerCase()
  return options.filter((o) => o.value.toLowerCase().startsWith(q)).slice(0, limit)
}

// Live member items contain the open roster needed for mention choices.
export function memberChoices(members) {
  return ['@team', '@root', ...members.filter((member) => !member.owner && !member.closed && member.name !== 'root' && member.name !== 'team').map((member) => '@' + member.name).sort()]
}
