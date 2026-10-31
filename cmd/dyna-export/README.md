# v1 exporter

Build from the reviewed v1 commit on the capacity host:

```sh
(cd cmd/dyna-export && go build -o "$PWD/dyna-export" .)
```

The owner must pause v1 before cutover. The cutover operator captures a standalone
SQLite backup and private copies of configuration and skill files. Never point
the exporter at a live database or live configuration. The operator reads the
append-only room feeds in place, without copying them.

- `--backup` names the standalone SQLite backup. It must have no WAL or
  shared-memory sidecar.
- `--data` names a private directory containing captured skill bodies.
- `--feeds` names the v1 data root containing `rooms/<room-id>/messages.jsonl`.
  The exporter reads these append-only files in place and never writes them.
  It skips an unterminated final line and names the room in the private report.
  The owner must review that gap; the file read is not an atomic snapshot.
- `--config` names a private captured config directory with `settings.json`
  and `profiles.json`.
- `--manifest` names a private JSON capture manifest. The operator makes it
  after the pause. It contains no remote URLs, credentials, or credential
  references. The operator checks each checkout's GitHub identity and records
  only its normalized `host/owner/name`. The operator selects a default room
  that holds each repository. The operator records the archive refs created
  during cutover, and captures enabled skills into the data tree. Include each
  active repository hook's main checkout path, even when a room uses a worktree
  or another clone. Give both paths the same verified identity when known.
  When no identity is verified, omit the room checkout mapping or set its
  identity to an empty string. The exporter skips it and records a room handoff.
  For an unverified hook checkout alias, the operator may supply `hookRooms`
  with the registered room names. Otherwise the exporter uses only exact
  checkout matches; an unassigned hook appears in the private report only.
  Global hooks also appear in the private report only.
- `--commit` is the full 40-character reviewed source commit.
- `--out` names a new directory; the exporter never replaces one.

For example, the manifest shape is:

```json
{
  "repositories": [
    {"path": "/captured/original/checkout", "identity": "github.com/example/repo", "defaultRoom": "example"}
  ],
  "archiveRefs": [
    {"session": "ses_example", "ref": "refs/dyna/archive/ses_example"}
  ],
  "skills": [
    {"name": "review", "path": "captured-skills/review/SKILL.md"},
    {"name": "package-skill", "package": "example-package@1.0.0"}
  ]
}
```

The paths in `repositories` are the original checkout paths stored in v1,
not live checkout paths for the exporter to read. Skill paths are relative to
`--data` and cannot escape it. The skill catalog is a sanitized capture of
enabled skills. Its count check does not prove that the capture includes every
live skill.

```sh
dyna-export --backup /capture/backup.db --data /capture/data --feeds /v1/data --config /capture/config \
  --manifest /capture/manifest.json --commit <full-commit-sha> --out /capture/bundle
dyna-export verify --backup /capture/backup.db --data /capture/data --feeds /v1/data --config /capture/config \
  --manifest /capture/manifest.json --out /capture/bundle
```

The exporter creates the directory with mode 0700 and files with mode 0600.
The bundle includes device token hashes and private room records. Do not post
or attach it. `report.json` lists field classifications and gaps; it also
needs private handling. A count match cannot detect substitutions or prove
field preservation. The verifier separately compares each rendered instruction text and its order with the captured database. The operator must review all gaps before cutover.
