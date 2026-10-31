package runtime

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

const backupStamp = "20060102T150405.000000000Z"
const copyStamp = "20060102T150405000000000Z"

type backupStore interface {
	Backup(context.Context, time.Time) (string, error)
	RecordBackupCopy(context.Context, time.Time, string) error
}

type backupRunner func(context.Context, string, string, int64, *os.File) error

func sshBackup(ctx context.Context, dest, name string, size int64, input *os.File) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", dest, fmt.Sprintf("%s %d", name, size))
	cmd.Stdin = input
	return cmd.Run()
}

type backupLoop struct {
	store     backupStore
	dir       string
	dest      string
	recipient age.Recipient
	now       func() time.Time
	run       backupRunner
}

func (b *backupLoop) copy(ctx context.Context, path string, now time.Time) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(b.dir, ".encrypted-*.partial")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	out, err := age.Encrypt(tmp, b.recipient)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, src); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	info, err := tmp.Stat()
	if err != nil {
		return err
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return b.run(ctx, b.dest, "dyna-"+now.UTC().Format(copyStamp)+".db.age", info.Size(), tmp)
}

func (b *backupLoop) backup(ctx context.Context) {
	now := b.now().UTC()
	path, err := b.store.Backup(ctx, now)
	if err != nil {
		log.Printf("backup: %v", err)
		return
	}
	if b.recipient == nil {
		return
	}
	err = b.copy(ctx, path, now)
	cause := ""
	if err != nil {
		cause = err.Error()
		log.Printf("backup copy: %v", err)
	}
	if err := b.store.RecordBackupCopy(ctx, now, cause); err != nil {
		log.Printf("record backup copy: %v", err)
	}
}

func (b *backupLoop) start(ctx context.Context, ticks <-chan time.Time) {
	names, err := filepath.Glob(filepath.Join(b.dir, "store-*.db"))
	if err != nil {
		log.Printf("list backups: %v", err)
	}
	sort.Strings(names)
	due := true
	if len(names) > 0 {
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(names[len(names)-1]), "store-"), ".db")
		if at, err := time.Parse(backupStamp, stamp); err == nil && b.now().Sub(at) < 24*time.Hour {
			due = false
		}
	}
	if due {
		b.backup(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			b.backup(ctx)
		}
	}
}
