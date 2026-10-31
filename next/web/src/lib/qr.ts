// QR codes drawn in the page, as in v1, so a pairing link never goes to a QR service.
import qrcode from "qrcode-generator";

// An SVG path of the dark modules on a size x size grid, with no quiet zone; the tile's padding is that zone.
export function qrPath(text: string): { size: number; d: string } {
  const qr = qrcode(0, "M");
  qr.addData(text);
  qr.make();
  const size = qr.getModuleCount();
  let d = "";
  for (let row = 0; row < size; row++) {
    for (let column = 0; column < size; column++) {
      if (!qr.isDark(row, column)) continue;
      // Join a run of dark modules in a row into one rectangle.
      let width = 1;
      while (column + width < size && qr.isDark(row, column + width)) width++;
      d += `M${column} ${row}h${width}v1h-${width}z`;
      column += width - 1;
    }
  }
  return { size, d };
}
