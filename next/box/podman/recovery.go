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
// particular exit 125 alone is never permission to remove a container. A storage
// error must name the full ID from that exact ps row, never a diagnostic-only ID.
// No volume, pod or other box is removed by recovery; rm always uses the box name.
const recoverRemoving = `recover_removing() {
 local id=$1 listed name state full_id extra inspected diagnostic error_file status
 listed=$(podman --cgroup-manager=systemd ps --all --no-trunc --filter "name=$id" --format '{{.Names}}|{{.State}}|{{.ID}}') || return 1
 IFS='|' read -r name state full_id extra <<< "$listed"
 [ "$name" = "$id" ] && [ "$listed" = "$id|$state|$full_id" ] || return 1
 [[ "$full_id" =~ ^[0-9a-f]{64}$ ]] || return 1
 case "$state" in
  removing|created|configured|exited|stopped) ;;
  *) return 1;;
 esac
 status=0
 # Keep stdout and stderr separate: Podman 6 can print [] on stdout for a
 # missing container. Sentinels retain trailing newlines in both streams.
 error_file=$(mktemp) || return 1
 inspected=$(podman --cgroup-manager=systemd inspect --type container --format '{{.State.Status}}' "$id" 2>"$error_file"; result=$?; printf x; exit "$result") || status=$?
 diagnostic=$(cat -- "$error_file"; result=$?; printf x; exit "$result") || { rm -f -- "$error_file"; return 1; }
 rm -f -- "$error_file" || return 1
 inspected=${inspected%x}
 inspected=${inspected%$'\n'}
 diagnostic=${diagnostic%x}
 diagnostic=${diagnostic%$'\n'}
 if [ "$status" = 0 ]; then
  [ "$inspected" = removing ] && [ -z "$diagnostic" ] || return 1
 else
  case "$status" in 1|125) ;; *) return 1;; esac
  case "$inspected" in ''|'[]') ;; *) return 1;; esac
  case "$diagnostic" in
   "Error: container $id not known"|"Error: no container with name or ID \"$id\" found: no such container"|"Error: no such container \"$id\""|"Error: getting container from store \"$full_id\": container not known") ;;
   *) return 1;;
  esac
 fi
 podman --cgroup-manager=systemd rm -f "$id"
}
`
