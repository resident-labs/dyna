package dev.dyna.call

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PairingStateTest {
    private class Memory : PairingStore {
        var saved: Pairing? = null
        override fun load() = saved
        override fun save(p: Pairing) { saved = p }
        override fun clear() { saved = null }
    }

    private val old = Pairing("http://a", "c=old")
    private val replacement = Pairing("http://a", "c=new")

    @Test fun aRejectionOfTheCurrentPairingForgetsIt() {
        val s = Memory().also { savePairing(it, old) }
        assertTrue(forgetIf(s, old))
        assertEquals(null, s.saved)
    }

    @Test fun anOldRejectionReleasedAfterAReplacementKeepsTheReplacement() {
        val s = Memory().also { savePairing(it, old) }
        val checked = s.load()!! // the check starts with the old cookie
        savePairing(s, replacement) // the owner pairs again while it runs
        assertFalse(forgetIf(s, checked)) // the old rejection arrives now
        assertEquals(replacement, s.saved)
    }

    @Test fun nothingToForgetIsNotAnError() {
        assertFalse(forgetIf(Memory(), old))
    }
}
