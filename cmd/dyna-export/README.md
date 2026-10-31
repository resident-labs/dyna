# v1 exporter

Build from the reviewed v1 commit on the capacity host:

```sh
(cd cmd/dyna-export && go build -o "$PWD/dyna-export" .)
```

After the owner pauses v1, the cutover operator captures a standalone SQLite
backup and consistent private copies of the required files. Never point the
exporter at a live database or live configuration.

- `--backup` names the standalone SQLite backup. It must have no WAL or
  shared-memory sidecar.
- `--data` names a private capture of the v1 data tree. It must contain each
  room's `rooms/<room-id>/messages.jsonl` and any captured skill bodies.
- `--config` names a private captured config directory with `settings.json`
  and `profiles.json`.
- `--manifest` names a private JSON capture manifest. The operator makes it
  after the pause. It contains no remote URLs, credentials, or credential
  references. The operator checks each checkout's GitHub identity and records
  only its normalized `host/owner/name`. The operator selects a default room
  that holds each repository. The operator records the archive refs created
  during cutover, and captures enabled skills into the data tree.
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
dyna-export --backup /capture/backup.db --data /capture/data --config /capture/config \
  --manifest /capture/manifest.json --commit <full-commit-sha> --out /capture/bundle
dyna-export verify --backup /capture/backup.db --data /capture/data --config /capture/config \
  --manifest /capture/manifest.json --out /capture/bundle
```

The exporter creates the directory with mode 0700 and files with mode 0600.
The bundle includes device token hashes and private room records. Do not post
or attach it. `report.json` lists field classifications and gaps; it also
needs private handling. A count match cannot detect substitutions or prove
field preservation. The operator must review all gaps before cutover.
