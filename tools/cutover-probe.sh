#!/usr/bin/env bash
# P3 per-harness cutover probe (plan P3.1–P3.6, spec AC-P3-04/AC-P3-10).
#
# Proves that ONE harness is actually wired to the engine and works through the
# real surfaces it uses — not that the config "looks right":
#
#   config.mcp        the harness mcpServers.my-memory command == engine binary
#                     (claude: <home>/.claude.json · omp: <home>/.omp/agent/mcp.json)
#   config.hooks      claude only: every memory hook command == `<engine> hook --agent claude --event …`
#   config.ext        omp only: extension symlink -> <engine repo>/harness/omp/my-memory.ts
#   config.no-dual    no hook still routes through the Python wrapper (mem-hook.sh)
#   config.no-creds   the my-memory block and hook strings carry no token/secret
#   config.no-sync    the hook path never invokes `mem sync` (no network)
#   mcp.handshake     spawn the CONFIGURED command: initialize + tools/list
#   mcp.tools         9 frozen tools served (internal/mcp/tools.frozen.json)
#   mcp.write         tools/call memory_add → isError false, record id returned
#   mcp.read          tools/call memory_search → the record just written is found
#   mcp.stdout        every server stdout line parsed as JSON-RPC (no noise)
#   stats.writes      stats shows source agent:claude writes rose by ≥1
#   hook.session-start hook exits 0 and injects a non-empty block
#   hook.user-prompt   hook exits 0
#   hook.stop          hook exits 0 (commits locally, never pushes)
#   push.count        0 push attempts observed through a counting git shim
#   data.verify       engine `verify` → 0 problems
#   data.records      record count rose by exactly the probe writes
#   data.remotes      `git for-each-ref refs/remotes` digest unchanged
#   guard.other       (optional) other harness configs unchanged vs a preflight hash file
#
# usage: tools/cutover-probe.sh --harness claude|omp [--live] [--home DIR]
#                               [--repo DIR] [--engine BIN] [--expect engine|python]
#                               [--python-home DIR] [--guard FILE] [--json]
#
#   --expect engine  (default) config must name the engine binary and the engine
#                    must serve exactly the frozen 9 tools.
#   --expect python  rollback proof: config must name the Python writer again and
#                    the Python server must still serve the frozen 9 (superset ok).
#   (no --live)  static/config checks only; nothing is executed or written.
#   --live       also spawns the MCP server (one synthetic record write into the
#                repo) and runs the three configured hooks against a disposable
#                clone of the repo.
#   --guard FILE TSV of `<path>\t<sha256>` recorded before the cutover; each file
#                must still hash the same (proves other harnesses were untouched).
#
# Exit: 0 all checks PASS, 1 any FAIL, 2 usage.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
harness=claude
live=0
config_home="${HOME}"
repo="${MEM_HOME:-$HOME/my-memory}"
engine_bin="$here/../bin/mem"
guard=""
as_json=0
expect="engine"
python_home="${HOME}/my-memory"

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --harness) harness="${2:-}"; shift 2 ;;
    --live) live=1; shift ;;
    --home) config_home="${2:-}"; shift 2 ;;
    --repo) repo="${2:-}"; shift 2 ;;
    --engine) engine_bin="${2:-}"; shift 2 ;;
    --expect) expect="${2:-}"; shift 2 ;;
    --python-home) python_home="${2:-}"; shift 2 ;;
    --guard) guard="${2:-}"; shift 2 ;;
    --json) as_json=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "cutover-probe: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$harness" in
  claude|omp) ;;
  *) echo "cutover-probe: harness '$harness' not implemented yet (implemented: claude, omp)" >&2
     exit 2 ;;
esac
case "$expect" in engine|python) ;; *) echo "cutover-probe: --expect engine|python" >&2; exit 2 ;; esac
if [ -d "$(dirname "$engine_bin")" ]; then
  engine_bin="$(cd "$(dirname "$engine_bin")" && pwd)/$(basename "$engine_bin")"
