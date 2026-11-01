# Backups

The engine keeps the newest 3 daily copies of `store.db` in `<data>/backups`. When the owner sets the two backup flags, it also sends each day's archive, encrypted, to a second machine. That machine keeps at least the newest 14 archives. It removes older copies only after 14 days. This guide shows how to set that up and how to restore an archive.

Each backup first removes any `.partial` file a crash left in `<data>/backups`. The engine skips the backup and alerts each room's root when free space is under twice the size of `store.db` plus 20 GiB. A skip also records a failed off-host copy. A disk the engine cannot measure does not stop the backup.

## What the second machine can do

The backup key can add archives. It cannot do anything else:

- `restrict` turns off a shell, terminal, port forwarding and agent forwarding.
- `command=` runs only `backup-recv.sh`, whatever the engine asks for.
- The receiver takes one file and its byte count. It names the file using its own clock.
- It refuses a file over 16 GiB, and a copy that would leave less than 50 GiB free.
- A short copy or invalid byte count is refused, and the partial file is removed.
- The receiver never sends a file back. Only the owner reads the archives, on that machine.

A compromised engine host can replace every copy over 14 days, so the owner has 14 days to notice it; a copy under 14 days old cannot be removed by an upload.

Each archive is an encrypted `tar.gz`. It holds `store.db`, `AGENTS.md`, `rooms/`, `archive/`, `connect/` and `cliproxy/auth-v2/`, when those exist. `rooms/` holds each room's files, transcripts and instructions. `connect/pending.json` contains pending link approval secrets and is included when present. Keep restored archives private.

## Set up the age key pair

Make the key pair on the owner's own machine. Do not make it on the engine host or the second machine.

```sh
mkdir -p ~/dyna-backup
age-keygen -o ~/dyna-backup/age-key.txt
age-keygen -y ~/dyna-backup/age-key.txt
```

The first command writes the secret key and prints the public key. The second command prints the public key again. The public key starts with `age1`.

Keep `age-key.txt` mode 0600, and store it outside the engine host and the second machine, for example in a password manager. Without the secret key, no archive can be read.

Give the engine only the public key. It needs no secret key.

## Make the SSH key and the host alias

Make an SSH key with no passphrase. The engine runs `ssh` with `BatchMode=yes`, so it cannot ask for one.

```sh
ssh-keygen -t ed25519 -N "" -f ~/.ssh/dyna_backup_ed25519
```

On the engine host, add a Host alias to the SSH config of the user that runs the engine:

```text
Host dyna-backup
    HostName backup.example.net
    User dyna-backup
    IdentityFile ~/.ssh/dyna_backup_ed25519
    IdentitiesOnly yes
```

Check the alias without connecting: `ssh -G dyna-backup` prints the resolved settings.

## Install the receiver on the second machine

The receiver writes archives into its own folder. Put it in the backup folder, not in a shared folder. It runs with `umask 077`, so its archives are private.

```sh
sudo useradd --create-home --shell /bin/sh dyna-backup
sudo install -d -m 0700 -o dyna-backup /home/dyna-backup/backups
sudo install -m 0700 -o dyna-backup next/docs/backup-recv.sh /home/dyna-backup/backups/backup-recv.sh
```

The third command copies `next/docs/backup-recv.sh` from this repository. Run it from the repository root. The mode 0700 makes the script executable for the backup user.

Add one line to `/home/dyna-backup/.ssh/authorized_keys`. Use the public key from `dyna_backup_ed25519.pub`:

```text
restrict,command="/home/dyna-backup/backups/backup-recv.sh" ssh-ed25519 AAAA... dyna-engine
```

Keep the whole entry on one line. The path in `command=` must be absolute.

## Turn on off-host copies in the engine

Set the two backup flags on the engine together:

- `--backup.dest` names the SSH alias, here `dyna-backup`.
- `--backup.recipient` is the age public key, which starts with `age1`.

Give the engine only the public key. A copy that fails does not change the local copies. The engine keeps its 3 local copies either way.

## Restore an archive

Stop the engine before you restore. Restore into a new folder. Do not write over the live `store.db`.

Get the archive from the second machine. The receiver does not send files, so use the owner's own access on that machine. Then decrypt it and unpack it with the `age` CLI and `tar`:

```sh
mkdir -p ~/restore
age -d -i ~/dyna-backup/age-key.txt ~/restore/dyna-<stamp>.tar.gz.age | tar xz -C ~/restore
```

Confirm that `archive/` and `connect/` returned if the engine had them. Then check the restored database:

```sh
sqlite3 ~/restore/store.db "PRAGMA integrity_check;"
```

The output must be `ok`. If the check does not print `ok`, do not use that archive. Try the next newest archive.

A local copy in `<data>/backups` holds only `store.db`, named `store-<stamp>.db`. It has no files, transcripts, archives, Connect state or proxy sign-ins. Check it with the same `PRAGMA integrity_check`.
