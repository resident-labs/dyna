// Package browser packs and unpacks shared Chromium user-data-dir profiles,
// omitting caches and lock files while keeping login state.
package browser

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// excludedSegments are directory or file names omitted anywhere in a profile tar.
var excludedSegments = map[string]bool{
	"Cache":         true,
	"Code Cache":    true,
	"GPUCache":      true,
	"ShaderCache":   true,
	"GrShaderCache": true,
	"DawnCache":     true,
	"Crashpad":      true,
}

// ShouldExclude reports whether rel (a slash-separated path relative to the Chromium
// user-data-dir root) is left out of the profile tar.
//
// Left out: Cache, Code Cache, GPUCache, ShaderCache, GrShaderCache, DawnCache,
// Service Worker/CacheStorage, Service Worker/ScriptCache, Crashpad, and Singleton*
// lock files. Kept: Local Storage, IndexedDB, Service Worker/Database, and all other
// login state.
func ShouldExclude(rel string) bool {
	clean := path.Clean(strings.TrimPrefix(filepath.ToSlash(rel), "./"))
	if clean == "." || clean == "" {
		return false
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		if excludedSegments[part] || strings.HasPrefix(part, "Singleton") {
			return true
		}
		if part == "Service Worker" && i+1 < len(parts) {
			if parts[i+1] == "CacheStorage" || parts[i+1] == "ScriptCache" {
				return true
			}
		}
	}
	return false
}

// PackProfile writes a tar archive of dir to w, omitting excluded caches and locks.
// A missing dir produces an empty valid tar archive.
func PackProfile(dir string, w io.Writer) error {
	tw := tar.NewWriter(w)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return tw.Close()
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("browser profile %q is not a directory", dir)
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if ShouldExclude(slash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		mode := fi.Mode()
		switch {
		case mode.IsDir():
			hdr, err := tar.FileInfoHeader(fi, "")
			if err != nil {
				return err
			}
			hdr.Name = slash + "/"
			return tw.WriteHeader(hdr)
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			hdr, err := tar.FileInfoHeader(fi, target)
			if err != nil {
				return err
			}
			hdr.Name = slash
			return tw.WriteHeader(hdr)
		case mode.IsRegular():
			f, err := os.Open(p)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			defer f.Close()
			// Re-stat the open file so header size matches what we read.
			stat, err := f.Stat()
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(stat, "")
			if err != nil {
				return err
			}
			hdr.Name = slash
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			_, err = io.CopyN(tw, f, stat.Size())
			return err
		default:
			// Skip Unix sockets, FIFOs, and device nodes.
			return nil
		}
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// UnpackProfile extracts a tar stream from r into dir, rejecting path traversal and
// skipping excluded paths.
func UnpackProfile(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeXHeader || hdr.Typeflag == tar.TypeXGlobalHeader || hdr.Typeflag == tar.TypeGNULongName || hdr.Typeflag == tar.TypeGNULongLink {
			continue
		}
		if strings.Contains(hdr.Name, "\\") {
			return fmt.Errorf("invalid tar path %q", hdr.Name)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if name == "" || name == "." {
			continue
		}
		if strings.HasPrefix(name, "/") {
			return fmt.Errorf("absolute tar path %q", hdr.Name)
		}
		clean := path.Clean(name)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("tar path escapes root: %q", hdr.Name)
		}
		if ShouldExclude(clean) {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(clean))
		if dst != root && !strings.HasPrefix(dst, root+string(filepath.Separator)) {
			return fmt.Errorf("tar path escapes root: %q", hdr.Name)
		}
		if err := ensureSafeParents(root, filepath.Dir(dst)); err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			mode := fs.FileMode(hdr.Mode)&0777 | 0700
			if err := os.MkdirAll(dst, mode); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			mode := fs.FileMode(hdr.Mode)&0777 | 0600
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			link := hdr.Linkname
			if strings.HasPrefix(link, "/") || strings.Contains(link, "\\") {
				return fmt.Errorf("invalid symlink target %q for %q", link, hdr.Name)
			}
			resolved := path.Clean(path.Join(path.Dir(clean), link))
			if resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("symlink %q escapes root: %q", hdr.Name, link)
			}
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Symlink(link, dst); err != nil {
				return err
			}
		}
	}
}

// ensureSafeParents creates parent directories inside root and refuses any symlink along the way.
func ensureSafeParents(root, dir string) error {
	if dir == root {
		return nil
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory %q escapes root", dir)
	}
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(cur, 0700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("parent %q is not a directory", cur)
		}
	}
	return nil
}
