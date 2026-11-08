#!/usr/bin/env bash
# P3 staged-cutover switcher: moves ONE harness's MCP + hook config between the
# Python writer (pre-cutover) and the engine writer (post-cutover), and back.
#
# The switch is surgical: only the exact command strings that name the memory
# binary are rewritten, every other byte of the config file is left alone. That
# is what makes the rollback provably byte-exact (`cutover-switch.sh --to
# python` restores the same bytes `--to engine` replaced) — see P3 spec §5.4.
#
# usage: tools/cutover-switch.sh --to engine|python|broken [--harness claude|omp]
#                               [--home DIR] [--engine BIN] [--python-home DIR]
#                               [--backup] [--dry-run]
#
#   --home DIR       config root to edit (default: $HOME). Point it at a sandbox
#                    to rehearse the cutover/rollback drill without touching the
#                    real harness config.
#   --engine BIN     engine binary the config should name (default: ../bin/mem).
#   --python-home    the Python memory home (default: $HOME/my-memory).
#   --to broken      negative control: same shape as engine but a path that does
#                    not exist, so `cutover-probe.sh --live` must FAIL.
#   --backup         copy each config to <file>.bak-<UTC stamp> before editing.
#   --dry-run        print the planned replacements, write nothing.
#
# Surfaces per harness: claude = ~/.claude.json + ~/.claude/settings.json (4
# command strings). omp = ~/.omp/agent/mcp.json (MCP command) + the extension
# symlink ~/.omp/agent/extensions/my-memory.ts (repointed to the writer's own
# harness/omp/my-memory.ts; rollback restores the exact link target).
#
# Exit codes: 0 success/already-in-that-mode, 1 failure (unknown config shape,
# JSON invalid after write), 2 usage.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
harness=claude
to=""
home_dir="${HOME}"
engine_bin=""
python_home="${HOME}/my-memory"
backup=0
dry=0

usage() {
  sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --harness) harness="${2:-}"; shift 2 ;;
    --to) to="${2:-}"; shift 2 ;;
    --home) home_dir="${2:-}"; shift 2 ;;
    --engine) engine_bin="${2:-}"; shift 2 ;;
    --python-home) python_home="${2:-}"; shift 2 ;;
    --backup) backup=1; shift ;;
    --dry-run) dry=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "cutover-switch: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$to" in
  engine|python|broken) ;;
  *) echo "cutover-switch: --to engine|python|broken is required" >&2; exit 2 ;;
esac
case "$harness" in
  claude|omp) ;;
  *) # Other harnesses add their own config surfaces; refusing beats guessing a schema.
     echo "cutover-switch: harness '$harness' not implemented yet (implemented: claude, omp)" >&2
     exit 2 ;;
esac

[ -n "$engine_bin" ] || engine_bin="$here/../bin/mem"
if [ -d "$(dirname "$engine_bin")" ]; then
  engine_bin="$(cd "$(dirname "$engine_bin")" && pwd)/$(basename "$engine_bin")"
fi
if [ "$to" = "engine" ] && [ ! -x "$engine_bin" ]; then
  echo "cutover-switch: engine binary not executable: $engine_bin" >&2
  exit 1
fi

ext_link=""
if [ "$harness" = "omp" ]; then
  # omp: MCP JSON + the TS extension symlink (hook surface). No hooks JSON exists.
  mcp_config="$home_dir/.omp/agent/mcp.json"
  hook_config=""
  ext_link="$home_dir/.omp/agent/extensions/my-memory.ts"
  [ -f "$mcp_config" ] || { echo "cutover-switch: missing config: $mcp_config" >&2; exit 1; }
  [ -L "$ext_link" ] || { echo "cutover-switch: extension symlink missing: $ext_link" >&2; exit 1; }
else
  mcp_config="$home_dir/.claude.json"
  hook_config="$home_dir/.claude/settings.json"
  for f in "$mcp_config" "$hook_config"; do
    if [ ! -f "$f" ]; then
      echo "cutover-switch: missing config: $f" >&2
      exit 1
    fi
  done
fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [ "$backup" = 1 ] && [ "$dry" = 0 ]; then
  for f in "$mcp_config" "$hook_config"; do
    [ -n "$f" ] || continue
    cp -p "$f" "$f.bak-$stamp"
    echo "BACKUP $f.bak-$stamp sha256=$(shasum -a 256 "$f.bak-$stamp" | cut -d' ' -f1)"
  done
  if [ -n "$ext_link" ]; then
    # cp -P keeps the link itself, so the pre-switch target is preserved byte-exact.
    cp -P "$ext_link" "$ext_link.bak-$stamp"
    echo "BACKUP $ext_link.bak-$stamp target=$(readlink "$ext_link.bak-$stamp")"
  fi
