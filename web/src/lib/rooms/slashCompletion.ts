import type { SlashCommand } from '../../api.ts'

export function commandOptions(commands: SlashCommand[]) {
  return commands.filter((c) => c.source === 'skill' || c.source === 'mcp-prompt').map((c) => ({
    value: '/' + c.name,
    group: c.source === 'skill' ? 'Skills' : 'MCP prompts',
    arg: c.source === 'skill' ? 'message' : c.args.map((arg) => arg.name).join(', '),
    hint: c.description,
  })).sort((a, b) => (a.group === b.group ? 0 : a.group === 'Skills' ? -1 : 1))
}

export function matchCommand(commands: SlashCommand[], draft: string) {
  const item = commands.reduce<SlashCommand | undefined>((best, c) => {
    const prefix = '/' + c.name
    if (!draft.startsWith(prefix) || (draft.length > prefix.length && !/\s/.test(draft.charAt(prefix.length)))) return best
    return !best || c.name.length > best.name.length ? c : best
  }, undefined)
  return item ? { item, text: draft.slice(item.name.length + 1).trim() } : null
}
