// The named themes. Every color on screen comes from one token set: a token missing from a theme is a compile
// error. v1 follows the OS in CSS and has a dark-only terminal; v2 has named themes and opens on Carbon.
export type ThemeId = "carbon" | "oled" | "ink" | "nord" | "light" | "paper";
export type ThemeMode = "dark" | "light";
export type Token =
  // Surfaces, then the washes and lines drawn over them.
  | "--bg" | "--sidebar" | "--raised" | "--line" | "--selected" | "--hover" | "--track"
  // --firm is a firmer line for inputs and outlined buttons, at 14% of the theme's wash.
  | "--firm"
  // --focus is the one accent, for focus rings, selection and links.
  | "--mention-bg" | "--mention-bar" | "--focus" | "--text" | "--muted" | "--attention" | "--attention-fill" | "--danger" | "--success"
  // Identity colors: the hue comes from the name, these set lightness and chroma per theme.
  | "--id-name" | "--id-face" | "--id-alpha" | "--id-initials"
  | "--shadow-sm" | "--shadow-md" | "--shadow-lg" | "--scrim"
  // A remote desktop is near-black whatever the theme, as a display is, so these never change with it.
  | "--screen" | "--screen-chip" | "--screen-text" | "--screen-muted" | "--screen-line" | "--screen-hover" | "--screen-focus"
  | "--screen-id-face" | "--screen-id-alpha" | "--screen-id-initials"
  // A QR code must stay dark on white to scan, so every theme holds the same pair.
  | "--qr-ink" | "--qr-paper";
// Black, red, green, yellow, blue, magenta, cyan and white, then the bright set.
type Ansi = readonly [string, string, string, string, string, string, string, string, string, string, string, string, string, string, string, string];

export interface Theme {
  readonly id: ThemeId;
  readonly label: string;
  readonly mode: ThemeMode;
  readonly tokens: Readonly<Record<Token, string>>;
  readonly terminal: { readonly ansi: Ansi; readonly selection: string };
}

// A flat low alpha on every hue, because solid faces turn warm hues brown.
const glass = { "--id-name": "80% 0.12", "--id-face": "72% 0.13", "--id-alpha": "10%", "--id-initials": "84% 0.1" };
// A shadow needs more alpha to show on a dark page.
const darkShadows = { "--shadow-sm": "rgb(0 0 0 / 24%)", "--shadow-md": "rgb(0 0 0 / 36%)", "--shadow-lg": "rgb(0 0 0 / 44%)", "--scrim": "rgb(0 0 0 / 50%)" };
const lightShadows = { "--shadow-sm": "rgb(0 0 0 / 12%)", "--shadow-md": "rgb(0 0 0 / 18%)", "--shadow-lg": "rgb(0 0 0 / 28%)", "--scrim": "rgb(0 0 0 / 50%)" };
const qr = { "--qr-ink": "#151416", "--qr-paper": "#ffffff" };
const screen = { "--screen": "#0d0d0f", "--screen-chip": "rgb(20 20 22 / 82%)", "--screen-text": "#e8e9eb", "--screen-muted": "#9c9fa6",
  "--screen-line": "rgb(255 255 255 / 8%)", "--screen-hover": "rgb(255 255 255 / 4%)", "--screen-focus": "#93a6f5",
  "--screen-id-face": glass["--id-face"], "--screen-id-alpha": glass["--id-alpha"], "--screen-id-initials": glass["--id-initials"] };
// Black stays readable on a dark page, and white on a light one, as most programs print those on the page. The cost is
// text on a colored background: on Light, white on a color falls to about 1.0 to 1.3:1, as in other light terminal themes.
const darkTerminal = {
  ansi: ["#54575e", "#ef8a80", "#9ccf9a", "#e8c27a", "#8fa8f0", "#d3a6ef", "#8cc8e3", "#d8d9dc",
    "#7b7f87", "#f4a59c", "#b4dcb2", "#f0d39c", "#bccbff", "#e6c6f7", "#b0d9ee", "#f1f2f4"],
  selection: "rgb(255 255 255 / 22%)",
} as const;

const lightTerminal = {
  ansi: ["#24262b", "#b3332e", "#3d7a3a", "#8a6100", "#3d5bd0", "#8a3fae", "#1f7a8c", "#6f737c",
    "#5f636c", "#c9463f", "#4c8f48", "#a37300", "#5571db", "#a050c2", "#2a8ea3", "#8a8e96"],
  selection: "rgb(61 91 208 / 22%)",
} as const;

export const carbon: Theme = {
  id: "carbon", label: "Carbon", mode: "dark",
  tokens: { "--bg": "#131416", "--sidebar": "#0c0d0e", "--raised": "#1b1c1f", "--line": "rgb(255 255 255 / 8%)", "--firm": "rgb(255 255 255 / 14%)", "--selected": "rgb(255 255 255 / 9%)",
    "--hover": "rgb(255 255 255 / 4%)", "--track": "rgb(255 255 255 / 8%)", "--focus": "#93a6f5", "--text": "#e8e9eb", "--muted": "#9c9fa6",
    "--mention-bg": "rgb(239 182 109 / 10%)", "--mention-bar": "#efb66d", "--attention": "#efb66d", "--attention-fill": "#efb66d", "--danger": "#ec8480", "--success": "#86c79b", ...glass, ...darkShadows, ...screen, ...qr },
  terminal: darkTerminal,
};