fi

exec python3 - "$to" "$home_dir" "$engine_bin" "$python_home" "$dry" "$mcp_config" "$hook_config" "$harness" "$ext_link" <<'PY'
import hashlib
import json
import os
import sys

to, home_dir, engine, python_home, dry, mcp_path, hook_path, harness, ext_link = sys.argv[1:10]
dry = dry == "1"
broken = os.path.join(home_dir, ".cutover-broken-engine", "mem")

EVENTS = ["SessionStart", "UserPromptSubmit", "Stop"]
HOOK_EVENTS = {"SessionStart": "session-start", "UserPromptSubmit": "user-prompt", "Stop": "stop"}


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()


def mcp_command(doc):
    entry = (doc.get("mcpServers") or {}).get("my-memory")
    if not isinstance(entry, dict):
        raise SystemExit("cutover-switch: %s has no mcpServers.my-memory entry" % mcp_path)
    cmd = entry.get("command")
    if not isinstance(cmd, str) or not cmd:
        raise SystemExit("cutover-switch: mcpServers.my-memory has no command string")
    return cmd


def hook_commands(doc):
    out = {}
    hooks = doc.get("hooks") or {}
    for event in EVENTS:
        found = []
        for group in hooks.get(event) or []:
            for h in (group or {}).get("hooks") or []:
                if isinstance(h, dict) and isinstance(h.get("command"), str):
                    cmd = h["command"]
                    if "mem-hook.sh" in cmd or " hook --agent " in cmd:
                        found.append(cmd)
        if not found:
            raise SystemExit("cutover-switch: %s has no memory hook under %s" % (hook_path, event))
        if len(found) > 1:
            raise SystemExit(
                "cutover-switch: %s has %d memory hooks under %s (dual writer) — resolve by hand"
                % (hook_path, len(found), event)
            )
        out[event] = found[0]
    return out


mcp_doc = json.loads(read(mcp_path))
hook_doc = json.loads(read(hook_path)) if hook_path else None
cur_mcp = mcp_command(mcp_doc)
cur_hooks = hook_commands(hook_doc) if hook_doc else None


def variant(mode, event=None):
    if mode == "engine":
        return engine if event is None else "%s hook --agent claude --event %s" % (engine, HOOK_EVENTS[event])
    if mode == "python":
        if event is None:
            return os.path.join(python_home, "bin", "mem")
        return "%s claude %s" % (os.path.join(python_home, "harness", "mem-hook.sh"), HOOK_EVENTS[event])
    if mode == "broken":
        return broken if event is None else "%s hook --agent claude --event %s" % (broken, HOOK_EVENTS[event])
    raise SystemExit("cutover-switch: unknown mode %r" % mode)


def variant_ext(mode):
    # omp only: where the extension symlink must point in each mode.
    repo = os.path.dirname(os.path.dirname(engine))
    if mode == "engine":
        return os.path.join(repo, "harness", "omp", "my-memory.ts")
    if mode == "python":
        return os.path.join(python_home, "harness", "omp", "my-memory.ts")
    if mode == "broken":
        return os.path.join(home_dir, ".cutover-broken-engine", "harness", "omp", "my-memory.ts")
    raise SystemExit("cutover-switch: unknown mode %r" % mode)