fi
[ -x "$engine_bin" ] || { echo "cutover-probe: engine binary not executable: $engine_bin" >&2; exit 2; }
active_bin="$engine_bin"
if [ "$expect" = "python" ]; then
  active_bin="$python_home/bin/mem"
  [ -f "$active_bin" ] || { echo "cutover-probe: python writer missing: $active_bin" >&2; exit 2; }
fi
[ -d "$repo" ] || { echo "cutover-probe: repo not found: $repo" >&2; exit 2; }
agent=claude
mcp_config="$config_home/.claude.json"
hook_config="$config_home/.claude/settings.json"
ext_link=""
if [ "$harness" = "omp" ]; then
  # omp (oh-my-pi): MCP lives in ~/.omp/agent/mcp.json; the hook surface is the
  # TS extension symlinked into the engine repo — there is no hooks JSON file.
  agent=oh-my-pi
  mcp_config="$config_home/.omp/agent/mcp.json"
  hook_config=""
  ext_link="$config_home/.omp/agent/extensions/my-memory.ts"
fi
[ -f "$mcp_config" ] || { echo "cutover-probe: missing $mcp_config" >&2; exit 1; }
if [ "$harness" = "claude" ]; then
  [ -f "$hook_config" ] || { echo "cutover-probe: missing $hook_config" >&2; exit 1; }
else
  [ -L "$ext_link" ] || { echo "cutover-probe: extension symlink missing: $ext_link" >&2; exit 1; }
fi

tmp="$(mktemp -d -t cutover-probe)"
trap 'rm -rf "$tmp"' EXIT

names=()
statuses=()
details=()
failed=0

report() { # name status detail
  names+=("$1"); statuses+=("$2"); details+=("$3")
  [ "$2" = "PASS" ] || failed=1
  if [ "$as_json" = 0 ]; then
    printf 'CHECK %-18s %-4s %s\n' "$1" "$2" "$3"
  fi
}

run_checks() { # pairs of "name<TAB>PASS|FAIL<TAB>detail" on stdin
  while IFS=$'\t' read -r n s d; do
    [ -n "$n" ] || continue
    report "$n" "$s" "$d"
  done
}

digest_remotes() {
  git -C "$repo" for-each-ref refs/remotes 2>/dev/null | shasum -a 256 | cut -d' ' -f1
}

stat_field() { # field [agent] -> value from engine stats --json
  "$active_bin" --home "$repo" stats --json 2>/dev/null | python3 -c '
import json,sys
field=sys.argv[1]
agent=sys.argv[2] if len(sys.argv)>2 else "claude"
try: d=json.load(sys.stdin)
except Exception: print(""); raise SystemExit
if field=="records": print(d.get("records",""))
elif field=="claude_writes":
    for h in d.get("harness") or []:
        if h.get("agent")==agent: print(h.get("writes",0)); break
    else: print("")
' "$1" "${2:-$agent}"
}

records_before="$(stat_field records)"
writes_before="$(stat_field claude_writes)"
remotes_before="$(digest_remotes)"

# ---------------------------------------------------------------------------
# Static config checks
# ---------------------------------------------------------------------------
python3 - "$mcp_config" "$hook_config" "$engine_bin" "$expect" "$python_home" "$agent" "$ext_link" <<'PY' > "$tmp/config.out" 2>&1
import json, os, re, sys
mcp_path, hook_path, engine, expect, python_home, agent, ext_link = sys.argv[1:8]
EVENTS = {"SessionStart": "session-start", "UserPromptSubmit": "user-prompt", "Stop": "stop"}
SECRET = [
    re.compile(r"sk-[A-Za-z0-9]{16,}"),
    re.compile(r"ghp_[A-Za-z0-9]{20,}"),
    re.compile(r"apikey_[A-Za-z0-9]{16,}"),
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY"),
]

