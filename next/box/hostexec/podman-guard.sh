#!/bin/bash
# Host exec's podman (and docker) shim: refuses to remove or stop Dyna's own containers by accident.
# DYNA_FORCE=1 forces it. An absolute path to the real binary bypasses it, by design.
dir=$(dirname "$0")
tool=$(basename "$0")
real=$(PATH=${PATH//"$dir:"/} command -v "$tool") || { echo "$tool: not found" >&2; exit 127; }

args=("$@")
n=0
while [[ ${args[n]:-} == -* ]]; do
	case ${args[n]} in
	--connection | -c | --url | --identity | --log-level | --root | --runroot | --cgroup-manager | --runtime | --storage-driver) n=$((n + 2)) ;;
	*) n=$((n + 1)) ;;
	esac
done
w1=${args[n]:-}
w2=${args[n + 1]:-}
kind=container
rest=("${args[@]:n+1}")
case $w1 in
rm | stop | kill | restart) verb=$w1 ;;
container | pod | volume)
	kind=$w1 verb=$w2 rest=("${args[@]:n+2}") ;;
system) kind=system verb=$w2 ;;
*) exec "$real" "$@" ;;
esac
case $kind:$verb in
container:rm | container:stop | container:kill | container:restart | pod:rm | pod:stop | pod:kill | pod:restart | volume:rm) ;;
*:prune | system:reset | system:migrate)
	echo "Stopped: '$tool $*' would remove Dyna containers that run other sessions and rooms. Continuing could destroy every other Dyna room on this host. Never force a bulk or prune command; DYNA_FORCE=1 does not apply to it." >&2
	exit 125 ;;
*) exec "$real" "$@" ;;
esac

all=0 targets=() filters=()
for ((i = 0; i < ${#rest[@]}; i++)); do
	case ${rest[i]} in
	--) targets+=("${rest[@]:i+1}"); break ;;
	--all | --all=*) [[ ${rest[i]} == --all=false || ${rest[i]} == --all=0 ]] || all=1 ;;
	--latest | --latest=*) [[ ${rest[i]} == --latest=false || ${rest[i]} == --latest=0 ]] || { all=1; filters+=(--latest); } ;;
	--filter) filters+=(--filter "${rest[i + 1]:-}"); all=1; i=$((i + 1)) ;;
	--filter=*) filters+=(--filter "${rest[i]#--filter=}"); all=1 ;;
	-t | --time | -s | --signal) i=$((i + 1)) ;;
	--*) ;;
	# A short cluster (-fa, -a=true) with an a or l anywhere counts as bulk. A stray match in a value such as -sKILL only refuses more.
	-[a-zA-Z]*)
		[[ ${rest[i]} == *a* ]] && all=1
		[[ ${rest[i]} == *l* ]] && all=1 filters+=(--latest) ;;
	*) targets+=("${rest[i]}") ;;
	esac
done
if ((all)); then
	if [ "${DYNA_FORCE:-}" = 1 ]; then
		echo "Stopped: '$tool $*' takes its targets from a bulk list. DYNA_FORCE=1 applies only to containers named in the arguments, for example DYNA_FORCE=1 $tool rm -f <name>." >&2
		exit 125
	fi
	case $kind in
	container) list=(ps -a -q) ;;
	pod) list=(pod ps -q) ;;
	volume) list=(volume ls -q) ;;
	esac
	listed=$("$real" "${list[@]}" "${filters[@]}" 2>/dev/null) || { echo "Stopped: could not list the containers '$tool $*' would hit, so it was not run." >&2; exit 125; }
	mapfile -t targets <<<"$listed"
fi
((${#targets[@]})) || exec "$real" "$@"
[ "${DYNA_FORCE:-}" = 1 ] && exec "$real" "$@"

labels='.Labels'
[ "$kind" = container ] && labels='.Config.Labels'
# One line per target: its name, then its labels. A target that does not exist is left to the real command to report.
hits=$("$real" "$kind" inspect --format "{{.Name}}{{range \$k, \$v := $labels}} {{\$k}}={{\$v}}{{end}}" "${targets[@]}" 2>/dev/null | grep -E ' dyna\.(box|ci|stack)|^dyna-box-' | sed -E 's/ dyna\.layout=[^ ]*//' | paste -sd';' - | sed 's/;/; /g')
if [ -n "$hits" ]; then
	echo "Stopped: this would remove Dyna containers that run other sessions and rooms ($hits). Continuing could destroy every other Dyna room on this host. If you really mean these, name them and rerun with DYNA_FORCE=1, for example DYNA_FORCE=1 $tool rm -f <name>. Never force a bulk or prune command." >&2
	exit 125
fi
exec "$real" "$@"
