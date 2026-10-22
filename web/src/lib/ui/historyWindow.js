// Keep a few pages on either side of the reader, then release the far end.
export function trimHistoryRows(rows, keep, older = false) {
 if (rows.length <= keep * 2) return { rows, before: 0, after: 0 }
 const dropped = rows.length - keep
 return older
  ? { rows: rows.slice(0,keep), before: 0, after: dropped }
  : { rows: rows.slice(dropped), before: dropped, after: 0 }
}
