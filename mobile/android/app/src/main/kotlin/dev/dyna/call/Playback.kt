package dev.dyna.call

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack

/** Plays the engine's PCM16 mono 24 kHz audio on the speaker and reports what was heard through [send]. */
class Playback(send: (String) -> Unit) {
    private val track = AudioTrack.Builder()
        .setAudioAttributes(AudioAttributes.Builder()
            .setUsage(AudioAttributes.USAGE_VOICE_COMMUNICATION)
            .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
        .setAudioFormat(AudioFormat.Builder()
            .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
            .setSampleRate(RATE).setChannelMask(AudioFormat.CHANNEL_OUT_MONO).build())
        .setTransferMode(AudioTrack.MODE_STREAM)
        .setBufferSizeInBytes(AudioTrack.getMinBufferSize(RATE, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT) * 4)
        .build()
    private val core = PlaybackCore(object : Sink {
        override fun write(pcm: ByteArray, offset: Int, length: Int) = track.write(pcm, offset, length, AudioTrack.WRITE_NON_BLOCKING)
        override fun head() = track.playbackHeadPosition.toLong() and 0xffffffffL
        override fun flush() { track.pause(); track.flush(); track.play() }
    }, RATE, send)
    @Volatile private var open = true
    private val writer = Thread {
        track.play()
        while (open) {
            val chunk = try { core.next() } catch (e: InterruptedException) { break }
            core.write(chunk)
        }
    }.apply { name = "dyna-playback"; start() }
    private val clock = Thread {
        while (open) {
            core.tick()
            try { Thread.sleep(20) } catch (e: InterruptedException) { break }
        }
    }.apply { name = "dyna-playback-clock"; start() }

    fun play(base64: String, id: String) = core.play(decodePcm(base64), id)

    fun done(id: String) = core.done(id)

    fun clear() = core.clear()

    fun close() {
        open = false
        writer.interrupt()
        clock.interrupt()
        track.release()
    }
}
