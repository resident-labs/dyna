// Only supplied cells and blank padding are copied; display-only row numbers
// and generated column labels never enter this representation.
export function tableCSV(rows: readonly (readonly string[])[], width: number): string {
  return rows.map((row) => Array.from({ length: width }, (_, column) => {
    const value = row[column] ?? ''
    return !value || /[",\r\n]/.test(value) ? '"' + value.replace(/"/g, '""') + '"' : value
  }).join(',')).join('\r\n')
}