def emit(name, ok, detail):
    print("%s\t%s\t%s" % (name, "PASS" if ok else "FAIL", detail.replace("\t", " ").replace("\n", " ")))

try:
    mcp = json.load(open(mcp_path, encoding="utf-8"))
    hooks = json.load(open(hook_path, encoding="utf-8")) if hook_path else None
except Exception as err:
    emit("config.parse", False, "JSON parse failed: %s" % err)
    raise SystemExit(0)

def want_mcp():
    if expect == "python":
        return os.path.join(python_home, "bin", "mem")
    return engine

def want_hook(event):
    mem_event = EVENTS[event]
    if expect == "python":
        return "%s claude %s" % (os.path.join(python_home, "harness", "mem-hook.sh"), mem_event)
    return "%s hook --agent claude --event %s" % (engine, mem_event)

entry = ((mcp.get("mcpServers") or {}).get("my-memory") or {})
cmd = entry.get("command")
args = entry.get("args") or []
env = entry.get("env") or {}

if agent == "oh-my-pi":
    # omp: hook surface = the TS extension symlink; there is no hooks JSON.
    repo = os.path.dirname(os.path.dirname(engine))
    def want_ext():
        if expect == "python":
            return os.path.join(python_home, "harness", "omp", "my-memory.ts")
        return os.path.join(repo, "harness", "omp", "my-memory.ts")
    want = want_ext()
    try:
        ext = os.readlink(ext_link)
    except Exception as err:
        ext = ""
        emit("config.ext", False, "readlink failed: %s" % err)
    emit("config.mcp", cmd == want_mcp() and args == ["mcp"] and env.get("MEM_AGENT") == agent,
         "command=%r args=%r MEM_AGENT=%r (want %r)" % (cmd, args, env.get("MEM_AGENT"), want_mcp()))
    if ext:
        ext_ok = ext == want and os.path.isfile(want)
        src = ""
        if os.path.isfile(want):
            try:
                src = open(want, encoding="utf-8").read()
            except Exception:
                src = ""
                ext_ok = False
        emit("config.ext", ext_ok, "symlink -> %s (want %s)" % (ext, want))
        dual = []
        if expect == "engine":
            if ext.startswith(os.path.join(python_home, "harness") + os.sep):
                dual.append(ext)
            if cmd == os.path.join(python_home, "bin", "mem"):
                dual.append(cmd)
        emit("config.no-dual", not dual, "python writer still wired: %d" % len(dual))
        blob = json.dumps(entry, ensure_ascii=False) + " " + src
        hits = [p.pattern for p in SECRET if p.search(blob)]
        emit("config.no-creds", not hits, "secret-shaped strings: %s" % (hits or "none"))
        sync_hits = re.findall(r"[\"']sync[\"']", src)
        emit("config.no-sync", not sync_hits, "extension invoking sync: %d" % len(sync_hits))
    raise SystemExit(0)

emit("config.mcp", cmd == want_mcp() and args == ["mcp"] and env.get("MEM_AGENT") == "claude",
     "command=%r args=%r MEM_AGENT=%r (want %r)" % (cmd, args, env.get("MEM_AGENT"), want_mcp()))

hook_cmds = {}
dual = []
for event, mem_event in EVENTS.items():
    found = []
    for group in (hooks.get("hooks") or {}).get(event) or []:
        for h in (group or {}).get("hooks") or []:
            c = h.get("command") if isinstance(h, dict) else None
            if not isinstance(c, str):
                continue
            if "mem-hook.sh" in c or " hook --agent " in c:
                found.append(c)
    hook_cmds[event] = found
    if expect == "engine" and any("mem-hook.sh" in c for c in found):
        dual.extend(found)

want = {e: want_hook(e) for e in EVENTS}
mismatch = [e for e in EVENTS if hook_cmds.get(e) != [want[e]]]
emit("config.hooks", not mismatch,
     "all three hooks == %s command" % expect if not mismatch else
     "mismatch=%s got=%s" % (mismatch, {e: hook_cmds.get(e) for e in mismatch}))
