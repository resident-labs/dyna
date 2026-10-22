package dev.dyna.call

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class LinkTest {
    @Test fun parsesThePairingLink() {
        assertEquals("https://m.example.dev" to "ABC123XYZ9",
            parsePairLink("dyna://pair?url=https%3A%2F%2Fm.example.dev&code=ABC123XYZ9"))
        assertEquals("http://100.64.0.1:7457" to "C", parsePairLink("dyna://pair?code=C&url=http://100.64.0.1:7457"))
    }

    @Test fun refusesBadLinks() {
        assertNull(parsePairLink("https://pair?url=http://x&code=C"))
        assertNull(parsePairLink("dyna://pair?url=ftp://x&code=C"))
        assertNull(parsePairLink("dyna://pair?url=http://x"))
        assertNull(parsePairLink("dyna://pair?code=C"))
        assertNull(parsePairLink(""))
    }

    @Test fun badEscapesRejectTheLinkWithoutThrowing() {
        assertNull(parsePairLink("dyna://pair?url=http://x&code=%"))
        assertNull(parsePairLink("dyna://pair?url=http://x&code=%4"))
        assertNull(parsePairLink("dyna://pair?url=http://x&code=%ZZ"))
        assertNull(parsePairLink("dyna://pair?url=http://x&code=C&extra=%G1")) // an unknown parameter counts too
    }

    @Test fun updatesOnlyWhenTheServedVersionIsNewer() {
        assertTrue(updateAvailable(1, """{"versionCode":2,"protocol":1}"""))
        // An engine update that does not touch the app serves the same number: no prompt.
        assertFalse(updateAvailable(2, """{"versionCode":2,"protocol":1}"""))
        assertFalse(updateAvailable(3, """{"versionCode":2,"protocol":1}"""))
        assertFalse(updateAvailable(1, "not json"))
        assertFalse(updateAvailable(1, "{}"))
    }

    @Test fun showsTheHostOfTheAddress() {
        assertEquals("100.64.0.1:7457", hostOf("http://100.64.0.1:7457"))
        assertEquals("m.example.dev", hostOf("https://m.example.dev/"))
    }

    @Test fun onlyARefusalMeansTheCookieIsGone() {
        assertTrue(cookieRejected(401))
        assertTrue(cookieRejected(403))
        assertFalse(cookieRejected(200))
        assertFalse(cookieRejected(502)) // a proxy error is not a revoked device
    }
}
