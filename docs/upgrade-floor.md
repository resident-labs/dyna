# Store upgrade floor

The engine checks the upgrade floor before it runs startup migrations. The
floor starts empty, so this release still accepts every previously supported
store. Each requirement names a completed import key in SQLite's `meta` table
and a release that can import a store missing that key.

A cleanup may remove an import or migration shim only after its eager import
has shipped in a release at least 30 days earlier. The import must write its
completion key only after it has processed all retained records. A lazy reader
does not prove this: first ship an eager pass and completion key, then wait.

In the cleanup PR, add that key to `upgradeFloor` in
`internal/engine/stores/floor.go`. Name the newest release that still has the
import and can bring the older store through it. Keep the list of pre-SQLite
legacy state file patterns there complete for the removed import. Add tests for
its old stores, including stores without `dyna.db`. Never treat a legacy JSON
store as a new empty store. A missing key refuses startup before migrations and
tells the owner which release to install and start once.

A new empty store starts with the required keys. The engine reports migration
failures through its existing typed engine alert and startup health status.
