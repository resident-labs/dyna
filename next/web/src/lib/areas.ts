// Which area F6 moves to (spec 5.13): the next one after the area that holds the focus, or the one before with back.
// Areas nest, as a Settings list sits inside the Settings section, so the innermost one that holds it counts. With
// none holding it, F6 goes to the first and Shift+F6 to the last.
export function nextArea(areas: readonly { contains(node: Node | null): boolean }[], active: Node | null, back: boolean): number {
  const index = areas.findLastIndex(area => area.contains(active));
  if (index < 0) return back ? areas.length - 1 : 0;
  return (index + (back ? areas.length - 1 : 1)) % areas.length;
}
