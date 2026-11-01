# next/web

The v2 web app. To check a change on the trial's real data before merge, run `bash trial-view.sh` on the trial host; it serves this tree read only, as a device named Web QA, to the browser that opens the link in `~/.config/dyna-next/web-qa.url`.
After the Web QA device is revoked, delete `~/.config/dyna-next/web-qa.cookie` on the trial host, and the next run pairs again.

## Fixture and width check

Run `dyna-webdev -fixture full` for invented accounts, rated profiles and posts. Run `dyna-webdev -fixture empty` for one room, no posts, no accounts and unrated profiles. Build the web app before the server, so the server embeds the current assets.

From the repository root, set `DYNA_WEBDEV_BIN` to an absolute output path, then run `bash scripts/ci.sh next-web-width-build` and `bash scripts/ci.sh next-web-width`. The check needs Node 24, Go and a non-snap browser. It prefers an installed Playwright headless shell, then Google Chrome. Set `DYNA_CHROMIUM` to select another browser. It checks four widths with both fixtures. `known-width-failures.ts` lists existing clipped elements by fixture, route, width and text. Remove entries as fixes land. The check fails on new failures and on entries that no longer match.
