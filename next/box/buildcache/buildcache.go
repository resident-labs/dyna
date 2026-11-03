// Package buildcache is the compiler cache that a host's boxes and CI runners share. sccache's local disk cache serves one
// sccache server at a time, and each box and runner runs its own, so the host keeps one store instead. It serves the store over
// WebDAV on a Unix socket, and sccache reads and writes it by key.
package buildcache

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/webdav"
)

const (
	// maxSize bounds the store. Horizon's 500 dependencies take 260 MiB for one toolchain and profile, so this keeps every
	// repository's profiles across several toolchains, and a disk smaller than 500 GiB keeps a tenth of itself instead.
	maxSize = 50 << 30
	// pruneEvery is how often the store is cut back to its bound. A build writes far less than a tenth of the bound in an hour.
	pruneEvery = time.Hour
	// tempPrefix names a file that a write has not finished. It takes the entry's name only when whole, so no read sees half
	// an entry, and one left by a crash goes after staleTemp.
	tempPrefix = ".put-"
	staleTemp  = time.Hour
)

// Serve serves the store in dir on a Unix socket until ctx ends, and prunes it.
func Serve(ctx context.Context, dir, socket string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	// A CI runner connects as a subordinate UID, which owns nothing on the host.
	if err := os.Chmod(socket, 0o666); err != nil {
		listener.Close()
		return err
	}
	go func() {
		for {
			if err := prune(ctx, dir, limit(dir)); err != nil && ctx.Err() == nil {
				log.Printf("prune build cache: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(pruneEvery):
			}
		}
	}()
	server := &http.Server{Handler: Handler(dir)}
	go func() {
		<-ctx.Done()
		server.Close()
	}()
	if err := server.Serve(listener); ctx.Err() == nil {
		return err
	}
	return nil
}

// Handler serves the store in dir. sccache needs only reads and writes by key, and WebDAV's other methods come with them.
func Handler(dir string) http.Handler {
	dav := &webdav.Handler{FileSystem: webdav.Dir(dir), LockSystem: webdav.NewMemLS()}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Join(dir, filepath.FromSlash(path.Clean("/"+r.URL.Path)))
		switch r.Method {
		case http.MethodPut:
			put(w, r, dir, name)
			return
		case http.MethodGet:
			// A read is a use, so pruning removes what was used longest ago.
			now := time.Now()
			_ = os.Chtimes(name, now, now)
		}
		dav.ServeHTTP(w, r)
	})
}

// put writes an entry beside its name and then renames it, so two boxes writing one key both leave a whole entry.
func put(w http.ResponseWriter, r *http.Request, dir, name string) {
	if name == dir || strings.HasPrefix(filepath.Base(name), tempPrefix) {
		http.Error(w, "invalid name", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	file, err := os.CreateTemp(filepath.Dir(name), tempPrefix+"*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(file, r.Body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), name)
	}
	if err != nil {
		os.Remove(file.Name())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// limit is the store's bound on the disk that holds dir.
func limit(dir string) int64 {
	var disk syscall.Statfs_t
	if err := syscall.Statfs(dir, &disk); err != nil {
		return maxSize
	}
	return min(maxSize, int64(disk.Blocks)*disk.Bsize/10)
}

// prune removes the entries used longest ago once the store passes its bound, down to nine tenths of it, so that it does not
// run again at the next write.
func prune(ctx context.Context, dir string, bound int64) error {
	type entry struct {
		path string
		size int64
		used time.Time
	}
	var entries []entry
	var total int64
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), tempPrefix) {
			if time.Since(info.ModTime()) > staleTemp {
				os.Remove(name)
			}
			return nil
		}
		entries = append(entries, entry{name, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil || total <= bound {
		return err
	}
	slices.SortFunc(entries, func(a, b entry) int { return a.used.Compare(b.used) })
	for _, e := range entries {
		if total <= bound/10*9 {
			break
		}
		if err := os.Remove(e.path); err == nil || errors.Is(err, os.ErrNotExist) {
			total -= e.size
		}
	}
	return nil
}
