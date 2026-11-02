// Agents write long tokens, and a browser wraps those only where it must: mid-word, at the edge of the line. A token this
// long gets places to wrap, as a shorter one (GitHub) moves to the next line whole.
const long = 16;
const seams = /(?<=[_/\\=&])(?=[^\s_/\\])|(?<=[a-z0-9])(?=[A-Z][a-z])|(?<=[A-Za-z0-9]\.)(?=[A-Za-z])/;

// The text cut at each place it may wrap: after a _ or /, between camelCase words, and at a dot inside a name.
export const wrapParts = (text: string): string[] =>
  text.split(/(\s+)/).flatMap((word, i) => {
    if (i % 2 || word.length < long) return word ? [word] : [];
    return word.split(seams);
  }).reduce<string[]>((parts, part) => {
    // Plain words stay one run, so only a real wrap point becomes a <wbr>.
    const last = parts.at(-1);
    if (last !== undefined && (/\s$/.test(last) || /^\s/.test(part))) parts[parts.length - 1] = last + part;
    else parts.push(part);
    return parts;
  }, []);
