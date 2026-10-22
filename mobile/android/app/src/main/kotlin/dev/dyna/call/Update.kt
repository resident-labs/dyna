package dev.dyna.call

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.core.content.FileProvider
import okhttp3.Request
import java.io.File

/** Looks for a newer app on the paired engine, and installs it through Android's installer. */
object Update {
    /** True when the engine serves a newer app. Call off the main thread; any failure is "no". */
    fun check(pairing: Pairing): Boolean = try {
        val req = Request.Builder().url(pairing.base + "/api/android/version").build()
        Api.client.newCall(req).execute().use { r ->
            r.isSuccessful && updateAvailable(BuildConfig.VERSION_CODE, r.body?.string().orEmpty())
        }
    } catch (e: Exception) {
        false
    }

    /**
     * Downloads the app and opens the installer. Android asks for "install unknown apps"
     * for this app only now, at the first update; the caller runs this again when the owner
     * comes back from that screen. Call off the main thread. Returns a message when it
     * could not start.
     */
    fun install(activity: Activity, pairing: Pairing): String? {
        if (!activity.packageManager.canRequestPackageInstalls()) {
            activity.startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:" + activity.packageName)))
            return "Allow installs from this app, then press Update again."
        }
        val dir = File(activity.cacheDir, "update").apply { mkdirs() }
        val apk = File(dir, "dyna-call.apk")
        try {
            val req = Request.Builder().url(pairing.base + "/api/android/app.apk").build()
            Api.client.newCall(req).execute().use { r ->
                if (!r.isSuccessful) return "The update download failed (HTTP ${r.code})."
                apk.outputStream().use { out -> r.body!!.byteStream().copyTo(out) }
            }
        } catch (e: Exception) {
            return "The update download failed: ${e.message}"
        }
        val uri = FileProvider.getUriForFile(activity, "dev.dyna.call.update", apk)
        activity.startActivity(Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK))
        return null
    }
}
