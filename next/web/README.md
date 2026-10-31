# next/web

The v2 web app. To check a change on the trial's real data before merge, run `bash trial-view.sh` on the trial host; it serves this tree read only, as a device named Web QA.
After the Web QA device is revoked, delete `~/.config/dyna-next/web-qa.cookie` on the trial host, and the next run pairs again.
