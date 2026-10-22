package dev.dyna.call

import android.content.Context
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.util.concurrent.TimeUnit

/** The paired engine: its web app address and the device cookie ("name=value"). */
data class Pairing(val base: String, val cookie: String)

object Api {
    /** One client for HTTP and the call socket. The socket pings so a dead network is noticed. */
    val client: OkHttpClient = OkHttpClient.Builder()
        .pingInterval(20, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.SECONDS)
        .build()

    private const val PREFS = "pairing"

    private class Prefs(ctx: Context) : PairingStore {
        private val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        override fun load(): Pairing? {
            val base = p.getString("base", null) ?: return null
            val cookie = p.getString("cookie", null) ?: return null
            return Pairing(base, cookie)
        }
        override fun save(p: Pairing) { this.p.edit().putString("base", p.base).putString("cookie", p.cookie).apply() }
        override fun clear() { p.edit().clear().apply() }
    }

    fun load(ctx: Context): Pairing? = Prefs(ctx).load()

    /** Forgets the saved pairing if it is still [checked]; see [forgetIf]. */
    fun forgetIf(ctx: Context, checked: Pairing): Boolean = dev.dyna.call.forgetIf(Prefs(ctx), checked)

    /**
     * Whether the engine still accepts the device cookie: false only when it refuses it
     * (revoked or expired). A network error says nothing, so it counts as valid.
     * Call off the main thread.
     */
    fun stillPaired(pairing: Pairing): Boolean = try {
        val req = Request.Builder().url(pairing.base + "/api/events/poll").header("Cookie", pairing.cookie).build()
        client.newCall(req).execute().use { !cookieRejected(it.code) }
    } catch (e: Exception) {
        true
    }

    fun forget(ctx: Context) = Prefs(ctx).clear()

    /**
     * Pairs with the one-time code (the page's pairing flow) and stores the device cookie.
     * Call it off the main thread. It throws with a short message on failure.
     */
    fun pair(ctx: Context, address: String, code: String, name: String): Pairing {
        val base = address.trim().trimEnd('/')
        val body = JSONObject().put("code", code.trim()).put("name", name).toString()
            .toRequestBody("application/json".toMediaType())
        val req = Request.Builder().url("$base/api/pair").post(body)
            .header("Origin", base).header("X-Dyna", "1").build()
        client.newCall(req).execute().use { r ->
            if (!r.isSuccessful) {
                val msg = try { JSONObject(r.body?.string() ?: "").optString("error") } catch (e: Exception) { "" }
                throw IllegalStateException(msg.ifEmpty { "Pairing failed (HTTP ${r.code})." })
            }
            val cookie = r.headers("Set-Cookie").firstOrNull()?.substringBefore(';')
                ?: throw IllegalStateException("The engine sent no device cookie.")
            val pairing = Pairing(base, cookie)
            savePairing(Prefs(ctx), pairing)
            return pairing
        }
    }
}
