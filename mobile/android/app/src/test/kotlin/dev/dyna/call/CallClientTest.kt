package dev.dyna.call

import okhttp3.Request
import okhttp3.Response
import okio.ByteString
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class CallClientTest {
    /** A socket that records what the client sends and closes. The test plays the engine by calling [listener]. */
    private class FakeSocket : WebSocket {
        lateinit var listener: WebSocketListener
        val sent = ArrayList<String>()
        val closes = ArrayList<Int>()
        override fun request() = Request.Builder().url("http://x/").build()
        override fun queueSize() = 0L
        override fun send(text: String): Boolean { sent.add(text); return true }
        override fun send(bytes: ByteString) = false
        override fun close(code: Int, reason: String?): Boolean { closes.add(code); return true }
        override fun cancel() {}
    }

    private val events = ArrayList<Event>()
    private val reasons = ArrayList<String>()
    private val socket = FakeSocket()
    private val client = CallClient(Pairing("http://x", "c=1"), "r", { events.add(it) }, { reasons.add(it) },
        { _, l -> socket.also { it.listener = l } })

    @Test fun framesReachTheCallback() {
        socket.listener.onMessage(socket, """{"type":"state","state":"listening"}""")
        assertEquals(listOf<Event>(Event.State("listening")), events)
    }

    @Test fun engineCloseIsAnsweredAndReportedOnce() {
        socket.listener.onClosing(socket, TAKEN_OVER, "")
        socket.listener.onFailure(socket, RuntimeException("reset"), null)
        assertEquals(listOf("Another device took the call."), reasons)
        assertEquals(listOf(TAKEN_OVER), socket.closes) // OkHttp waits for the app to answer the close
    }

    @Test fun lateCallbacksOfALeftCallDoNothing() {
        client.close() // the app replaced this call
        // What the old socket delivers after that, in every form it can.
        socket.listener.onMessage(socket, """{"type":"audio","audio":"AAA=","id":"old"}""")
        socket.listener.onClosing(socket, 1000, "late")
        socket.listener.onClosed(socket, 1000, "late")
        socket.listener.onFailure(socket, RuntimeException("reset"), null)
        assertTrue(events.isEmpty())
        assertTrue(reasons.isEmpty())
    }

    @Test fun hangupSendsTheFrameThenStopsCallbacks() {
        client.hangup()
        assertTrue(socket.sent.single().contains("hangup"))
        socket.listener.onClosed(socket, 1000, "hangup")
        assertTrue(reasons.isEmpty())
    }
}
