package dev.dyna.call

/** Where the one saved pairing lives. */
interface PairingStore {
    fun load(): Pairing?
    fun save(p: Pairing)
    fun clear()
}

private val lock = Any()

/** Saves a pairing. It never interleaves with [forgetIf]. */
fun savePairing(store: PairingStore, p: Pairing) = synchronized(lock) { store.save(p) }

/**
 * Forgets the saved pairing only if it is still [checked]. A check of the engine takes a
 * while; if the owner paired again meanwhile, the old answer must not delete the new pairing.
 * Returns whether it forgot.
 */
fun forgetIf(store: PairingStore, checked: Pairing): Boolean = synchronized(lock) {
    if (store.load() == checked) { store.clear(); true } else false
}