export const themes: readonly Theme[] = [
  carbon,
  {
    id: "oled", label: "OLED black", mode: "dark",
    tokens: { "--bg": "#000000", "--sidebar": "#000000", "--raised": "#121315", "--line": "rgb(255 255 255 / 13%)", "--firm": "rgb(255 255 255 / 14%)", "--selected": "rgb(255 255 255 / 12%)",
      "--hover": "rgb(255 255 255 / 7%)", "--track": "rgb(255 255 255 / 8%)", "--focus": "#9db0f7", "--text": "#ececee", "--muted": "#a0a2a9",
      "--mention-bg": "rgb(242 187 116 / 10%)", "--mention-bar": "#f2bb74", "--attention": "#f2bb74", "--attention-fill": "#f2bb74", "--danger": "#f08b87", "--success": "#8ccda1", ...glass, ...darkShadows, ...screen, ...qr },
    terminal: darkTerminal,
  },
  {
    id: "ink", label: "Ink", mode: "dark",
    tokens: { "--bg": "#0e1117", "--sidebar": "#090c11", "--raised": "#161b23", "--line": "rgb(196 212 255 / 9%)", "--firm": "rgb(196 212 255 / 14%)", "--selected": "rgb(196 212 255 / 10%)",
      "--hover": "rgb(196 212 255 / 5%)", "--track": "rgb(196 212 255 / 8%)", "--focus": "#8fb0ff", "--text": "#e6ebf3", "--muted": "#97a0ae",
      "--mention-bg": "rgb(239 182 109 / 10%)", "--mention-bar": "#efb66d", "--attention": "#efb66d", "--attention-fill": "#efb66d", "--danger": "#ef8a86", "--success": "#7fcba0", ...glass, ...darkShadows, ...screen, ...qr },
    terminal: darkTerminal,
  },
  {
    id: "nord", label: "Nord", mode: "dark",
    tokens: { "--bg": "#262b35", "--sidebar": "#20242d", "--raised": "#313745", "--line": "rgb(236 239 244 / 11%)", "--firm": "rgb(236 239 244 / 14%)", "--selected": "rgb(236 239 244 / 11%)",
      "--hover": "rgb(236 239 244 / 6%)", "--track": "rgb(236 239 244 / 8%)", "--focus": "#88c0d0", "--text": "#f4f6f9", "--muted": "#b4bccb",
      "--mention-bg": "rgb(235 203 139 / 10%)", "--mention-bar": "#ebcb8b", "--attention": "#ebcb8b", "--attention-fill": "#ebcb8b", "--danger": "#f0a0a7", "--success": "#a3be8c", ...glass, ...darkShadows, ...screen, ...qr },
    terminal: darkTerminal,
  },
  {
    id: "light", label: "Light", mode: "light",
    tokens: { "--bg": "#fafafb", "--sidebar": "#f1f2f4", "--raised": "#ffffff", "--line": "rgb(20 24 34 / 11%)", "--firm": "rgb(20 24 34 / 14%)", "--selected": "rgb(20 24 34 / 8%)",
      "--hover": "rgb(20 24 34 / 4%)", "--track": "rgb(20 24 34 / 8%)", "--focus": "#3d5bd0", "--text": "#24262b", "--muted": "#5f636c",
      "--mention-bg": "rgb(164 85 0 / 8%)", "--mention-bar": "#a45500", "--attention": "#a45500", "--attention-fill": "#c27412", "--danger": "#b3332e", "--success": "#2f7d4a",
      "--id-name": "47% 0.13", "--id-face": "62% 0.13", "--id-alpha": "12%", "--id-initials": "38% 0.11", ...lightShadows, ...screen, ...qr },
    terminal: lightTerminal,
  },
  {
    id: "paper", label: "Paper", mode: "light",
    tokens: { "--bg": "#fbf9f6", "--sidebar": "#f3efe9", "--raised": "#ffffff", "--line": "rgb(60 40 20 / 12%)", "--firm": "rgb(60 40 20 / 14%)", "--selected": "rgb(60 40 20 / 8%)",
      "--hover": "rgb(60 40 20 / 4%)", "--track": "rgb(60 40 20 / 8%)", "--focus": "#3d5bd0", "--text": "#2a2723", "--muted": "#63605a",
      "--mention-bg": "rgb(164 85 0 / 8%)", "--mention-bar": "#a45500", "--attention": "#a45500", "--attention-fill": "#c27412", "--danger": "#b3332e", "--success": "#2e7b49",
      "--id-name": "47% 0.13", "--id-face": "62% 0.13", "--id-alpha": "12%", "--id-initials": "38% 0.11", ...lightShadows, ...screen, ...qr },
    terminal: lightTerminal,
  },
];

// The stored id is the one unknown value: anything but a known id is Carbon. A browser that blocks site storage
// throws on the read, and the app must still paint, so that is Carbon too.
export function themeFor(read: () => string | null): Theme {
  let stored: string | null = null;
  try { stored = read(); } catch { /* Storage is blocked: no stored id. */ }
  return themes.find(item => item.id === stored) ?? carbon;
}
