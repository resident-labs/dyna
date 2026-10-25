// Column picking for scripts/model-ratings.mjs, kept on its own so a test can
// check it without a browser or a network.
//
// An Artificial Analysis comparison table has one header cell per model and
// reasoning level and one row per metric. A header names the vendor as well as
// the model, "Anthropic Claude Opus 5.5 (Max, Default Fallback)", so a plain
// slug comparison misses the baseline. The trailing cell "Add model" belongs
// to no model, and every model can hold several columns.

export const toSlug = (name) => name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');

// A bare slug, a model page and a comparison page all name the same model. A
// comparison path holds the baseline after "-vs-", and the page kinds
// "releases" and "comparisons" may stand in front of the slug, so all three
// forms yield the model slug alone.
export const slugOf = (arg) => {
  const text = String(arg ?? '').trim();
  const path = /^https?:\/\//.test(text) ? new URL(text).pathname : text;
  const after = path.match(/\/models\/(?:(?:releases|comparisons)\/)*(.+)$/)?.[1];
  return (after ?? path).replace(/[?#].*$/, '').replace(/\/+$/, '').split('-vs-')[0];
};

// A cell such as "$2.11", "70%" or "845.63s" becomes a number, and anything
// else becomes null so a missing value never counts as zero.
export const num = (cell) => {
  const n = Number(String(cell ?? '').replace(/[$,s%\s]/g, ''));
  return cell && Number.isFinite(n) ? n : null;
};

// True when the header belongs to the model with this slug. The vendor prefix
// may stand in front of the slug, and the reasoning level may follow it.
export const isColumn = (header, slug) => {
  const s = toSlug(String(header ?? '').replace(/\s*\(.*$/, ''));
  return s === slug || s.endsWith(`-${slug}`);
};

// Returns the column index of the model's top reasoning level, by Intelligence
// Index, or -1 when the table holds no column for that model.
const topColumn = (headers, index, slug) => {
  let best = -1;
  for (let c = 0; c < headers.length; c++) {
    if (!isColumn(headers[c], slug)) continue;
    if (best < 0 || (num(index[c]) ?? -1) > (num(index[best]) ?? -1)) best = c;
  }
  return best;
};

// headers is the header row, index the Intelligence Index row, both aligned by
// column. Returns the model and baseline column indexes; -1 means not found.
export const pickColumns = (headers, index, model, baseline) => ({
  model: topColumn(headers, index, model),
  baseline: topColumn(headers, index, baseline),
});
