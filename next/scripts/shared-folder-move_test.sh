#!/usr/bin/env bash
# LEGACY(v1-transfer): the test of shared-folder-move.sh. Remove with it.
# Real rsync over a fake ssh that gives each host its own folder, against a real SQLite store.
set -uo pipefail
script=$(cd "$(dirname "$0")" && pwd)/shared-folder-move.sh
tmp=$(mktemp -d)
trap 'chmod -R u+w "$tmp"; rm -rf "$tmp"' EXIT
export HOST_DATA=$tmp/hostdata DATA=$tmp/data DYNA=$tmp/tools/dyna SOCKET=$tmp/sock TEST_TMP=$tmp
room=room_1
old=rooms/$room/shared
mkdir -p "$tmp/tools" "$DATA/rooms/$room" "$tmp/dyna-files"
export PATH=$tmp/tools:$PATH

# ssh runs here, with the host data folder swapped for the host's own.
cat > "$tmp/tools/ssh" <<'SH'
#!/usr/bin/env bash
user=
while [[ $1 == -* ]]; do
  if [[ $1 == -l ]]; then user=$2@; shift; fi
  shift
done
login=$user$1; shift
command="$*"
export TEST_LOGIN=$login
exec bash -c "${command//$HOST_DATA/$TEST_TMP/$login}"
SH
# sqlite3 is a small stand-in where the real one is missing. It prints a row as the real one does.
cat > "$tmp/tools/sqlite3" <<'SH'
#!/usr/bin/env python3
import sqlite3, sys
args = [a for a in sys.argv[1:] if a != '-readonly']
db = sqlite3.connect(args[0], isolation_level=None)
if args[1].lstrip().upper().startswith(('BEGIN', 'CREATE', 'INSERT')):
    db.executescript(args[1])
else:
    for row in db.execute(args[1]):
        print('|'.join('' if v is None else str(v) for v in row))
SH
# podman answers from a file of container lines, one set per host.
cat > "$tmp/tools/podman" <<'SH'
#!/usr/bin/env bash
list=$TEST_TMP/containers-${TEST_LOGIN:-engine}
[ -f "$list" ] || exit 0
case $1 in
  ps) cut -d' ' -f1 "$list" ;;
  inspect) cat "$list" ;;
