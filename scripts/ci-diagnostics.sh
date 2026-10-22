#!/usr/bin/env bash
# Shared CI evidence. The failure path uses only Bash builtins and file reads.
ci_diag_init() {
  local LC_ALL=C existing=
  _ci_diag_limit=${1:-65536}
  _ci_diag_path="$RUNNER_TEMP/ci-diagnostics.jsonl"
  _ci_diag_full=false
  if [[ -f $_ci_diag_path ]]; then
    IFS= read -r -N 65536 existing < "$_ci_diag_path" || :
  else
    : > "$_ci_diag_path" || return 1
  fi
  _ci_diag_bytes=${#existing}
  if ((_ci_diag_bytes > _ci_diag_limit - 136)); then _ci_diag_full=true; fi
}

ci_diag_quote() {
  local LC_ALL=C value=${1:0:4096} char code i
  _ci_diag_quoted=
  for ((i=0; i<${#value}; i++)); do
    char=${value:i:1}
    case "$char" in
      '"') _ci_diag_quoted+='\"' ;;
      '\') _ci_diag_quoted+='\\' ;;
      *)
        printf -v code '%d' "'$char"
        if ((code < 32 || code > 126)); then
          printf -v char '\\u%04x' "$code"
        fi
        _ci_diag_quoted+=$char
        ;;
    esac
  done
}

ci_diag_record() {
  local kind=$1 key=$2 value=$3 quoted_key quoted_value line
  [[ $_ci_diag_full == false ]] || return 0
  ci_diag_quote "$key"; quoted_key=$_ci_diag_quoted
  ci_diag_quote "$value"; quoted_value=$_ci_diag_quoted
  printf -v line '{"kind":"%s","key":"%s","value":"%s"}' "$kind" "$quoted_key" "$quoted_value"
  # Reserve space for a final explicit truncation record.
  if ((_ci_diag_bytes + ${#line} + 1 > _ci_diag_limit - 136)); then
    printf '%s\n' '{"kind":"truncated","key":"report","value":"diagnostic section byte limit"}' >> "$_ci_diag_path" || :
    _ci_diag_full=true
    return 0
  fi
  printf '%s\n' "$line" >> "$_ci_diag_path" || :
  ((_ci_diag_bytes += ${#line} + 1))
  return 0
}

ci_diag_file() {
  local value=
  if [[ -r $2 ]]; then
    IFS= read -r -N 4096 value 2>/dev/null < "$2" || :
    ci_diag_record "$1" "$2" "$value"
  else
    ci_diag_record unavailable "$2" "not readable in this namespace"
  fi
}

ci_diag_env() {
  local file=$1 entry key value count=0
  [[ -r $file ]] || return 0
  # Redirect errors before opening a process file that may have disappeared.
  while IFS= read -r -d '' entry; do
    ((count+=1))
    if ((count > 256)); then
      ci_diag_record truncated "$file" "environment entry limit"
      break
    fi
    key=${entry%%=*}
    case "$key" in
      GOMAXPROCS|GOMEMLIMIT|GOFLAGS|CGO_ENABLED|CC|CXX|GOAMD64|GODEBUG|GORACE)
        value=${entry#*=}
        ci_diag_record environment "$file:$key" "${value:0:512}"
        ;;
    esac
  done 2>/dev/null < "$file" || :
}

ci_diag_snapshot() {
  local stage=$1 proc=$2 cgroup=$3 dir=$3 hierarchy controllers relative
  local status line summary count=0 visible=0 threads=0 value field
  [[ $_ci_diag_full == false ]] || return 0
  ci_diag_record snapshot "$stage" "time=$EPOCHREALTIME pid=$BASHPID uid=$EUID"
  ci_diag_file limits "$proc/self/limits"
  ci_diag_file cgroup "$proc/self/cgroup"
  ci_diag_env "$proc/self/environ"
  if [[ -r $proc/self/cgroup ]]; then
    while IFS=: read -r hierarchy controllers relative; do
      if [[ $hierarchy == 0 && -z $controllers && $relative == /* && $relative != *..* &&
            -r $cgroup$relative/pids.max ]]; then
        dir=$cgroup$relative
        dir=${dir%/}
        break
      fi
    done 2>/dev/null < "$proc/self/cgroup" || :
  fi
  ci_diag_record scope cgroups "current cgroup and up to eight visible ancestors; host ancestors may be hidden"
  for ((count=0; count<8; count++)); do
    for field in pids.current pids.max pids.peak pids.events pids.events.local memory.events; do
      ci_diag_file counter "$dir/$field"
    done
    [[ $dir != "$cgroup" && $dir == "$cgroup/"* ]] || break
    dir=${dir%/*}
  done
  [[ $stage != entry ]] || return 0
  count=0
  for status in "$proc"/[0-9]*/status; do
    [[ -r $status ]] || continue
    ((count+=1))
    if ((count > 64)); then
      ci_diag_record truncated processes "64 process samples; not a host-wide task total"
      break
    fi
    summary=
    while IFS= read -r line; do
      case "$line" in
        Name:*|Pid:*|PPid:*|State:*|Uid:*|Threads:*) summary+="${line:0:256}"$'\n' ;;
      esac
      if [[ $line == Threads:* ]]; then
        value=${line#Threads:}
        value=${value//[[:space:]]/}
        if [[ $value =~ ^[0-9]+$ ]]; then threads=$((threads + value)); fi
      fi
    done 2>/dev/null < "$status" || :
    ((visible+=1))
    ci_diag_record process "$status" "$summary"
    ci_diag_env "${status%/status}/environ"
  done
  ci_diag_record sample_total processes "sampled_processes=$visible sampled_threads=$threads"
}

# Toolchain commands run only at entry, while process creation is available.
# The read caps output, and timeout caps a misconfigured compiler command.
ci_diag_tool() {
  local name=$1
  shift
  _ci_diag_output=
  IFS= read -r -N 4096 _ci_diag_output < <(timeout 30s "$@" 2>&1) || :
  ci_diag_record toolchain "$name" "$_ci_diag_output"
}

ci_diag_exit() {
  local status=$1
  trap - EXIT
  if ((status != 0)); then
    # A nested CI phase may already have appended its failure evidence.
    if ci_diag_init 2>/dev/null; then
      ci_diag_record failure "$_ci_diag_phase" "exit=$status"
      ci_diag_snapshot failure /proc /sys/fs/cgroup
    fi
  fi
  return "$status"
}

ci_diagnostics_begin() {
  local -a compiler=()
  _ci_diag_phase="$1/$2"
  ci_diag_init 32768 2>/dev/null || return 0
  trap 'ci_diag_exit "$?"' EXIT
  if ((_ci_diag_bytes == 0)); then
    ci_diag_record format version "1; escaped byte values; at most 64 KiB"
    ci_diag_record phase "$_ci_diag_phase" "entry"
    ci_diag_snapshot entry /proc /sys/fs/cgroup
    ci_diag_tool go-environment go env -json GOVERSION GOROOT GOOS GOARCH GOAMD64 CGO_ENABLED CC CXX GOFLAGS GOTOOLCHAIN GOGCCFLAGS CGO_CFLAGS CGO_LDFLAGS GOEXPERIMENT
    ci_diag_tool cc-command go env CC
    # Do not evaluate shell text supplied through CC.
    if [[ $_ci_diag_output =~ ^[a-zA-Z0-9_./[:space:]-]+$ ]]; then
      read -r -a compiler <<< "$_ci_diag_output" || :
      if ((${#compiler[@]})); then
        ci_diag_tool cc-version "${compiler[@]}" --version
      fi
    else
      ci_diag_record unavailable cc-version "compiler command needs shell parsing; retained without execution"
    fi
  fi
}

# Record the effective settings at the actual CI Go invocation, including
# temporary cross-build assignments. No helper process is needed for this.
ci_diag_go() {
  local key
  if ci_diag_init 32768 2>/dev/null; then
    ci_diag_record invocation "$_ci_diag_phase" "go $*"
    for key in GOMAXPROCS GOMEMLIMIT GOFLAGS CGO_ENABLED CC CXX GOAMD64 GOOS GOARCH GODEBUG GORACE; do
      ci_diag_record child_environment "$key" "${!key-<unset>}"
    done
  fi
  command go "$@"
}