emit("config.no-dual", not dual, "python wrapper hooks present: %d" % len(dual))

blob = json.dumps(entry, ensure_ascii=False)
for event in EVENTS:
    blob += " " + " ".join(hook_cmds.get(event) or [])
hits = [p.pattern for p in SECRET if p.search(blob)]
emit("config.no-creds", not hits, "secret-shaped strings: %s" % (hits or "none"))
sync = [c for c in sum(hook_cmds.values(), []) if re.search(r"\bmem\b.*\bsync\b", c)]
emit("config.no-sync", not sync, "hook commands invoking sync: %d" % len(sync))
PY
run_checks < "$tmp/config.out"

# ---------------------------------------------------------------------------
# Guard: other harness configs unchanged
# ---------------------------------------------------------------------------
if [ -n "$guard" ]; then
  if [ ! -f "$guard" ]; then
    report guard.other FAIL "guard file missing: $guard"
  else
    changed=""
    while IFS=$'\t' read -r path want_hash; do
      [ -n "$path" ] || continue
      got_hash="$(shasum -a 256 "$path" 2>/dev/null | cut -d' ' -f1)"
      [ "$got_hash" = "$want_hash" ] || changed="$changed $(basename "$path")"
    done < "$guard"
    report guard.other "$([ -z "$changed" ] && echo PASS || echo FAIL)" \
      "$([ -z "$changed" ] && echo "all $(grep -c . "$guard") files unchanged" || echo "changed:$changed")"
  fi
fi

# ---------------------------------------------------------------------------
# Live: MCP handshake + write + read through the CONFIGURED command
# ---------------------------------------------------------------------------
if [ "$live" = 1 ]; then
  frozen="$here/../internal/mcp/tools.frozen.json"
  probe_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  MEM_HOME="$repo" python3 - "$mcp_config" "$repo" "$frozen" "$probe_id" "$expect" "$tmp" "$agent" <<'PY' > "$tmp/mcp.out" 2>"$tmp/mcp.err"
import json, os, re, select, subprocess, sys, time

mcp_path, repo, frozen_path, probe_id, expect, work, agent = sys.argv[1:8]
entry = (json.load(open(mcp_path, encoding="utf-8")).get("mcpServers") or {}).get("my-memory") or {}
argv = [entry.get("command")] + list(entry.get("args") or [])
env = dict(os.environ)
env.update({str(k): str(v) for k, v in (entry.get("env") or {}).items()})
env["MEM_HOME"] = repo

expected = None
if frozen_path and os.path.exists(frozen_path):
    expected = [t["name"] for t in json.load(open(frozen_path, encoding="utf-8"))]

def emit(name, ok, detail):
    print("%s\t%s\t%s" % (name, "PASS" if ok else "FAIL", str(detail).replace("\t", " ").replace("\n", " ")[:400]))

proc = None
try:
    proc = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, env=env, bufsize=0)
except Exception as err:
    emit("mcp.handshake", False, "cannot spawn %r: %s" % (argv, err))
    raise SystemExit(0)

buf = b""
noise = []

def read_frame(timeout=30):
    global buf
    end = time.time() + timeout
    while True:
        while b"\n" in buf:
            line, buf = buf.split(b"\n", 1)
            if not line.strip():
                continue
            try:
                return json.loads(line.decode("utf-8"))
            except Exception:
                noise.append(line[:120].decode("utf-8", "replace"))
                continue
        left = end - time.time()
        if left <= 0:
            raise TimeoutError("no frame within %.0fs" % timeout)
        r, _, _ = select.select([proc.stdout.fileno()], [], [], left)
        if not r:
            raise TimeoutError("no frame within %.0fs" % timeout)
        chunk = os.read(proc.stdout.fileno(), 65536)
        if not chunk:
            raise EOFError("server closed stdout")
        buf += chunk

