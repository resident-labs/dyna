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
