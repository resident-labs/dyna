# Trial box start measurements

Measured on 2026-10-31 on the hermeslab trial engine. The host had a 20 CPU Intel Core i7-12700K and 30 GiB of memory. Other trial work could run at the same time. These are observations, not a performance guarantee.

| Measure | Runs | Median | Worst |
| --- | ---: | ---: | ---: |
| First observed start after an image tag change | 3 | 96.361 s | 142.771 s |
| Image build within those starts | 3 | 20.749 s | 22.681 s |
| Warm session start to box ready | 5 | 3.498 s | 3.642 s |
| Warm session start to first complete assistant transcript entry | 5 | 7.322 s | 18.548 s |
| Eight-way fan-out to last box ready | 5 batches | 15.844 s | 17.763 s |

## Method and limits

All box times use the trial store's `events.at` timestamps. Start is `session.started`; ready is `box.changed` to `ready`. The warm runs used the cached `resident-labs/dyna` image and the haiku profile in the existing QA room. Each disposable session received a short request and was closed with `--discard`. The first assistant entry is read from the Pi JSONL transcript. It is a complete entry, **not** the first streamed model token. The store does not retain `turn.activity`; that event reports tool activity, not tokens.

The cold rows use the first observed `image.built` event for each distinct tag in the existing trial history. They occurred on 2026-10-30 and 2026-10-31 UTC. Each was followed by the ready event for the single box then creating in that room. This yields only three samples, not the requested five. The first observed tag is not proof that no earlier build existed outside this store. Build time is included in session-to-ready; the separate build row uses `image.built.seconds`. Creating-to-ready for those three runs was 142.549, 78.868 and 96.357 seconds. Historical runs used the trial profile and may have had different host load or image contents.

Each fan-out batch launched eight session-start calls concurrently. The interval runs from the earliest `session.started` event to the last ready event. The five intervals were 15.844, 17.763, 16.559, 12.102 and 12.344 seconds. The first batch's start events spanned 3.230 seconds; the other four spanned at most 0.054 seconds. Thus the first interval excludes time before the first stored start and includes delayed admission of later calls.

The latest `creating` event in each batch occurred 1.234 to 3.237 seconds after the earliest start. Last-ready followed that latest admission by 9.683 to 15.566 seconds. Admission adds delay, but it was not the dominant part of these observed batch times; box creation and startup overlapped and took longer. This does not isolate the causes within creation or prove that admission will not bottleneck larger batches.

Raw event times and transcript-entry timestamps accompany the measurement report. No model output text is included.

## Where the cold start goes

Measured on 2026-11-01 on pve, on private Podman stores, away from CI and from hermeslab. This host is an unprivileged LXC container on a RaidZ2 ZFS pool with Podman 5.4.2. Its load average stayed between 110 and 265, and its full IO pressure near 50%. Treat every time here as a statement about this host: the shape carries over to hermeslab, the seconds do not.

A cold start has three costs, and `image.built` reports only the second:

| Cost | Hermeslab | pve |
| --- | ---: | ---: |
| Base build, outside `image.built` | not measured | 228.786 s |
| Toolchain build | 20.749 s median | not measured |
| First container of an image with the box mapping | not measured | see below |

Boxes run with `--userns=keep-id:uid=1000,gid=1000`. The first container of an image with that mapping makes Podman chown a copy of the whole image, in `storage-chown-by-maps`, and it holds the store lock while it does. On pve that took:

| Image | Files | First create |
| --- | ---: | ---: |
| `alpine:3.20` with one layer added, seven runs | 89 | 19.682 to 46.054 s |
| `node:22` slim, with two other chowns running | 5,774 | 1608.614 s |
| The real `dyna-base`, 1.25 GB | 32,368 | unfinished after 3261 s, stopped |

Hermeslab reports overlay on ext4, native overlay diff, `Supports shifting: false` and no fuse-overlayfs, the same as pve's ZFS store, so the first create should chown there too. That comes from the store status, not from a timing. So the 75 s that `image.built` does not explain is base build plus chown, and **their split on hermeslab is still unknown**. The step logs below give it at the next real tag change. Pve's times do not bound it: 89 files took 20 s or more here.

`force_mask` and `ignore_chown_errors` do not avoid this: the first needs fuse-overlayfs and the second squashes every user to one. An overlay `volatile` mount was slower in all three pairs tested (136, 56 and 47 s against 30, 35 and 20 s on `alpine`).

## Step logs

