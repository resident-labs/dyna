// Package share carries every room's shared folder from the engine host to the others, with rclone as a library: the engine
// host serves <Data>/rooms over WebDAV on a Unix socket, and each other host mounts it through a forward of that socket.
package share

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/backend/webdav"
	_ "github.com/rclone/rclone/cmd/mount2"
	"github.com/rclone/rclone/cmd/mountlib"
	_ "github.com/rclone/rclone/cmd/serve/webdav"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/vfs/vfscommon"
	"github.com/spf13/pflag"
)

const (
	// A host sees another host's change once its own listing expires, so dirCacheTime is most of what the rules promise.
	dirCacheTime = 5 * time.Second
	attrTimeout  = time.Second
	// writeBack is how long a closed file waits before it leaves its host, so it is what a dying host can lose.
	writeBack    = 2 * time.Second
	cacheMaxSize = 10 * fs.Gibi
	// serveDirCache is short because boxes on the engine host write past the server, which only learns of a change by listing.
	serveDirCache = time.Second
	// filePerms keeps scripts runnable, as WebDAV carries no file modes.
	filePerms = 0o777
)

func init() {
	// rclone adds its profile and stats flags to the global set, and cobra would show them on every dyna command.
	pflag.CommandLine = pflag.NewFlagSet("dyna", pflag.ContinueOnError)
}

// Serve serves root on a Unix socket until ctx ends. It leaves symlinks out: following one could reach past the rooms.
func Serve(ctx context.Context, root, socket string) error {
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	_, err := rc.Calls.Get("serve/start").Fn(ctx, rc.Params{"type": "webdav", "fs": ":local,skip_links:" + root, "addr": "unix://" + socket, "dir_cache_time": serveDirCache.String()})
	if err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// Mount mounts the server behind socket at dir and returns when it is unmounted. It needs no server yet: when the socket goes
// away and comes back, as it does when the engine restarts, a request waits for it, and boxes bound to the mount carry on.
func Mount(ctx context.Context, socket, dir, cache string) error {
	if err := config.SetCacheDir(cache); err != nil {
		return err
	}
	local := filepath.Join(cache, "relay.sock")
	if err := relay(ctx, local, socket, holdServer); err != nil {
		return err
	}
	remote, err := webdav.NewFs(ctx, "share", "", configmap.Simple{"url": "http://localhost", "vendor": "rclone", "unix_socket": local})
	if err != nil {
		return err
	}
	method, mount := mountlib.ResolveMountMethod("mount2")
	if mount == nil {
		return fmt.Errorf("no mount method %q", method)
	}
	options := mountlib.Opt
	options.AllowOther = true
	options.AttrTimeout = fs.Duration(attrTimeout)
	cached := vfscommon.Opt
	cached.CacheMode = vfscommon.CacheModeFull
	cached.CacheMaxSize = cacheMaxSize
	cached.DirCacheTime = fs.Duration(dirCacheTime)
	cached.WriteBack = fs.Duration(writeBack)
	cached.FilePerms = filePerms
	point := mountlib.NewMountPoint(mount, dir, remote, &options, &cached)
	if _, err := point.Mount(); err != nil {
		return err
	}
	return point.Wait()
}
