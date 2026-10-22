package dev.dyna.call

import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener

/**
 * One socket on the live call. [onClosed] runs once with a short reason when the call
 * or the network ends it. Events arrive on OkHttp's thread. After [hangup] this client
 * calls nothing back, so a late callback from an old socket cannot touch a newer call.
 */
class CallClient(
    private val pairing: Pairing,
    room: String,
    private val onEvent: (Event) -> Unit,
    private val onClosed: (String) -> Unit,
    connect: (Request, WebSocketListener) -> WebSocket = Api.client::newWebSocket,
) {
    private val ws: WebSocket
    @Volatile private var ended = false

    init {
        val req = Request.Builder().url(voiceUrl(pairing.base, room))
            .header("Origin", pairing.base).header("Cookie", pairing.cookie).build()
        ws = connect(req, object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, text: String) {
                if (!ended) parseEvent(text)?.let(onEvent)
            }
            // The engine closes first; OkHttp waits for us to answer the close.
            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(code, reason)
                finish(if (code == TAKEN_OVER) "Another device took the call." else reason.ifEmpty { "The call ended." })
            }
            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) =
                finish(if (response != null) "The engine refused the call (HTTP ${response.code}). Is a call live in this room?" else "Connection lost: ${t.message}")
        })
    }

    @Synchronized private fun finish(reason: String) {
        if (ended) return
        ended = true
        onClosed(reason)
    }

    fun send(frame: String) = ws.send(frame)

    /** Leaves the call without ending it for other pages. */
    fun close() {
        ended = true
        ws.close(1000, "leave")
    }

    fun hangup() {
        ended = true
        ws.send(hangupFrame())
        ws.close(1000, "hangup")
    }
}
