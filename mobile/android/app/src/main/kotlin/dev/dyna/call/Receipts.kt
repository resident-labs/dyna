package dev.dyna.call

/**
 * Tracks what the speaker has played of each response and reports it to the engine as
 * playback receipts, as web/src/lib/voice/audio.js does. The engine holds the next
 * announcement until the earlier one ended, and counts "started" as heard.
 *
 * Positions are in frames queued to the speaker since it last started or was flushed;
 * the caller passes the speaker's head position (frames played so far). The speaker plays
 * responses in order, so a response starts when the head passes its first frame and
 * ends once its audio is complete and the head passes its last. Thread-safe.
 */
class Receipts(private val rate: Int, private val send: (String) -> Unit) {
    private class Item(val id: String, val first: Long) {
        var end = first
        var done = false
        var started = false
    }

    private val items = ArrayList<Item>()
    private var queued = 0L

    /** Notes [frames] more frames of response [id], queued to the speaker. */
    @Synchronized fun queue(id: String, frames: Int) {
        val item = items.lastOrNull { it.id == id } ?: Item(id, queued).also { items.add(it) }
        queued += frames
        item.end = queued
    }

    /** The engine sent all of response [id]. A response with no audio sends nothing. */
    @Synchronized fun done(id: String) {
        items.lastOrNull { it.id == id }?.done = true
    }

    /** Reports the receipts the speaker's [head] position has earned. */
    @Synchronized fun advance(head: Long) {
        while (items.isNotEmpty()) {
            val item = items[0]
            if (!item.started && head > item.first) {
                item.started = true
                send(playbackFrame(item.id, "started"))
            }
            if (!(item.started && item.done && head >= item.end)) break
            send(playbackFrame(item.id, "ended", ms(item.end - item.first)))
            items.removeAt(0)
        }
    }

    /**
     * The speaker was stopped at [head] and flushed (barge-in). Every response ends: what
     * was heard of a started one, and 0 ms for one that never started. Positions restart at 0.
     */
    @Synchronized fun clear(head: Long) {
        for (item in items) {
            if (item.started) send(playbackFrame(item.id, "ended", ms(minOf(item.end, head) - item.first)))
            else send(playbackFrame(item.id, "ended", 0))
        }
        items.clear()
        queued = 0
    }

    private fun ms(frames: Long) = maxOf(0L, frames) * 1000 / rate
}
