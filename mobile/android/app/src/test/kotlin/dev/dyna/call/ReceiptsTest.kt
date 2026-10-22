package dev.dyna.call

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test

class ReceiptsTest {
    private val sent = ArrayList<String>()

    /** 1000 frames per second keeps the numbers readable: 1 frame = 1 ms. */
    private val r = Receipts(1000) { f ->
        val m = JSONObject(f)
        sent.add("${m.getString("state")} ${m.getString("id")} ${m.getLong("played_ms")}")
    }

    @Test fun consecutiveAnnouncementsEachStartAndEnd() {
        r.queue("a", 100); r.done("a")
        r.queue("b", 50); r.done("b")
        r.advance(0)
        assertEquals(emptyList<String>(), sent)
        r.advance(10)
        assertEquals(listOf("started a 0"), sent)
        r.advance(100)
        assertEquals(listOf("started a 0", "ended a 100"), sent)
        r.advance(101)
        r.advance(150)
        assertEquals(listOf("started a 0", "ended a 100", "started b 0", "ended b 50"), sent)
    }

    @Test fun endWaitsForDoneAndDrain() {
        r.queue("a", 100)
        r.advance(100)
        assertEquals(listOf("started a 0"), sent)
        r.done("a")
        r.advance(100)
        assertEquals(listOf("started a 0", "ended a 100"), sent)
    }

    @Test fun clearReportsHeardAndUnstarted() {
        r.queue("a", 100)
        r.queue("b", 100); r.done("b")
        r.advance(40)
        r.clear(40)
        assertEquals(listOf("started a 0", "ended a 40", "ended b 0"), sent)
        // positions restart after the flush: the next response starts from frame 0
        r.queue("c", 10); r.done("c")
        r.advance(1)
        r.advance(10)
        assertEquals(listOf("started a 0", "ended a 40", "ended b 0", "started c 0", "ended c 10"), sent)
    }

    @Test fun doneWithoutAudioSendsNothing() {
        r.done("x")
        r.advance(1000)
        assertEquals(emptyList<String>(), sent)
    }
}
