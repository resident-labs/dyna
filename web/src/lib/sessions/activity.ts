// activityLabel is what a live session is doing, in one line a person reads.
// It keeps the step and drops the MCP server prefix from a tool ID, so
// "mcp__dyna__shell: Read guide command" reads "Read guide command". The
// session view and the members list share it.
const shellTools = new Set(['shell', 'shell_output', 'shell_stop', 'shell_jobs', 'bash', 'command', 'command_execution', 'run_command', 'run_terminal_cmd'])

// A file tool whose arguments are unreadable still names what it does.
const fileSteps = new Map([
  ['read', 'reading a file'], ['read_file', 'reading a file'], ['view_file', 'reading a file'],
  ['edit', 'editing a file'], ['multiedit', 'editing a file'], ['edit_file', 'editing a file'], ['apply_patch', 'editing a file'], ['file_change', 'editing a file'],
  ['write', 'writing a file'], ['write_file', 'writing a file'],
])

export function activityLabel(text: string | undefined): string {
  const [first = ''] = (text ?? '').split('\n')
  const line = first.trim()
  if (!line) return ''
  // mcp__<server>__<tool> is one tool ID in the agent's spelling.
  const bare = line.replace(/^mcp__[a-z0-9_.-]+__/i, '')
  const sep = bare.indexOf(': ')
  if (sep < 0) return bare
  const tool = bare.slice(0, sep)
  const step = bare.slice(sep + 2).trim()
  // Arguments that no harness made readable stay out of the label.
  const readable = step.startsWith('{') || step.startsWith('[') ? '' : step
  // A shell's description or command is already the step by itself.
  if (shellTools.has(tool.toLowerCase())) return readable || 'running a command'
  if (!readable) return fileSteps.get(tool.toLowerCase()) ?? tool
  return `${tool}: ${readable}`
}
