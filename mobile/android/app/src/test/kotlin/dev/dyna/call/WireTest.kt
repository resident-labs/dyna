package dev.dyna.call

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.json.JSONObject
import org.junit.Test

class WireTest {
    @Test fun parsesEngineFrames() {
        assertEquals(Event.Audio("AAA=", "r1"), parseEvent("""{"type":"audio","audio":"AAA=","id":"r1"}"""))
        assertEquals(Event.AudioDone("r1"), parseEvent("""{"type":"audio_done","id":"r1"}"""))
        assertEquals(Event.Clear, parseEvent("""{"type":"clear","id":"r1"}"""))
        assertEquals(Event.State("listening"), parseEvent("""{"type":"state","state":"listening"}"""))
        assertEquals(Event.Error("boom"), parseEvent("""{"type":"error","message":"boom"}"""))
    }

    @Test fun ignoresUnknownAndBadFrames() {
        assertNull(parseEvent("""{"type":"audio"}"""))
        assertNull(parseEvent("""{"type":"tool","id":"x"}"""))
        assertNull(parseEvent("not json"))
    }

    @Test fun buildsClientFrames() {
        val audio = JSONObject(audioFrame("AQI="))
        assertEquals("audio", audio.getString("type"))
        assertEquals("AQI=", audio.getString("audio"))
        assertEquals("hangup", JSONObject(hangupFrame()).getString("type"))
    }

    @Test fun buildsVoiceUrl() {
        assertEquals("ws://100.1.2.3:7457/api/voice/realtime?v=1&room=a+b", voiceUrl("http://100.1.2.3:7457/", "a b"))
        assertEquals("wss://m.example.dev/api/voice/realtime?v=1&room=dyna", voiceUrl("https://m.example.dev", "dyna"))
    }

    @Test fun buildsPlaybackReceipt() {
        val f = JSONObject(playbackFrame("r1", "ended", 1500))
        assertEquals("playback", f.getString("type"))
        assertEquals("r1", f.getString("id"))
        assertEquals("ended", f.getString("state"))
        assertEquals(1500, f.getLong("played_ms"))
    }

    @Test fun micChunkRoundTrips() {
        val frame = JSONObject(pcmFrame(byteArrayOf(1, 2, 3, 4, 5), 4))
        assertEquals("audio", frame.getString("type"))
        assertEquals(listOf<Byte>(1, 2, 3, 4), decodePcm(frame.getString("audio")).toList())
    }
}
