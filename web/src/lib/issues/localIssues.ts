// Pure helpers for the Issues panel. Issues come from roomsvc as
// {id,title,file,line,filer,filedAt,state,reason,goalId,githubUrl,changedAt}.

// The fields the panel's helpers read. Other issue fields pass through.
export interface IssueView {
  id?: string
  file?: string
  line?: number
  state?: string
  goalId?: string
}

// The top folder of an issue's file, for the filter chips.
const area = (i: IssueView): string => (i.file ? i.file.split('/')[0] ?? '' : '')

// Top folders in use, most used first, then by name.
export function areas(list: readonly IssueView[]): string[] {
  const n = new Map<string, number>()
  for (const i of list) if (area(i)) n.set(area(i), (n.get(area(i)) ?? 0) + 1)
  return [...n].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])).map(([k]) => k)
}


// file:line, the file alone when the line is unknown, or empty.
export const location = (i: IssueView): string => (i.file ? (i.line && i.line > 0 ? `${i.file}:${i.line}` : i.file) : '')
