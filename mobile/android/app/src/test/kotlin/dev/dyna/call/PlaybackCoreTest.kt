package dev.dyna.call

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import java.util.concurrent.TimeUnit
import org.junit.Test

class PlaybackCoreTest {
    private class FakeSink : Sink {
        val written = ArrayList<Int>() // sizes of chunks that reached the speaker
        var head = 0L
        var room = Int.MAX_VALUE // bytes the speaker takes per call
        val tries = java.util.concurrent.Semaphore(0)
        override fun write(pcm: ByteArray, offset: Int, length: Int): Int {
            tries.release()
            val n = minOf(room, length)
            if (n > 0) written.add(n)
            return n
        }
        override fun head() = head
        override fun flush() { head = 0 }
    }

    private val sink = FakeSink()
    private val sent = ArrayList<String>()
    private val core = PlaybackCore(sink, 1000) { f ->
        val m = JSONObject(f)
        sent.add("${m.getString("state")} ${m.getString("id")} ${m.getLong("played_ms")}")
    }

    private fun pcm(frames: Int) = ByteArray(frames * 2)

    @Test fun chunkHeldByWriterDuringClearIsNotPlayed() {
        core.play(pcm(100), "a")
        val held = core.next() // the writer took the chunk, then the owner talks over it
        core.clear()
        core.write(held)
        assertEquals(emptyList<Int>(), sink.written)
        core.play(pcm(10), "b")
        core.write(core.next())
        assertEquals(listOf(20), sink.written)
    }

    @Test fun clearGetsInWhileAWriteWaitsForRoom() {
        sink.room = 0 // the speaker is full
        core.play(pcm(100), "a")
        val held = core.next()
        val writer = Thread { core.write(held) }.apply { start() }
        assertTrue(sink.tries.tryAcquire(1, TimeUnit.SECONDS)) // the writer is waiting for room
        core.clear() // must not wait for the writer
        writer.join(1000)
        assertFalse(writer.isAlive)
        assertEquals(emptyList<Int>(), sink.written)
    }

    @Test fun closingInterruptsAWriterWaitingForRoomWithoutAnError() {
        sink.room = 0
        core.play(pcm(100), "a")
        val held = core.next()
        var failure: Throwable? = null
        val writer = Thread { core.write(held) }.apply { setUncaughtExceptionHandler { _, e -> failure = e }; start() }
        assertTrue(sink.tries.tryAcquire(1, TimeUnit.SECONDS))
        writer.interrupt()
        writer.join(1000)
        assertFalse(writer.isAlive)
        assertEquals(null, failure)
    }

    @Test fun partialWritesContinueWhereTheyStopped() {
        sink.room = 60
        core.play(pcm(100), "a") // 200 bytes
        core.write(core.next())
        assertEquals(listOf(60, 60, 60, 20), sink.written)
    }

    @Test fun positionFromBeforeClearCannotAcknowledgeNextResponse() {
        core.play(pcm(100), "a")
        sink.head = 50
        core.tick()
        core.clear()
        core.play(pcm(10), "b"); core.done("b")
        sink.head = 0 // flushed speaker: b has not played
        core.tick()
        assertEquals(listOf("started a 0", "ended a 50"), sent) // nothing for b: it has not played
    }

    @Test fun staleCallbackOfReplacedCallDoesNothing() {
        val active = Active<String>()
        var ran = 0
        active.set("old")
        val queued = { active.ifCurrent("old") { ran++ } } // the old close, queued on the UI thread
        active.set("new")
        queued()
        assertEquals(0, ran)
        active.ifCurrent("new") { ran++ }
        assertEquals(1, ran)
    }
}
