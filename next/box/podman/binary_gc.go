package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PruneBinaries keeps wanted, the two newest other SHA directories (mtime, then ascending SHA for ties),
// and every object mounted by any container, including stopped ones. Ambiguous legacy mounts keep the store. Inventory
// failures never permit deletion. Container creation must hold bin/.activation.lock while pinning a mount;
// the final inventory and deletion share that lock in one remote command, never across SSH calls.
func (h *Host) PruneBinaries(ctx context.Context, bin, wanted string) error {
	if err := binaryPath(bin, wanted); err != nil {
		return err
	}
	ctx = WithRemoteCancellation(ctx)
	out, err := h.run(ctx, "bash", "-c", binaryObjectsScript, "dyna-binary-objects", bin)
	if err != nil {
		return err
	}
	fields := bytes.Split(out, []byte{0})
	if len(fields) < 2 || len(fields)%2 != 0 || len(fields[len(fields)-1]) != 0 || !filepath.IsAbs(string(fields[0])) {
		return errors.New("invalid binary object inventory")
	}
	bin = string(fields[0]) // Physical path, also used when resolving container mount sources below.
	type object struct {
		sum, stamp string
		modified   time.Time
	}
	var objects []object
	for i := 1; i < len(fields)-1; i += 2 {
		sum, stamp := string(fields[i]), string(fields[i+1])
		if binaryPath(bin, sum) != nil {
			continue // Never prune non-SHA names or symlinks.
		}
		parts := strings.SplitN(stamp, ":", 3) // Device, inode, then nanosecond publication mtime.
		if len(parts) != 3 {
			return errors.New("invalid binary object metadata")
		}
		modified, err := time.Parse("2006-01-02 15:04:05.999999999 -0700", parts[2])
		if err != nil {
			return fmt.Errorf("invalid binary object mtime: %w", err)
		}
		if sum != wanted {
			objects = append(objects, object{sum, stamp, modified})
		}
	}
	refs, err := h.binaryMountSources(ctx, bin)
	if err != nil {
		return err
	}
	keep := map[string]bool{wanted: true}
	for _, source := range refs {
		if source == "/" || source == bin || strings.HasPrefix(bin, source+"/") {
			return nil // A full-store/ancestor mount cannot be assigned an immutable pin.
		}
		if relative, ok := strings.CutPrefix(source, bin+"/"); ok {
			sum, _, _ := strings.Cut(relative, "/")
			if binaryPath(bin, sum) != nil {
				return nil // current and legacy layouts are ambiguous, even if current resolves today.
			}
			keep[sum] = true
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].modified.Equal(objects[j].modified) {
			return objects[i].sum < objects[j].sum
		}
		return objects[i].modified.After(objects[j].modified)
	})
	args := []string{"bash", "-c", binaryDeleteScript, "dyna-prune-binaries", bin}
	for i, obj := range objects {
		if i >= 2 && !keep[obj.sum] {
			args = append(args, obj.sum, obj.stamp)
		}
	}
	if len(args) == 5 {
		return nil
	}
	_, err = h.run(ctx, args...)
	return err
}

// binaryMountSources owns the untrusted Podman JSON boundary. In particular a successful but partial
// inspect, a missing/null Mounts field, and a mismatched ID are not an inventory. No engine rows are used.
func (h *Host) binaryMountSources(ctx context.Context, bin string) ([]string, error) {
	out, err := h.run(ctx, "podman", "ps", "--all", "--no-trunc", "--format", "json")
	if err != nil {
		return nil, err
	}
	var listed []struct{ ID string }
	if err := json.Unmarshal(out, &listed); err != nil || listed == nil {
		return nil, errors.New("invalid binary GC container list")
	}
	if len(listed) == 0 {
		return nil, nil
	}
	ids := make(map[string]bool, len(listed))
	args := []string{"podman", "container", "inspect", "--"}
	for _, container := range listed {
		if binaryPath(bin, container.ID) != nil || ids[container.ID] {
			return nil, errors.New("invalid binary GC container ID")
		}
		ids[container.ID] = true
		args = append(args, container.ID)
	}
	out, err = h.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var inspected []struct {
		ID     string
		Mounts *[]struct {
			Type   string
			Source *string
		}
	}
	if err := json.Unmarshal(out, &inspected); err != nil || len(inspected) != len(ids) {
		return nil, errors.New("incomplete binary GC container inspect")
	}
	var sources []string
	for _, container := range inspected {
		if !ids[container.ID] || container.Mounts == nil {
			return nil, errors.New("invalid binary GC mount inventory")
		}
		delete(ids, container.ID)
		for _, mount := range *container.Mounts {
			if mount.Source == nil {
				return nil, errors.New("missing binary GC mount source")
			}
			source := *mount.Source
			if mount.Type == "tmpfs" && source == "" {
				continue
			}
			if (mount.Type != "bind" && mount.Type != "volume" && mount.Type != "tmpfs") || !filepath.IsAbs(source) || strings.ContainsRune(source, 0) {
				return nil, errors.New("invalid binary GC mount source or type")
			}
			// Keep the lexical source as well as its physical path: current/legacy mounts remain
			// ambiguous even when a symlink currently resolves to a particular SHA.
			sources = append(sources, filepath.Clean(source))
		}
	}
	if len(sources) == 0 {
		return nil, nil
	}
	out, err = h.run(ctx, append([]string{"realpath", "-m", "-z", "--"}, sources...)...)
	if err != nil {
		return nil, err
	}
	physical := bytes.Split(out, []byte{0})
	if len(physical) != len(sources)+1 || len(physical[len(physical)-1]) != 0 {
		return nil, errors.New("invalid resolved binary GC mount inventory")
	}
	for i, path := range physical[:len(physical)-1] {
		if !filepath.IsAbs(string(path)) {
			return nil, errors.New("invalid resolved binary GC mount source")
		}
		resolved := string(path)
		if strings.HasPrefix(resolved, bin+"/") && !strings.HasPrefix(sources[i], bin+"/") {
			// An alias from outside the store may hide a legacy/current mount.
			return []string{bin}, nil
		}
		sources = append(sources, resolved)
	}
	return sources, nil
}

