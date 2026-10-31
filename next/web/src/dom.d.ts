// TypeScript 5.9's DOM types lack focusVisible (Chromium 122, Firefox 104, Safari 18.4).
interface FocusOptions { focusVisible?: boolean }