Each box start now logs one line per step, always on. Engine lines go to the engine log, for example `box box_1: repositories took 1.204s`, and cover the repository fetch and push, instructions, binary, base and toolchain images, slice, forward, container create and start. The box agent's lines cover hello, clone, checkout and Pi start. Those go to the container's log: read them with `podman logs`.

## Results of the startup work

Measured on pve under the conditions above. **No box-level run exists for any row here.** A warm `dyna-base` could not be made on this pool in useful time, so the session-to-ready rows at the top of this file have no after figure.

### Base tag churn

The tag hashes the Containerfile and `pi.tgz`. The same commit packed twice gave a byte-identical tarball, so builds are reproducible. But the tarball held the whole `third_party/pi` tree hash, so a change that shipped nothing still changed it.

Replaying the 13 commits on main that changed a base input since 2026-10-29: all 13 made a new tag. Three of them shipped nothing new. One more returned to content still among the last two tags. So the build id alone takes 13 builds to 10, and keeping two tags takes that to 9. The other 9 are real Pi or recipe changes, and only building ahead helps with those. This counts commits, not hermeslab's deploys, so it does not say which of the nine tags seen there were needless.

| Pack of one tree, then a change to it | Tarball | Build id |
| --- | --- | --- |
| Baseline | `61e9b822d5b8b229` | `9c4cfb3c4d74` |
| Docs only, in `DYNA.md` | same | same |
| A test file | same | same |
| One line of source | `13c81a17df6011a7` | `3fd683d6e6e5` |

Those tarballs through the real `BuildBase`, with a small recipe in place of `dyna-base`:

| Deploy | Time | Images |
| --- | ---: | ---: |
| First | 51.783 s | 1 |
| Docs only | 0.093 s | 1, no build |
| A test file | 0.052 s | 1, no build |
| One line of source | 23.871 s | 2 |

### Warming an image

After `podman rm` of the dummy, the image keeps its mapped layer in Podman's own record, and a box with the same mapping adds no second copy. On the `node` image, the first create took 1608.614 s and the next, after the dummy was gone, 1.657 s.

While a warm of a second image ran under a 60 s limit:

| Measure | Idle | During the warm |
| --- | ---: | ---: |
| A running box writing every 100 ms | | longest gap 102 ms over 1,115 lines |
| `podman ps -a` | 0.037 s | still queued at 20 s |
| `podman images` | | still queued at 20 s |
| `podman exec` into that box | 3.127 s | returned at 43.685 s, about 24 s past its own 20 s limit |

So a warm does not touch what running boxes do, and heartbeats make no Podman call. It stalls Podman itself: creates, wakes, stops, the owner's terminal and the CI pool's listing on that host wait for it. Status quo does the same at an unplanned moment, with no limit.

The limit holds with some slack. The 60 s warm ended at 91.551 s with exit 137: `SIGTERM` did nothing, and `timeout -k 5` then needed longer to unwind. Afterwards no helper was left, `podman ps` answered in 0.074 s and no container remained. The store stayed sound: a create of that image then made the full copy in 991.216 s, the next took 0.165 s, and a real run passed. The engine's limit is 120 s, so expect up to about 150 s on a disk this busy. A warm that misses the limit is not retried, and that image's first box makes the copy, on top of the time already spent. Hermeslab's whole cold start took 78.868 to 142.549 s including its builds, so 120 s should be enough there, but that is an inference. `image.built` seconds now include the warm.

### Eight-way fan-out

Real GitHub fetches and real SSH pushes through `Mirror.Prepare`, with the reconciler's limit of four at a time, an image context read and a hello read, old mirror and new alternating. Each round is six batches, on tmpfs to keep the pool out of a network measure.

| Round | Old median | New median |
| ---: | ---: | ---: |
| 1 | 7.229 s | 3.369 s |
| 2 | 7.223 s | 3.440 s |
| 3 | 7.018 s | 3.451 s |

This is the repository part only. It shows the mirror's share of a fan-out halves. It does not show the 15.844 s box-level figure falls by the same amount.

### runc and crun

crun stays out. On the host's own tools, 200 alternating runs of one rootless bundle on tmpfs, so with neither Podman nor the pool involved: runc 1.1.15 median 27.6 ms, crun 1.30.1 median 13.1 ms. That saves 14.5 ms, about 0.4% of a 3.5 s warm start, which is no clear win, and a change of runtime on every host is not free.

Through Podman the same comparison says nothing here. Nineteen runs of each had medians of 5.4 s for runc and 12.9 s for crun, with runs from 3.5 to 43.8 s. IO noise that size hides a difference of milliseconds, so neither figure favours either runtime. Those 38 runs did all pass with the box mapping. `box-needs` and grants were not tried with crun, since it is not adopted.
