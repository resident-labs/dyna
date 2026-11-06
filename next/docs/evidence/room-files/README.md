# Room files evidence

These 24 native-resolution screenshots show the built app in Chromium, dark and light, at 1440 × 1000 and 390 × 1000. The app is a local `dyna-webdev -fixture empty` instance, whose temporary room's shared root points to this Dyna room's real `/box/shared`. The room label and feed are local QA data, not the live owner's feed. Directory listings and file bytes come from the actual engine routes, not mocked responses. No shared file is changed by the check.

The tree is open through `design/pig-spike`. Examples are the real `design/room-files.md`, `design/pig-spike/pigspike.go` and `design/settings-390-pages.png`. The last one is an existing image of a Settings design, displayed as an image file rather than as another app page.

| Theme and width | Tree, two levels open | Markdown | Go | Image | Message path | Attachment modal |
| --- | --- | --- | --- | --- | --- | --- |
| Dark 1440 | [Tree](dark-1440-tree.png) | [Markdown](dark-1440-markdown.png) | [Go](dark-1440-go.png) | [Image](dark-1440-image.png) | [Message](dark-1440-message.png) | [Modal](dark-1440-modal.png) |
| Light 1440 | [Tree](light-1440-tree.png) | [Markdown](light-1440-markdown.png) | [Go](light-1440-go.png) | [Image](light-1440-image.png) | [Message](light-1440-message.png) | [Modal](light-1440-modal.png) |
| Dark 390 | [Tree](dark-390-tree.png) | [Markdown](dark-390-markdown.png) | [Go](dark-390-go.png) | [Image](dark-390-image.png) | [Message](dark-390-message.png) | [Modal](dark-390-modal.png) |
| Light 390 | [Tree](light-390-tree.png) | [Markdown](light-390-markdown.png) | [Go](light-390-go.png) | [Image](light-390-image.png) | [Message](light-390-message.png) | [Modal](light-390-modal.png) |

The same files are in `/box/shared/design/built/room-files/`.

## Reproduce

Build the app and local engine from `next`:

```sh
(cd web && npm ci && npm run build)
go build -o /tmp/dyna-webdev ./cmd/dyna-webdev
/tmp/dyna-webdev -fixture empty > /tmp/room-files-webdev.log 2>&1
```

In another terminal, start Vite in `next/web` for the DOM-level matcher test page:

```sh
npx vite --host 0.0.0.0 --port 5173
```

With Chromium at `/usr/bin/chromium`, install Playwright outside the checkout and run from `next/web` against the freshly started engine:

```sh
npm install --prefix /tmp/room-files-qa playwright
WEBDEV_LOG=/tmp/room-files-webdev.log SHARED_ROOT=/box/shared \
  node test/room-files.browser.mjs \
  /tmp/room-files-qa/node_modules/playwright/index.mjs \
  /box/shared/design/built/room-files
```

The browser check covers the Files entry point, lazy expansion, deep file selection, Copy path, highlighted Go source, image loading, narrow back navigation, unknown paths, browser back and forward, path-link modal opening, Escape closing only the modal, missing markdown/image/HTML/binary recovery links, absence of horizontal overflow and page errors, and the post-render path matcher (inline code, fenced code, existing links, punctuation, escaped ampersands and box-private paths).

Other verification run for this change:

- `go test ./engine/...` from `next`.
- `npm run check`, `npm test` (517 passing tests) and `npm run build` from `next/web`.
- `git diff --check`.

Not checked: Safari, Firefox, screen-reader output, live production feed or a deploy. This is an owner-only read UI with no agent tool or new agent-facing behavior, so no new agent guidance is needed.
