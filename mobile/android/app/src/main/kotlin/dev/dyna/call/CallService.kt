package dev.dyna.call

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import android.net.wifi.WifiManager
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager

/** What the screen shows about the call. The service writes it; the activity listens. */
object CallState {
    @Volatile var text = "Not in a call."
    @Volatile var live = false
    @Volatile var listener: ((String) -> Unit)? = null

    fun set(text: String, live: Boolean = this.live) {
        this.text = text
        this.live = live
        listener?.invoke(text)
    }
}

/**
 * Holds the call: socket, speaker and microphone. As a foreground service of type
 * microphone it keeps running, and keeps the microphone, after Home and with the
 * screen off. A partial wake lock and a Wi-Fi lock keep the CPU and radio up.
 */
class CallService : Service() {
    private val main = Handler(Looper.getMainLooper())
    private val active = Active<CallClient>()
    private var playback: Playback? = null
    private var mic: AudioRecord? = null
    private var wake: PowerManager.WakeLock? = null
    private var wifi: WifiManager.WifiLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val room = intent?.getStringExtra(ROOM)
        if (room == null) {
            active.get()?.hangup()
            end("Hung up.")
            return START_NOT_STICKY
        }
        startForeground(NOTE_ID, notification(room), ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE)
        val pairing = Api.load(this)
        if (pairing == null) {
            end("Pair first.")
            return START_NOT_STICKY
        }
        release()
        acquireLocks()
        val out = Playback { frame -> active.get()?.send(frame) }.also { playback = it }
        lateinit var mine: CallClient
        mine = CallClient(pairing, room, { e ->
            when (e) {
                is Event.Audio -> out.play(e.base64, e.id)
                is Event.AudioDone -> out.done(e.id)
                is Event.Clear -> out.clear()
                is Event.State -> main.post { active.ifCurrent(mine) { CallState.set("Voice: ${e.value}") } }
                is Event.Error -> main.post { active.ifCurrent(mine) { CallState.set(e.message) } }
            }
        }, { reason ->
            // Runs on the main thread, and only if this call is still the current one.
            main.post { active.ifCurrent(mine) { end(reason) } }
        })
        active.set(mine)
        startMic()
        CallState.set("Joining $room…", live = true)
        return START_NOT_STICKY
    }

    @android.annotation.SuppressLint("MissingPermission") // the activity asks before it starts the service
    private fun startMic() {
        val min = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        val rec = AudioRecord(MediaRecorder.AudioSource.VOICE_COMMUNICATION, RATE,
            AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, maxOf(min, CHUNK * 4))
        mic = rec
        rec.startRecording()
        Thread {
            val buf = ByteArray(CHUNK)
            while (mic === rec) {
                val n = rec.read(buf, 0, buf.size)
                if (n <= 0) break
                active.get()?.send(pcmFrame(buf, n))
            }
        }.apply { name = "dyna-mic"; start() }
    }

    private fun acquireLocks() {
        wake = (getSystemService(Context.POWER_SERVICE) as PowerManager)
            .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "dyna:call").apply { acquire() }
        wifi = (applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager)
            .createWifiLock(WifiManager.WIFI_MODE_FULL_LOW_LATENCY, "dyna:call").apply { acquire() }
    }

    /** Stops the socket, speaker, microphone and locks. */
    private fun release() {
        val rec = mic
        mic = null
        rec?.release()
        active.get()?.close()
        active.set(null)
        playback?.close()
        playback = null
        wake?.release()
        wake = null
        wifi?.release()
        wifi = null
    }

    private fun end(message: String) {
        release()
        CallState.set(message, live = false)
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    override fun onDestroy() {
        release()
        CallState.live = false
        super.onDestroy()
    }

    private fun notification(room: String): Notification {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "Call", NotificationManager.IMPORTANCE_LOW))
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, CHANNEL)
            .setSmallIcon(android.R.drawable.stat_sys_phone_call)
            .setContentTitle("Dyna call")
            .setContentText("In a call in $room")
            .setContentIntent(open)
            .setOngoing(true)
            .build()
    }

    companion object {
        const val ROOM = "room"
        private const val CHANNEL = "call"
        private const val NOTE_ID = 1
        /** 100 ms of PCM16 mono at 24 kHz. */
        private const val CHUNK = RATE / 10 * 2

        fun start(ctx: Context, room: String) =
            ctx.startForegroundService(Intent(ctx, CallService::class.java).putExtra(ROOM, room))

        fun stop(ctx: Context) = ctx.startService(Intent(ctx, CallService::class.java))
    }
}