const binaryObjectsScript = `set -eu
cd -P -- "$1"
printf '%s\0' "$PWD"
for dir in "$PWD"/*; do
 if [ -d "$dir" ] && [ ! -L "$dir" ]; then
  printf '%s\0' "${dir##*/}"
  stat --printf '%d:%i:%y\0' -- "$dir"
 fi
done
`

// The locked pass has its own owning boundary: a controlled NUL-delimited Go template, with a
// matching ID, explicit non-null mounts marker and end marker for EACH requested container. Buffer
// every successful inspect before considering any deletion; process substitution would lose exit status.
const binaryDeleteScript = `set -eu
bin=$1
shift
cd -P -- "$bin"
exec 9> .activation.lock
flock -x 9
inventory="$DYNA_START_DIR/binary-mounts"
mkdir -m 700 -- "$inventory"
ids=$(podman ps --all --no-trunc --format '{{.ID}}')
declare -A seen=() keep=()
keep_all=0
retain() {
 local source=$1 relative sum
 while [[ "$source" != / && "$source" == */ ]]; do source=${source%/}; done
 if [[ "$source" == *//* || "$source/" == */./* || "$source/" == */../* ]]; then
  keep_all=1
 elif [[ "$source" == / || "$source" == "$bin" || "$bin" == "$source/"* ]]; then
  keep_all=1
 elif [[ "$source" == "$bin/"* ]]; then
  relative=${source#"$bin/"}
  sum=${relative%%/*}
  if [[ "$sum" =~ ^[0-9a-f]{64}$ ]]; then keep[$sum]=1; else keep_all=1; fi
 fi
}
format='{{printf "%s\x00" .ID}}{{if eq .Mounts nil}}{{printf "INVALID\x00"}}{{else}}{{printf "MOUNTS\x00"}}{{range .Mounts}}{{printf "M\x00%s\x00%s\x00" .Type .Source}}{{end}}{{printf "END\x00"}}{{end}}'
if [ -n "$ids" ]; then
 while IFS= read -r id; do
  [[ "$id" =~ ^[0-9a-f]{64}$ ]] || exit 1
  [[ ! ${seen[$id]+yes} ]] || exit 1
  seen[$id]=1
  file="$inventory/$id"
  podman container inspect --format "$format" -- "$id" > "$file"
  exec 8< "$file"
  IFS= read -r -d '' actual <&8
  [ "$actual" = "$id" ] || exit 1
  IFS= read -r -d '' marker <&8
  [ "$marker" = MOUNTS ] || exit 1
  while true; do
   IFS= read -r -d '' marker <&8
   if [ "$marker" = END ]; then break; fi
   [ "$marker" = M ] || exit 1
   IFS= read -r -d '' kind <&8
   IFS= read -r -d '' source <&8
   if [ "$kind" = tmpfs ] && [ -z "$source" ]; then continue; fi
   case "$kind" in bind|volume|tmpfs) ;; *) exit 1 ;; esac
   [[ "$source" == /* ]] || exit 1
   retain "$source"
   lexical=$source
   realpath -m -z -- "$source" > "$inventory/source"
   IFS= read -r -d '' source < "$inventory/source"
   if [[ "$source" == "$bin/"* && "$lexical" != "$bin/"* ]]; then keep_all=1; fi
   retain "$source"
  done
  tail=
  if IFS= read -r -d '' tail <&8; then exit 1; fi
  [[ "$tail" = '' || "$tail" = $'\n' ]] || exit 1
  exec 8<&-
 done <<< "$ids"
fi
[ "$keep_all" -eq 0 ] || exit 0
while [ "$#" -gt 0 ]; do
 sum=$1 stamp=$2
 shift 2
 if [[ ${keep[$sum]+yes} ]] || [ -L "$sum" ] || [ ! -d "$sum" ]; then continue; fi
 # A publication since selection is not an old object any more. Also avoid an active stager.
 [ "$(stat -c '%d:%i:%y' -- "$sum")" = "$stamp" ] || continue
 staging=("$sum"/.dyna.*)
 [ ! -e "${staging[0]}" ] || continue
 rm -rf -- "$sum"
done
`
