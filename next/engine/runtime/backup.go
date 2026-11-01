package runtime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const backupStamp = "20060102T150405.000000000Z"

type backupStore interface {
	Backup(context.Context, time.Time) (string, error)
	RecordBackupCopy(context.Context, time.Time, string) error
	AlertDisk(context.Context, contract.HostName, int64, int64, time.Time) error
}

type backupRunner func(context.Context, string, int64, *os.File) error

type backupReader struct {
	ctx context.Context
	io.Reader
}

func (r backupReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func sshBackup(ctx context.Context, dest string, size int64, input *os.File) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", dest, fmt.Sprintf("%d", size))
	cmd.Stdin = input
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 66 {
		return errBackupNoSpace
	}
	return err
}

var errBackupNoSpace = errors.New(store.BackupCopyNoSpace)

type backupLoop struct {
	store     backupStore
	layout    box.Layout
	dest      string
	recipient age.Recipient
	now       func() time.Time
	run       backupRunner
	free      func(context.Context) (int64, bool)
}

func (b *backupLoop) copy(ctx context.Context, path string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(b.layout.Backups(), ".encrypted-*.partial")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	out, err := age.Encrypt(tmp, b.recipient)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tarWriter := tar.NewWriter(gz)
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if err = tarWriter.WriteHeader(&tar.Header{Name: filepath.Base(b.layout.Store()), Mode: 0600, Size: info.Size()}); err != nil {
		return err
	}
	if _, err = io.CopyN(tarWriter, backupReader{ctx, src}, info.Size()); err != nil {
		return err
	}
	dataDir := b.layout.Data
	rootDir, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer rootDir.Close()
	for _, root := range []string{b.layout.Instructions(""), b.layout.Rooms(), b.layout.Archive(), b.layout.Connect(), b.layout.ProxyAuth()} {
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !entry.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(dataDir, path)
			if err != nil {
				return err
			}
			f, err := rootDir.Open(rel)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(rel)
			if err = tarWriter.WriteHeader(header); err != nil {
				return err
			}
			_, err = io.Copy(tarWriter, io.LimitReader(backupReader{ctx, f}, info.Size()))
			return err
		})
		if err != nil {
			return err
		}
	}
	if err = tarWriter.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	info, err = tmp.Stat()
	if err != nil {
		return err
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return b.run(ctx, b.dest, info.Size(), tmp)
}

// roomy removes what a crashed backup left, then checks that a copy would leave the disk its 20 GiB.
// A disk it cannot measure does not stop the backup: a broken probe must not end backups for good.
func (b *backupLoop) roomy(ctx context.Context, now time.Time) bool {
	partials, err := filepath.Glob(filepath.Join(b.layout.Backups(), "*.partial"))
	if err != nil {
		log.Printf("list partial backups: %v", err)
	}
	for _, name := range partials {
		if err := os.Remove(name); err != nil {
			log.Printf("remove partial backup: %v", err)
		}
	}
	var size int64
	if info, err := os.Stat(b.layout.Store()); err == nil {
		size = info.Size()
	}
	need := 2*size + 20*box.GiB
	free, measured := b.free(ctx)
	if !measured {
		log.Printf("backup disk check failed, backing up anyway")
		return true
	}
	if free >= need {
		return true
	}
	log.Printf("backup skipped: %d bytes free, %d needed", free, need)
	if err := b.store.AlertDisk(ctx, contract.ControlPlane, free, need, now); err != nil {
		log.Printf("backup alert: %v", err)
	}
	return false
}

func (b *backupLoop) backup(ctx context.Context) {
	now := b.now().UTC()
	if !b.roomy(ctx, now) {
		if b.recipient != nil {
			if err := b.store.RecordBackupCopy(ctx, now, "low disk"); err != nil {
				log.Printf("record backup copy: %v", err)
			}
		}
		return
	}
	path, err := b.store.Backup(ctx, now)
	if err != nil {
		log.Printf("backup: %v", err)
		return
	}
	if b.recipient == nil {
		return
	}
	err = b.copy(ctx, path)
	cause := ""
	if err != nil {
		cause = err.Error()
		log.Printf("backup copy: %v", err)
	}
	if err := b.store.RecordBackupCopy(ctx, now, cause); err != nil {
		log.Printf("record backup copy: %v", err)
	}
}

func (b *backupLoop) start(ctx context.Context) {
	names, err := filepath.Glob(filepath.Join(b.layout.Backups(), "store-*.db"))
	if err != nil {
		log.Printf("list backups: %v", err)
	}
	wait := time.Duration(0)
	if len(names) > 0 {
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(names[len(names)-1]), "store-"), ".db")
		if at, err := time.Parse(backupStamp, stamp); err == nil {
			wait = max(0, at.Add(24*time.Hour).Sub(b.now()))
		}
	}
	if wait == 0 {
		b.backup(ctx)
		wait = 24 * time.Hour
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			b.backup(ctx)
			timer.Reset(24 * time.Hour)
		}
	}
}
