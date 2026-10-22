package dev.dyna.call

import org.json.JSONObject
import java.net.URLDecoder

/**
 * The pairing link from Settings > Devices: dyna://pair?url=<web app address>&code=<code>.
 * Any malformed part rejects the whole link; the caller ignores a null.
 */
fun parsePairLink(link: String): Pair<String, String>? {
    if (!link.startsWith("dyna://pair?")) return null
    val q = HashMap<String, String>()
    for (part in link.substringAfter('?').substringBefore('#').split('&')) {
        val (k, v) = (part.split('=', limit = 2) + "").take(2)
        if (k.isEmpty()) continue
        q[k] = try { URLDecoder.decode(v, "UTF-8") } catch (e: IllegalArgumentException) { return null }
    }
    val url = q["url"].orEmpty()
    val code = q["code"].orEmpty()
    if (!(url.startsWith("http://") || url.startsWith("https://")) || code.isEmpty()) return null
    return url to code
}

/**
 * Whether the engine's version.json ({"versionCode": N, ...}) is newer than the installed
 * app. The number changes only when the app or the voice protocol it speaks changes, so an
 * engine update that leaves the app alone never prompts.
 */
fun updateAvailable(installed: Int, versionJson: String): Boolean {
    val served = try { JSONObject(versionJson).optInt("versionCode", 0) } catch (e: Exception) { 0 }
    return served > installed
}

/** The host part of a web app address, for "Paired with <host>". */
fun hostOf(base: String): String = base.substringAfter("://").substringBefore('/')

/** Whether an answer to an authenticated call says the device cookie is no longer valid. */
fun cookieRejected(httpCode: Int): Boolean = httpCode == 401 || httpCode == 403