def send(obj):
    proc.stdin.write((json.dumps(obj) + "\n").encode())
    proc.stdin.flush()

try:
    send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
          "params": {"protocolVersion": "2025-06-18", "capabilities": {},
                     "clientInfo": {"name": "cutover-probe", "version": "1"}}})
    init = read_frame()
    result = init.get("result") or {}
    emit("mcp.handshake", bool(result.get("protocolVersion")) and "instructions" in result,
         "protocolVersion=%r instructions=%s" % (result.get("protocolVersion"), "yes" if "instructions" in result else "no"))
    send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    send({"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
    tools = [t["name"] for t in ((read_frame().get("result") or {}).get("tools") or [])]
    want = expected or ["memory_context", "memory_search", "memory_show", "memory_add",
                        "memory_list", "memory_verify", "memory_stats", "memory_sync", "memory_trace"]
    if expect == "python":
        ok = set(want) <= set(tools)  # the Python writer may serve a superset
    else:
        ok = sorted(tools) == sorted(want)  # the frozen contract is exact
    emit("mcp.tools", ok, "served=%d want=%d missing=%s extra=%s"
         % (len(tools), len(want), sorted(set(want) - set(tools)), sorted(set(tools) - set(want))))

    text = ("P3 cutover probe for %s via the engine write path; probe id %s" % (agent, probe_id))
    send({"jsonrpc": "2.0", "id": 3, "method": "tools/call",
          "params": {"name": "memory_add", "arguments": {"text": text, "title": "P3.1 cutover probe %s" % probe_id}}})
    add = read_frame()
    res = add.get("result") or {}
    body = " ".join(c.get("text", "") for c in (res.get("content") or []) if isinstance(c, dict))
    ids = re.findall(r"\b(?:lesson|knowledge|decision|project|handoff|rule)\.[0-9a-f]{8,16}\b", body)
    emit("mcp.write", add.get("error") is None and not res.get("isError") and bool(ids),
         "isError=%r ids=%s" % (res.get("isError"), ids[:2]))
    rec_id = ids[0] if ids else ""

    if rec_id:
        send({"jsonrpc": "2.0", "id": 4, "method": "tools/call",
              "params": {"name": "memory_search", "arguments": {"query": "cutover probe %s" % probe_id, "limit": 3}}})
        search = read_frame()
        sbody = " ".join(c.get("text", "") for c in ((search.get("result") or {}).get("content") or []) if isinstance(c, dict))
        emit("mcp.read", rec_id in sbody, "search returned written id=%s" % rec_id)
    else:
        emit("mcp.read", False, "no record id from memory_add — cannot verify read path")
except Exception as err:
    emit("mcp.handshake", False, "%s: %s" % (type(err).__name__, err))
    try:
        emit("mcp.tools", False, "not reached")
        emit("mcp.write", False, "not reached")
        emit("mcp.read", False, "not reached")
    except Exception:
        pass
finally:
    try:
        proc.stdin.close()
    except Exception:
        pass
    try:
        code = proc.wait(timeout=20)
    except Exception:
        proc.kill(); code = -1
    err = proc.stderr.read().decode("utf-8", "replace") if proc.stderr else ""
    if err.strip():
        open(os.path.join(work, "mcp-server-stderr.log"), "a").write(err)
    emit("mcp.stdout", not noise, "unparsable stdout lines: %d %s" % (len(noise), noise[:2]))
    if code != 0:
        emit("mcp.exit", False, "server exit code %s (stderr: %s)" % (code, err.strip().splitlines()[-1] if err.strip() else "-"))
PY
  run_checks < "$tmp/mcp.out"

  # -------------------------------------------------------------------------
  # Live: hooks, run exactly as configured, against a disposable clone
  # -------------------------------------------------------------------------
  hook_repo="$tmp/repo"
  if git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then
    git clone -q --shared "$repo" "$hook_repo" >/dev/null 2>&1 || cp -R "$repo" "$hook_repo"
    git -C "$hook_repo" remote remove origin >/dev/null 2>&1 || true
  else
    cp -R "$repo" "$hook_repo"
  fi

  mkdir -p "$tmp/bin"
  cat > "$tmp/bin/git" <<SH
#!/bin/sh
for arg in "\$@"; do
  if [ "\$arg" = "push" ]; then echo "\$*" >> "$tmp/push.log"; fi
done
exec /usr/bin/git "\$@"
SH
  chmod +x "$tmp/bin/git"
  : > "$tmp/push.log"

  hook_cmd() {
    python3 - "$hook_config" "$1" <<'PY'
import json, sys
path, event = sys.argv[1:3]
doc = json.load(open(path, encoding="utf-8"))
for group in (doc.get("hooks") or {}).get(event) or []:
    for h in (group or {}).get("hooks") or []:
        c = h.get("command") if isinstance(h, dict) else None
        if isinstance(c, str) and ("mem-hook.sh" in c or " hook --agent " in c):
            print(c); raise SystemExit
PY
  }

  exec_hook() { # label cmd payload-file require-output
    label="$1"; cmd="$2"
    out="$tmp/$label.out"; err="$tmp/$label.err"
    MEM_HOME="$hook_repo" PATH="$tmp/bin:$PATH" sh -c "$cmd" < "$3" > "$out" 2> "$err"
    code=$?
    chars=$(wc -c < "$out" | tr -d ' ')
    if [ "$code" -ne 0 ]; then
      report "$label" FAIL "exit=$code cmd=$cmd stderr=$(tail -1 "$err" 2>/dev/null | cut -c1-160)"
    elif [ "$4" = "require" ] && [ "$chars" -eq 0 ]; then
      report "$label" FAIL "exit=0 but injected nothing (cmd=$cmd)"
    else
      report "$label" PASS "exit=0 stdout=${chars}B cmd=$cmd"
    fi
  }

  run_hook() { # event payload-file label require-output (claude: cmd lives in settings.json)
    cmd="$(hook_cmd "$1")"
    if [ -z "$cmd" ]; then
      report "$3" FAIL "no memory hook configured for $1"
      return
    fi
    exec_hook "$3" "$cmd" "$2" "$4"
  }

  if [ "$harness" = "omp" ]; then
    # omp extension contract: EMPTY stdin for session-start/stop; JSON {prompt,
    # session_id} for user-prompt (my-memory.ts callHook).
    : > "$tmp/ss.json"
    printf '%s' '{"session_id":"cutover-probe","prompt":"my-memory engine cutover probe recall check"}' > "$tmp/up.json"
    : > "$tmp/st.json"
    if [ "$expect" = "engine" ]; then
      cmd_ss="$engine_bin hook --agent $agent --event session-start"
      cmd_up="$engine_bin hook --agent $agent --event user-prompt"
      cmd_st="$engine_bin hook --agent $agent --event stop"
    else
      cmd_ss="$python_home/harness/mem-hook.sh $agent session-start"
      cmd_up="$python_home/harness/mem-hook.sh $agent user-prompt"
      cmd_st="$python_home/harness/mem-hook.sh $agent stop"
    fi
    exec_hook hook.session-start "$cmd_ss" "$tmp/ss.json" require
    exec_hook hook.user-prompt "$cmd_up" "$tmp/up.json" optional
    exec_hook hook.stop "$cmd_st" "$tmp/st.json" optional
  else
    printf '%s' '{"session_id":"cutover-probe","transcript_path":"","cwd":"'"$hook_repo"'","hook_event_name":"SessionStart","source":"startup"}' > "$tmp/ss.json"
    printf '%s' '{"session_id":"cutover-probe","cwd":"'"$hook_repo"'","hook_event_name":"UserPromptSubmit","prompt":"my-memory engine cutover probe recall check"}' > "$tmp/up.json"
    printf '%s' '{"session_id":"cutover-probe","cwd":"'"$hook_repo"'","hook_event_name":"Stop","stop_hook_active":false}' > "$tmp/st.json"

    run_hook SessionStart "$tmp/ss.json" hook.session-start require
    run_hook UserPromptSubmit "$tmp/up.json" hook.user-prompt optional
    run_hook Stop "$tmp/st.json" hook.stop optional
  fi

  pushes=$(wc -l < "$tmp/push.log" | tr -d ' ')
  report push.count "$([ "$pushes" -eq 0 ] && echo PASS || echo FAIL)" "push attempts observed: $pushes"
fi

# ---------------------------------------------------------------------------
# Data integrity
# ---------------------------------------------------------------------------
verify_out="$tmp/verify.out"
"$active_bin" --home "$repo" verify > "$verify_out" 2>&1
verify_code=$?
problems="$(grep -o '[0-9]\+ problems' "$verify_out" | tail -1 | cut -d' ' -f1)"
if [ "$verify_code" -eq 0 ] && [ "$problems" = "0" ]; then
  report data.verify PASS "exit=0 problems=0"
else
  report data.verify FAIL "exit=$verify_code problems=${problems:-unknown}"
fi

records_after="$(stat_field records)"
writes_after="$(stat_field claude_writes)"
if [ "$live" = 1 ]; then
  expected_delta=1
else
  expected_delta=0
fi
if [ -n "$records_before" ] && [ -n "$records_after" ] && [ $((records_after - records_before)) -eq "$expected_delta" ]; then
  report data.records PASS "records ${records_before} -> ${records_after} (delta ${expected_delta})"
else
  report data.records FAIL "records ${records_before:-?} -> ${records_after:-?} (want delta ${expected_delta})"
fi
if [ "$live" = 1 ]; then
  if [ -n "$writes_after" ] && [ "$writes_after" -gt 0 ] && [ "$writes_after" -gt "${writes_before:-0}" ]; then
    report stats.writes PASS "agent:$agent writes ${writes_before:-0} -> ${writes_after}"
  else
    report stats.writes FAIL "agent:$agent writes ${writes_before:-0} -> ${writes_after:-?} (must rise)"
  fi
fi

remotes_after="$(digest_remotes)"
if [ "$remotes_before" = "$remotes_after" ]; then
  report data.remotes PASS "refs/remotes digest ${remotes_after:0:12} unchanged"
else
  report data.remotes FAIL "refs/remotes digest ${remotes_before:0:12} -> ${remotes_after:0:12}"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
passes=0
for s in "${statuses[@]}"; do [ "$s" = "PASS" ] && passes=$((passes + 1)); done
total=${#statuses[@]}

if [ "$as_json" = 1 ]; then
  printf '{\n  "harness": "%s",\n  "live": %s,\n  "engine": "%s",\n  "checks": [\n' "$harness" "$([ "$live" = 1 ] && echo true || echo false)" "$engine_bin"
  i=0
  while [ "$i" -lt "$total" ]; do
    sep=","; [ "$i" -eq $((total - 1)) ] && sep=""
    d="${details[$i]}"
    d="${d//\\/\\\\}"; d="${d//\"/\\\"}"
    printf '    {"name": "%s", "status": "%s", "detail": "%s"}%s\n' "${names[$i]}" "${statuses[$i]}" "$d" "$sep"
    i=$((i + 1))
  done
  printf '  ],\n  "passed": %d,\n  "total": %d,\n  "result": "%s"\n}\n' "$passes" "$total" "$([ "$failed" -eq 0 ] && echo PASS || echo FAIL)"
else
  if [ "$failed" -eq 0 ]; then
    printf 'PROBE %s PASS %d/%d checks (live=%s)\n' "$harness" "$passes" "$total" "$live"
  else
    printf 'PROBE %s FAIL %d/%d checks (live=%s)\n' "$harness" "$passes" "$total" "$live"
  fi
fi

[ "$failed" -eq 0 ] && exit 0 || exit 1
