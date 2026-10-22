package dev.dyna.call

import android.Manifest
import android.app.Activity
import android.content.pm.PackageManager
import android.content.Intent
import android.os.Bundle
import android.text.InputType
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import kotlin.concurrent.thread

/** The whole UI: pair once, then join the live call in a room. */
class MainActivity : Activity() {
    private var pendingRoom: String? = null
    private lateinit var status: TextView
    private lateinit var address: EditText
    private lateinit var code: EditText
    private lateinit var updateButton: Button
    private lateinit var pairForm: View
    private lateinit var pairedRow: LinearLayout
    private lateinit var pairedText: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val box = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL; setPadding(48, 96, 48, 48) }
        fun field(hint: String, value: String = "") = EditText(this).apply {
            this.hint = hint; setText(value); inputType = InputType.TYPE_CLASS_TEXT; setSingleLine()
            box.addView(this, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
        }
        fun button(label: String, onClick: () -> Unit) = Button(this).apply {
            text = label; setOnClickListener { onClick() }
            box.addView(this, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
        }
        val saved = Api.load(this)
        // The pairing form: shown until the device is paired, and again if the engine refuses the pairing.
        val form = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        box.addView(form, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
        val into = { v: View -> form.addView(v, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT) }
        address = EditText(this).apply { hint = "Dyna address, e.g. http://100.64.0.1:7457"; setText(saved?.base ?: ""); inputType = InputType.TYPE_CLASS_TEXT; setSingleLine() }.also(into)
        code = EditText(this).apply { hint = "Pairing code"; inputType = InputType.TYPE_CLASS_TEXT; setSingleLine() }.also(into)
        Button(this).apply { text = "Pair"; setOnClickListener { pair(address.text.toString(), code.text.toString()) } }.also(into)
        pairedRow = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        pairedText = TextView(this)
        pairedRow.addView(pairedText, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        pairedRow.addView(Button(this).apply { text = "Pair again"; setOnClickListener { Api.forget(this@MainActivity); showPairing(null) } })
        box.addView(pairedRow, 0)
        pairForm = form
        showPairing(saved)
        val room = field("Room name")
        button("Join call") { join(room.text.toString().trim()) }
        button("Hang up") { CallService.stop(this) }
        status = TextView(this).apply { text = if (saved == null) "Not paired." else CallState.text }
        box.addView(status)
        updateButton = button("Update available") { update() }.apply { visibility = View.GONE }
        setContentView(box)
        if (savedInstanceState == null) handleLink(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleLink(intent)
    }

    /** A dyna://pair link fills the address and code and pairs at once. */
    private fun handleLink(intent: Intent) {
        val (url, pairingCode) = parsePairLink(intent.dataString.orEmpty()) ?: return
        setIntent(Intent()) // the code works once: a recreated or resumed screen must not pair again
        address.setText(url)
        code.setText(pairingCode)
        pair(url, pairingCode)
    }

    /** A quiet button: it shows only when the engine serves a newer app. */
    private fun checkForUpdate() {
        val pairing = Api.load(this) ?: return
        thread {
            if (!Api.stillPaired(pairing)) { // revoked or expired on the engine: ask for a new pairing
                // Only if the pairing is still the one checked: the owner may have paired again meanwhile.
                if (Api.forgetIf(this, pairing)) runOnUiThread { showPairing(Api.load(this)); say("The engine no longer accepts this pairing. Pair again.") }
            } else if (Update.check(pairing)) {
                runOnUiThread { updateButton.visibility = View.VISIBLE }
            }
        }
    }

    private fun update() {
        val pairing = Api.load(this) ?: return
        thread { Update.install(this, pairing)?.let { say(it) } }
    }

    /** Paired: one line with the host and the form hidden. Not paired: the form. */
    private fun showPairing(p: Pairing?) {
        pairForm.visibility = if (p == null) View.VISIBLE else View.GONE
        pairedRow.visibility = if (p == null) View.GONE else View.VISIBLE
        if (p != null) pairedText.text = "Paired with ${hostOf(p.base)}"
    }

    private fun say(text: String) = runOnUiThread { status.text = text }

    private fun pair(address: String, code: String) {
        say("Pairing…")
        thread {
            try {
                val p = Api.pair(this, address, code, android.os.Build.MODEL)
                runOnUiThread { showPairing(Api.load(this)); say(CallState.text) }
                checkForUpdate()
            } catch (e: Exception) {
                say(e.message ?: "Pairing failed.")
            }
        }
    }

    private fun join(room: String) {
        if (Api.load(this) == null) return say("Pair first.")
        if (room.isEmpty()) return say("Enter a room name.")
        pendingRoom = room
        val need = arrayOf(Manifest.permission.RECORD_AUDIO, Manifest.permission.POST_NOTIFICATIONS)
            .filter { checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
        if (need.isEmpty()) CallService.start(this, room) else requestPermissions(need.toTypedArray(), 1)
    }

    override fun onRequestPermissionsResult(code: Int, perms: Array<out String>, results: IntArray) {
        // The notification is only a courtesy; the microphone is required.
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            say("The call needs the microphone permission.")
        } else {
            pendingRoom?.let { CallService.start(this, it) }
        }
    }

    override fun onStart() {
        super.onStart()
        CallState.listener = ::say
        say(CallState.text)
        checkForUpdate()
    }

    override fun onStop() {
        CallState.listener = null
        super.onStop()
    }
}
