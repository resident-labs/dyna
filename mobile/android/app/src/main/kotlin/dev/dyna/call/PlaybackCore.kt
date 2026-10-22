package dev.dyna.call

import java.util.concurrent.LinkedBlockingQueue

/**
 * The speaker, as [PlaybackCore] needs it. [write] never blocks: it takes what fits and
 * returns the bytes taken, or a negative number on error. [head] is frames played since
 * the last [flush].
 */
interface Sink {
    fun write(pcm: ByteArray, offset: Int, length: Int): Int
    fun head(): Long
    fun flush()
}

/**
 * The playback logic without Android: queued chunks, receipts, and barge-in. One lock covers
 * every step that touches the speaker or the receipts, so a clear cannot interleave with a
 * write or a position sample. A chunk is tagged with the generation it was queued in, and
 * chunks of an earlier generation are dropped.
 */
/** How long the writer waits for room in the speaker before it tries again. */
private const val WAIT_MS = 5L

class PlaybackCore(private val sink: Sink, rate: Int, send: (String) -> Unit) {
    class Chunk(val generation: Int, val pcm: ByteArray)

    private val lock = Any()
    private val receipts = Receipts(rate, send)
    private val queue = LinkedBlockingQueue<Chunk>()
    private var generation = 0

    fun play(pcm: ByteArray, id: String) = synchronized(lock) {
        receipts.queue(id, pcm.size / 2)
        queue.put(Chunk(generation, pcm))
    }

    fun done(id: String) = receipts.done(id)

    /** Waits for the next chunk. The writer thread calls this, then [write]. */
    fun next(): Chunk = queue.take()

    /**
     * Hands the chunk to the speaker as it has room. The lock is held only for each short
     * write, never while waiting for room, so [clear] and [tick] always get in; a chunk
     * of an earlier generation stops at once.
     */
    fun write(c: Chunk) {
        var offset = 0
        while (offset < c.pcm.size) {
            val n = synchronized(lock) {
                if (c.generation != generation) return
                sink.write(c.pcm, offset, c.pcm.size - offset)
            }
            if (n < 0) return
            if (n == 0) {
                try { Thread.sleep(WAIT_MS) } catch (e: InterruptedException) { return } // the player is closing
            } else offset += n
        }
    }

    /** Reports the receipts the speaker's position has earned. The clock thread calls it. */
    fun tick() = synchronized(lock) { receipts.advance(sink.head()) }

    /** Barge-in: drops queued and unplayed speech and reports what was heard. */
    fun clear() = synchronized(lock) {
        generation++
        queue.clear()
        receipts.clear(sink.head())
        sink.flush()
    }
}

/** The one current call. A callback of a replaced call finds it is not current and does nothing. */
class Active<T : Any> {
    private var current: T? = null

    @Synchronized fun set(call: T?) { current = call }

    @Synchronized fun get(): T? = current

    /** Runs [f] only if [call] is still the current one. */
    @Synchronized fun ifCurrent(call: T, f: () -> Unit) {
        if (current === call) f()
    }
}