if harness == "omp":
    try:
        cur_ext = os.readlink(ext_link)
    except OSError as err:
        raise SystemExit("cutover-switch: cannot readlink %s: %s" % (ext_link, err))
    modes = [m for m in ("engine", "python", "broken")
             if cur_mcp == variant(m) and cur_ext == variant_ext(m)]
    if len(modes) != 1:
        # Mixed state (mcp points one way, extension the other) is a real hazard.
        raise SystemExit(
            "cutover-switch: omp config is in an unknown/mixed state (mcp=%r, ext=%r) — refusing to rewrite blindly"
            % (cur_mcp, cur_ext)
        )
    current = modes[0]
    if current == to:
        print("STATE already mode=%s" % to)
        print("FILE %s sha256=%s" % (mcp_path, sha(mcp_path)))
        print("EXT %s -> %s" % (ext_link, cur_ext))
        raise SystemExit(0)

    plan = [(mcp_path, cur_mcp, variant(to)), (ext_link, cur_ext, variant_ext(to))]
    for path, old, new in plan:
        print("%s %s : %s -> %s" % ("PLAN" if dry else "EDIT", path, old, new))
    if dry:
        print("STATE dry-run mode=%s desired=%s" % (current, to))
        raise SystemExit(0)

    text = read(mcp_path)
    token_old, token_new = json.dumps(cur_mcp), json.dumps(variant(to))
    if text.count(token_old) != 1:
        raise SystemExit(
            "cutover-switch: %s: expected 1 occurrence of %s, found %d"
            % (mcp_path, token_old, text.count(token_old))
        )
    text = text.replace(token_old, token_new)
    new_doc = json.loads(text)
    if mcp_command(new_doc) != variant(to):
        raise SystemExit("cutover-switch: post-check failed: mcp command did not change to %r" % variant(to))
    mode_bits = os.stat(mcp_path).st_mode & 0o777
    tmp = mcp_path + ".cutover-tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.chmod(tmp, mode_bits)
    os.replace(tmp, mcp_path)

    tmplink = ext_link + ".cutover-tmp"
    if os.path.lexists(tmplink):
        os.remove(tmplink)
    os.symlink(variant_ext(to), tmplink)
    os.replace(tmplink, ext_link)

    print("STATE switched mode=%s -> %s" % (current, to))
    print("FILE %s sha256=%s" % (mcp_path, sha(mcp_path)))
    print("EXT %s -> %s" % (ext_link, os.readlink(ext_link)))
    raise SystemExit(0)


def detect():
    modes = []
    for mode in ("engine", "python", "broken"):
        if cur_mcp == variant(mode) and all(cur_hooks[e] == variant(mode, e) for e in EVENTS):
            modes.append(mode)
    if len(modes) == 1:
        return modes[0]
    # Mixed state is a real hazard: never rewrite it silently.
    raise SystemExit(
        "cutover-switch: config is in an unknown/mixed state (mcp=%r) — refusing to rewrite blindly"
        % cur_mcp
    )


current = detect()
if current == to:
    print("STATE already mode=%s" % to)
    print("FILE %s sha256=%s" % (mcp_path, sha(mcp_path)))
    print("FILE %s sha256=%s" % (hook_path, sha(hook_path)))
    sys.exit(0)


def json_literal(s):
    return json.dumps(s)


def replace_once(text, old, new, where, expect=1):
    token_old, token_new = json_literal(old), json_literal(new)
    n = text.count(token_old)
    if n != expect:
        raise SystemExit(
            "cutover-switch: %s: expected %d occurrence(s) of %s, found %d"
            % (where, expect, token_old, n)
        )
    return text.replace(token_old, token_new)


plan = []
mcp_text = read(mcp_path)
mcp_text = replace_once(mcp_text, cur_mcp, variant(to), mcp_path)
plan.append((mcp_path, cur_mcp, variant(to)))

hook_text = read(hook_path)
for event in EVENTS:
    hook_text = replace_once(hook_text, cur_hooks[event], variant(to, event), hook_path)
    plan.append((hook_path, cur_hooks[event], variant(to, event)))

# Fail closed before writing: the replacement must still be valid JSON and must
# parse back to exactly the intended command strings.
try:
    new_mcp = json.loads(mcp_text)
    new_hook = json.loads(hook_text)
except json.JSONDecodeError as err:
    raise SystemExit("cutover-switch: replacement produced invalid JSON: %s" % err)
if mcp_command(new_mcp) != variant(to):
    raise SystemExit("cutover-switch: post-check failed: mcp command did not change to %r" % variant(to))
for event in EVENTS:
    got = None
    for group in (new_hook.get("hooks") or {}).get(event) or []:
        for h in (group or {}).get("hooks") or []:
            if isinstance(h, dict) and h.get("command") == variant(to, event):
                got = h["command"]
    if got is None:
        raise SystemExit("cutover-switch: post-check failed: %s hook did not change" % event)

for path, old, new in plan:
    print("%s %s : %s -> %s" % ("PLAN" if dry else "EDIT", path, old, new))
if dry:
    print("STATE dry-run mode=%s desired=%s" % (current, to))
    sys.exit(0)

for path, text in ((mcp_path, mcp_text), (hook_path, hook_text)):
    mode = os.stat(path).st_mode & 0o777
    tmp = path + ".cutover-tmp"
    with open(tmp, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.chmod(tmp, mode)
    os.replace(tmp, path)

print("STATE switched mode=%s -> %s" % (current, to))
print("FILE %s sha256=%s" % (mcp_path, sha(mcp_path)))
print("FILE %s sha256=%s" % (hook_path, sha(hook_path)))
PY
