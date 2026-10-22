package dev.dyna.call

import org.json.JSONObject

// The call protocol, as in web/src/lib/voice/wire.js: JSON text frames on
// /api/voice/realtime?room=<name>, base64 PCM16 mono at 24 kHz both ways.

const val RATE = 24000

/**
 * The voice protocol this app speaks, sent as ?v=. Raise it, and versionCode in
 * version.properties, when the frames change. The engine keeps accepting older versions.
 */
const val PROTOCOL = 1

/** Close code when a newer page took the call. */
const val TAKEN_OVER = 4001

sealed interface Event {
    /** One chunk of base64 PCM16 from the engine. */
    data class Audio(val base64: String, val id: String) : Event
    /** The engine sent all of response [id]. */
    data class AudioDone(val id: String) : Event
    /** The engine dropped queued speech: the owner talked over it. */
    data object Clear : Event
    data class State(val value: String) : Event
    data class Error(val message: String) : Event
}

/** Turns one engine frame into an [Event], or null for frames the app ignores. */
fun parseEvent(text: String): Event? {
    val m = try { JSONObject(text) } catch (e: Exception) { return null }
    return when (m.optString("type")) {
        "audio" -> m.optString("audio").takeIf { it.isNotEmpty() }?.let { Event.Audio(it, m.optString("id")) }
        "audio_done" -> Event.AudioDone(m.optString("id"))
        "clear" -> Event.Clear
        "state" -> Event.State(m.optString("state"))
        "error" -> Event.Error(m.optString("message", "Voice reported an error."))
        else -> null
    }
}

/** One microphone chunk as a client audio frame. */
fun pcmFrame(pcm: ByteArray, len: Int): String =
    audioFrame(java.util.Base64.getEncoder().encodeToString(pcm.copyOf(len)))

fun decodePcm(base64: String): ByteArray = java.util.Base64.getMimeDecoder().decode(base64)

fun audioFrame(base64: String): String = JSONObject().put("type", "audio").put("audio", base64).toString()

/** Tells the engine what the owner heard of response [id]; state is "started" or "ended". */
fun playbackFrame(id: String, state: String, playedMs: Long = 0): String =
    JSONObject().put("type", "playback").put("id", id).put("state", state)
        .put("played_ms", maxOf(0L, playedMs)).toString()

fun hangupFrame(): String = JSONObject().put("type", "hangup").toString()

/** The /api/voice/realtime URL, as ws:// or wss://, for a web app address. */
fun voiceUrl(base: String, room: String): String {
    val ws = if (base.startsWith("https://")) "wss://" + base.removePrefix("https://") else "ws://" + base.removePrefix("http://")
    return ws.trimEnd('/') + "/api/voice/realtime?v=$PROTOCOL&room=" + java.net.URLEncoder.encode(room, "UTF-8")
}