esac
SH
cat > "$tmp/tools/dyna" <<'SH'
#!/usr/bin/env bash
echo "$*" >> "$TEST_TMP/dyna-log"
for arg in "$@"; do case $arg in env.sh=*) cp "${arg#env.sh=}" "$TEST_TMP/dyna-files/env.sh" ;; esac; done
SH
cat > "$tmp/tools/systemctl" <<'SH'
#!/usr/bin/env bash
cat "$TEST_TMP/engine-state" 2>/dev/null
SH
chmod +x "$tmp"/tools/*

failed=0
fail() { echo "FAIL: $*"; failed=1; }
has() { [ "$(cat "$2" 2>/dev/null)" = "$3" ] || fail "$1: $2 holds '$(cat "$2" 2>/dev/null)', want '$3'"; }
exists() { [ -e "$1" ] || fail "$1 is missing"; }
absent() { [ ! -e "$1" ] || fail "$1 should not be there"; }
put() { mkdir -p "$(dirname "$1")"; printf '%s' "$2" > "$1"; touch -d "$3" "$1"; }
move() { bash "$script" "$room" "$@"; }

central=$DATA/$old
engine=$HOST_DATA/$old
one=$tmp/one@one/$old
two=$tmp/two@two/$old

python3 -c "
import sqlite3
db = sqlite3.connect('$DATA/store.db')
db.executescript('''
CREATE TABLE meta (key TEXT, value TEXT);
INSERT INTO meta VALUES ('schema_version','70');
CREATE TABLE hosts (name TEXT, ssh TEXT, state TEXT, engine_host INTEGER);
INSERT INTO hosts VALUES ('engine','pip@localhost','ready',1),('one','one@one','ready',0),('two','two@two','ready',0),('gone','gone@gone','removed',0);
CREATE TABLE subscriptions (id TEXT, room TEXT, every_ms INTEGER, run_image TEXT, run_size TEXT, enabled INTEGER, run_host TEXT);
CREATE TABLE subscription_args (sub TEXT, pos INTEGER, arg TEXT);
CREATE TABLE subscription_files (sub TEXT, path TEXT, body TEXT);
CREATE TABLE subscription_events (sub TEXT, event_type TEXT);
CREATE TABLE room_shared (room TEXT PRIMARY KEY, bytes INTEGER, measured_at INTEGER);
INSERT INTO subscriptions VALUES ('sub_w','$room',900000,'horizon','small',1,'');
INSERT INTO subscriptions VALUES ('sub_wake','$room',0,'','',1,'');
INSERT INTO subscription_args VALUES ('sub_w',0,'bash'),('sub_w',1,'author-watch.sh'),('sub_w',2,''),('sub_w',3,'3');
INSERT INTO subscription_files VALUES ('sub_w','env.sh','WATCH_HOME=/box/shared/watch
repo_dir=\$WATCH_HOME/horizon.git
');
''')
"

put "$engine/only-engine" engine "2026-10-01 00:00"
put "$engine/same" old "2026-10-01 00:00"
put "$engine/horizon-local/data" heavy "2026-10-01 00:00"
put "$engine/watch/horizon.git/HEAD" clone "2026-10-01 00:00"
put "$engine/watch/state/x" state "2026-10-01 00:00"
put "$one/only-one" one "2026-10-02 00:00"
put "$one/same" newest "2026-10-03 00:00"
put "$one/horizon-local/data" heavy-one "2026-10-02 00:00"
put "$two/same" middle "2026-10-02 00:00"
printf '#!/bin/sh\n' > "$two/tool.sh"; chmod +x "$two/tool.sh"

move check > "$tmp/check.out" || fail "check failed"
grep -q 'engine host: needs no FUSE' "$tmp/check.out" || fail "check missed the engine host"
grep -q '== gone' "$tmp/check.out" && fail "check visited a removed host"
bash "$script" room_empty check > "$tmp/check.out" || fail "check stopped on a host with no folder"
[ "$(grep -c '^== ' "$tmp/check.out")" = 3 ] || fail "check skipped a host: $(cat "$tmp/check.out")"

move plan > "$tmp/plan.out" || fail "plan failed"
absent "$central/only-one"
absent "$HOST_DATA/cache/horizon-local"
grep -q 'only-one' "$tmp/plan.out" || fail "plan listed nothing: $(cat "$tmp/plan.out")"

move copy > "$tmp/copy.out" || fail "copy failed: $(cat "$tmp/copy.out")"
has "only one host's file arrives" "$central/only-one" one
has "only the engine's file arrives" "$central/only-engine" engine
has "the newest file wins" "$central/same" newest
[ -x "$central/tool.sh" ] || fail "a script lost its mode"
absent "$central/horizon-local"
absent "$central/watch/horizon.git"
has "other watcher files arrive" "$central/watch/state/x" state
has "the heavy folder reaches the host's cache" "$HOST_DATA/cache/horizon-local/data" heavy
has "each host keeps its own" "$tmp/one@one/cache/horizon-local/data" heavy-one
has "the clone reaches the cache" "$HOST_DATA/cache/horizon-watch.git/HEAD" clone
absent "$tmp/two@two/cache/horizon-local"
has "running boxes keep their heavy folder" "$engine/horizon-local/data" heavy
has "running boxes keep the clone" "$engine/watch/horizon.git/HEAD" clone
has "a host's file stays where it was" "$one/same" newest

# A box on the central copy wrote since, and a box on an old folder wrote something else.
put "$central/same" central-newer "2026-10-05 00:00"
put "$one/late" late "2026-10-04 00:00"
move copy > /dev/null || fail "catch-up failed"
has "a newer central file survives a catch-up" "$central/same" central-newer
has "a catch-up brings new files" "$central/late" late

echo "box_a running layout=3 $one /x" > "$tmp/containers-one@one"
echo "box_b running layout=4 /x" > "$tmp/containers-engine"
echo "box_c exited layout=3 /x" > "$tmp/containers-two@two"
move stale > "$tmp/stale.out"; status=$?
[ "$status" = 1 ] || fail "stale exited $status with a box still running"
grep -q '^one box_a running layout=3 old-folder=yes' "$tmp/stale.out" || fail "stale missed box_a: $(cat "$tmp/stale.out")"
grep -q '^two box_c exited' "$tmp/stale.out" || fail "stale missed a stopped box"
grep -q 'box_b' "$tmp/stale.out" && fail "stale listed a box on the new layout"

if move finish > "$tmp/finish.out" 2>&1; then fail "finish ran with a box on an old folder"; fi
exists "$one"
absent "$one.before-move"

: > "$tmp/containers-one@one"
move stale > "$tmp/stale.out" || fail "stale failed with only a stopped box left"
grep -q 'no running box is on the old layout' "$tmp/stale.out" || fail "stale did not say it finished"

put "$one/last" last "2026-10-06 00:00"
move finish > "$tmp/finish.out" 2>&1 || fail "finish failed: $(cat "$tmp/finish.out")"
has "finish copies once more" "$central/last" last
has "the old folder keeps its files" "$one.before-move/only-one" one
has "the old folder keeps its heavy folder" "$engine.before-move/horizon-local/data" heavy
[ -d "$one" ] && [ -z "$(ls -A "$one")" ] || fail "finish left no empty folder for an old stopped box"
move finish > "$tmp/finish.out" 2>&1 || fail "a second finish failed: $(cat "$tmp/finish.out")"
has "a second finish changes nothing" "$one.before-move/only-one" one

put "$one/box-wrote" late "2026-10-07 00:00"
if move finish > "$tmp/finish.out" 2>&1; then fail "finish passed two folders that both hold files"; fi
has "nothing is lost" "$one/box-wrote" late
rm "$one/box-wrote"

move watchers-off > /dev/null || fail "watchers-off failed"
grep -q 'subscription disable sub_w$' "$tmp/dyna-log" || fail "watchers-off missed a Run: $(cat "$tmp/dyna-log")"
grep -q 'sub_wake' "$tmp/dyna-log" && fail "watchers-off touched a wake"
python3 -c "
import sqlite3
sqlite3.connect('$DATA/store.db').execute(\"UPDATE subscriptions SET enabled=0 WHERE id='sub_w'\").connection.commit()"
move watchers-on hermeslab > /dev/null || fail "watchers-on failed"
grep -q 'subscription add --room room_1 --every 900000ms --image horizon --size small --host hermeslab --file env.sh=.* -- bash author-watch.sh  3$' "$tmp/dyna-log" || fail "watchers-on added: $(cat "$tmp/dyna-log")"
grep -q 'subscription remove sub_w$' "$tmp/dyna-log" || fail "watchers-on left the old Run"
grep -q '^repo_dir=/box/cache/horizon-watch.git$' "$tmp/dyna-files/env.sh" || fail "env.sh still names the old clone"
grep -q '^WATCH_HOME=/box/shared/watch$' "$tmp/dyna-files/env.sh" || fail "env.sh lost its state folder"

echo active > "$tmp/engine-state"
if move rollback > /dev/null 2>&1; then fail "rollback ran with the engine up"; fi
echo inactive > "$tmp/engine-state"
python3 -c "
import sqlite3
db = sqlite3.connect('$DATA/store.db')
db.execute('ALTER TABLE hosts ADD COLUMN x')
db.commit()"
put "$central/after-move" after "2026-10-08 00:00"
move rollback > "$tmp/rollback.out" 2>&1 || fail "rollback failed: $(cat "$tmp/rollback.out")"
has "the old folder is back" "$one/only-one" one
absent "$one.before-move"
has "what reached the central copy goes back" "$one/after-move" after
has "the central copy stays" "$central/after-move" after
version=$(sqlite3 "$DATA/store.db" "SELECT value FROM meta WHERE key='schema_version'")
[ "$version" = 69 ] || fail "the store is at $version"

if [ "$failed" = 0 ]; then echo "shared-folder-move: all passed"; fi
exit "$failed"
