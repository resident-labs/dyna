package podman

import "github.com/resident-labs/dyna/contract"

// Creation can retry synchronously because it still owns the complete spec.
func recoverCreateCommand(id contract.BoxID, args []string) []string {
	script := recoverRemoving + `id=$1; shift
status=0
"$@" || status=$?
if [ "$status" = 125 ]; then
 recover_removing "$id" || exit "$status"
 exec "$@"
fi
exit "$status"`
	return append([]string{"bash", "-c", script, "dyna-recover-create", string(id)}, args...)
}

// recoverRemoving is run only inside the owning box namespace lock, after a
// lifecycle command fails. Both probes must identify that exact name. Store
// errors other than Podman's missing-container response fail closed; in
// particular exit 125 alone is never permission to remove a container.
// No volume, pod or other box is removed by recovery.
const recoverRemoving = `recover_removing() {
 local id=$1 listed state inspected status
 listed=$(podman --cgroup-manager=systemd ps --all --filter "name=$id" --format '{{.Names}}|{{.State}}') || return 1
 case "$listed" in
  "$id|removing") state=removing;;
  "$id|created"|"$id|configured"|"$id|exited"|"$id|stopped") state=stopped;;
  *) return 1;;
 esac
 status=0
 inspected=$(podman --cgroup-manager=systemd inspect --type container --format '{{.State.Status}}' "$id" 2>&1) || status=$?
 if [ "$status" = 0 ]; then
  [ "$state" = removing ] && [ "$inspected" = removing ] || return 1
 else
  case "$status" in 1|125) ;; *) return 1;; esac
  case "$inspected" in
   *container*'not known'*|*'no such container'*) ;;
   *) return 1;;
  esac
 fi
 podman --cgroup-manager=systemd rm -f "$id"
}
`
