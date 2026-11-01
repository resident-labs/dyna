# next/web

The v2 web app. To check a change on the trial's real data before merge, run `bash trial-view.sh` on the trial host; it serves this tree read only, as a device named Web QA, to the browser that opens the link in `~/.config/dyna-next/web-qa.url`.
After the Web QA device is revoked, delete `~/.config/dyna-next/web-qa.cookie` on the trial host, and the next run pairs again.

## Fixtures

Run `dyna-webdev -fixture full` for invented accounts, rated profiles and posts. Run `dyna-webdev -fixture empty` for one room, no posts, no accounts and unrated profiles. Build the web app before the server, so the server embeds the current assets.
