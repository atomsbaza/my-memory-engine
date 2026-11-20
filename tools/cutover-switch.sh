#!/usr/bin/env bash
# P3 staged-cutover switcher: moves ONE harness's MCP + hook config between the
# Python writer (pre-cutover) and the engine writer (post-cutover), and back.
#
# The switch is surgical: only the exact command strings that name the memory
# binary are rewritten, every other byte of the config file is left alone. That
# is what makes the rollback provably byte-exact (`cutover-switch.sh --to
# python` restores the same bytes `--to engine` replaced) — see P3 spec §5.4.
#
# usage: tools/cutover-switch.sh --to engine|python|broken [--harness claude|omp|codex|gemini|cursor|hermes]
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
# codex = ~/.codex/config.toml ([mcp_servers.my-memory], TOML) + ~/.codex/hooks.json
# (grouped JSON hooks). gemini = ~/.gemini/settings.json (MCP mcpServers +
# grouped hooks in ONE file; BeforeAgent carries --format json --event-name).
# cursor = ~/.cursor/mcp.json (mcpServers) + ~/.cursor/hooks.json (cursor's own
# FLAT schema {"version":1,"hooks":{event:[{command}]}}). hermes =
# ~/.hermes/config.yaml (YAML: mcp_servers + hooks.pre_llm_call/on_session_end,
# absolute paths, hooks consent-allowlisted by hermes itself).
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
  claude|omp|codex|gemini|cursor|hermes) ;;
  *) # Other harnesses add their own config surfaces; refusing beats guessing a schema.
     echo "cutover-switch: harness '$harness' not implemented yet (implemented: claude, omp, codex, gemini, cursor, hermes)" >&2
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
case "$harness" in
  omp)
    # omp: MCP JSON + the TS extension symlink (hook surface). No hooks JSON exists.
    mcp_config="$home_dir/.omp/agent/mcp.json"
    hook_config=""
    ext_link="$home_dir/.omp/agent/extensions/my-memory.ts"
    [ -f "$mcp_config" ] || { echo "cutover-switch: missing config: $mcp_config" >&2; exit 1; }
    [ -L "$ext_link" ] || { echo "cutover-switch: extension symlink missing: $ext_link" >&2; exit 1; }
    ;;
  codex)
    mcp_config="$home_dir/.codex/config.toml"
    hook_config="$home_dir/.codex/hooks.json"
    ;;
  gemini)
    mcp_config="$home_dir/.gemini/settings.json"
    hook_config="$home_dir/.gemini/settings.json"
    ;;
  cursor)
    mcp_config="$home_dir/.cursor/mcp.json"
    hook_config="$home_dir/.cursor/hooks.json"
    ;;
  hermes)
    mcp_config="$home_dir/.hermes/config.yaml"
    hook_config="$home_dir/.hermes/config.yaml"
    ;;
  *)
    mcp_config="$home_dir/.claude.json"
    hook_config="$home_dir/.claude/settings.json"
    ;;
esac
for f in "$mcp_config" "$hook_config"; do
  [ -n "$f" ] || continue
  if [ ! -f "$f" ]; then
    echo "cutover-switch: missing config: $f" >&2
    exit 1
  fi
done

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
if [ "$backup" = 1 ] && [ "$dry" = 0 ]; then
  last=""
  for f in "$mcp_config" "$hook_config"; do
    [ -n "$f" ] || continue
    [ "$f" = "$last" ] && continue   # gemini/hermes: MCP + hooks share one file
    cp -p "$f" "$f.bak-$stamp"
    echo "BACKUP $f.bak-$stamp sha256=$(shasum -a 256 "$f.bak-$stamp" | cut -d' ' -f1)"
    last="$f"
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

# ---------------------------------------------------------------------------
# codex / gemini / cursor / hermes — generic spec-driven path. Same surgical
# discipline as claude/omp: only the exact command strings naming the memory
# binary are rewritten (json.dumps token for JSON, quoted scalar for TOML,
# raw string for YAML), every other byte is left alone, and the result must
# parse back with the intended commands before anything is written.
# ---------------------------------------------------------------------------
NEW_SPECS = {
    "codex": {
        "agent": "codex",
        "mcp_kind": "toml",
        "hook_kind": "grouped",
        # (config event key, --event value, extra flags)
        "events": [
            ("SessionStart", "session-start", []),
            ("UserPromptSubmit", "user-prompt", []),
            ("Stop", "stop", []),
        ],
    },
    "gemini": {
        "agent": "gemini",
        "mcp_kind": "json",
        "hook_kind": "grouped",
        "events": [
            ("SessionStart", "session-start", ["--format", "json"]),
            ("BeforeAgent", "user-prompt", ["--format", "json", "--event-name", "BeforeAgent"]),
        ],
    },
    "cursor": {
        "agent": "cursor-agent",
        "mcp_kind": "json",
        "hook_kind": "flat",
        "events": [
            ("sessionStart", "session-start", ["--format", "cursor"]),
            ("beforeSubmitPrompt", "user-prompt", ["--format", "cursor", "--event-name", "beforeSubmitPrompt"]),
            ("stop", "stop", []),
        ],
    },
    "hermes": {
        "agent": "hermes",
        "mcp_kind": "yaml",
        "hook_kind": "yaml",
        "events": [
            ("pre_llm_call", "user-prompt", ["--format", "hermes"]),
            ("on_session_end", "stop", []),
        ],
    },
}


def new_harness_switch():
    import re

    spec = NEW_SPECS[harness]
    agent = spec["agent"]
    single_file = mcp_path == hook_path

    def var_mcp(mode):
        if mode == "engine":
            return engine
        if mode == "python":
            return os.path.join(python_home, "bin", "mem")
        return broken

    def var_hook(mode, mem_event, extra):
        ex = " ".join(extra)
        if mode in ("engine", "broken"):
            base = engine if mode == "engine" else broken
            cmd = "%s hook --agent %s --event %s" % (base, agent, mem_event)
        else:
            cmd = "%s %s %s" % (os.path.join(python_home, "harness", "mem-hook.sh"), agent, mem_event)
        return (cmd + " " + ex).strip()

    def toml_section(text, header):
        m = re.search(r"^\[%s\]\s*$(.*?)(?=^\[|\Z)" % re.escape(header), text, re.M | re.S)
        return m.group(1) if m else None

    def toml_string(body, key):
        if body is None:
            return None
        m = re.search(r"^%s\s*=\s*\"([^\"]*)\"" % re.escape(key), body, re.M)
        return m.group(1) if m else None

    def load_mcp(kind, text, path):
        # -> the my-memory server command string
        if kind == "json":
            entry = (json.loads(text).get("mcpServers") or {}).get("my-memory")
            if not isinstance(entry, dict):
                raise SystemExit("cutover-switch: %s has no mcpServers.my-memory entry" % path)
            cmd = entry.get("command")
        elif kind == "toml":
            cmd = toml_string(toml_section(text, "mcp_servers.my-memory"), "command")
        else:
            import yaml
            entry = ((yaml.safe_load(text) or {}).get("mcp_servers") or {}).get("my-memory")
            cmd = (entry or {}).get("command")
        if not isinstance(cmd, str) or not cmd:
            raise SystemExit("cutover-switch: %s: my-memory server has no command string" % path)
        return cmd

    def hook_is_memory(c):
        return isinstance(c, str) and ("mem-hook.sh" in c or " hook --agent " in c)

    def load_hooks(kind, text_or_path, from_text=False):
        # -> {event key: command} for every wired memory hook (exactly 1 each)
        if from_text:
            text = text_or_path
        else:
            text = read(text_or_path)
        out = {}
        if kind == "grouped":
            doc = json.loads(text)
            for ek, _me, _ex in spec["events"]:
                found = []
                for group in (doc.get("hooks") or {}).get(ek) or []:
                    for h in (group or {}).get("hooks") or []:
                        c = h.get("command") if isinstance(h, dict) else None
                        if hook_is_memory(c):
                            found.append(c)
                out[ek] = found
        elif kind == "flat":
            doc = json.loads(text)
            for ek, _me, _ex in spec["events"]:
                found = [e.get("command") for e in (doc.get("hooks") or {}).get(ek) or []
                         if isinstance(e, dict) and hook_is_memory(e.get("command"))]
                out[ek] = found
        else:
            import yaml
            doc = yaml.safe_load(text) or {}
            for ek, _me, _ex in spec["events"]:
                found = [e.get("command") for e in (doc.get("hooks") or {}).get(ek) or []
                         if isinstance(e, dict) and hook_is_memory(e.get("command"))]
                out[ek] = found
        for ek, found in out.items():
            if not found:
                raise SystemExit("cutover-switch: hooks: no memory hook under %s" % ek)
            if len(found) > 1:
                raise SystemExit("cutover-switch: hooks: %d memory hooks under %s (dual writer) — resolve by hand"
                                 % (len(found), ek))
        return {ek: found[0] for ek, found in out.items()}

    mcp_text = read(mcp_path)
    hook_text = mcp_text if single_file else read(hook_path)
    cur_mcp = load_mcp(spec["mcp_kind"], mcp_text, mcp_path)
    cur_hooks = load_hooks(spec["hook_kind"], hook_text, from_text=True)

    modes = [m for m in ("engine", "python", "broken")
             if cur_mcp == var_mcp(m)
             and all(cur_hooks[ek] == var_hook(m, me, ex) for ek, me, ex in spec["events"])]
    if len(modes) != 1:
        raise SystemExit("cutover-switch: %s config is in an unknown/mixed state (mcp=%r, hooks=%r) — refusing to rewrite blindly"
                         % (harness, cur_mcp, cur_hooks))
    current = modes[0]
    if current == to:
        print("STATE already mode=%s" % to)
        for p in dict.fromkeys([mcp_path] + ([hook_path] if not single_file else [])):
            print("FILE %s sha256=%s" % (p, sha(p)))
        raise SystemExit(0)

    plan = [("mcp", mcp_path, cur_mcp, var_mcp(to))]
    for ek, me, ex in spec["events"]:
        plan.append(("hook", hook_path, cur_hooks[ek], var_hook(to, me, ex)))

    # Surgical replacement per file kind. JSON uses the json.dumps token (as
    # the claude path does); TOML replaces the quoted TOML string; YAML the
    # raw command string (plain scalar or inside double quotes). Each token
    # must occur exactly once in its file or nothing is written.
    for role, path, old, new in plan:
        text = mcp_text if path == mcp_path else hook_text
        if role == "mcp" and spec["mcp_kind"] == "toml":
            token_old, token_new = '"%s"' % old, '"%s"' % new
        elif role == "mcp" and spec["mcp_kind"] == "yaml":
            # the bare MCP path is a substring of the hook commands ("bin/mem
            # hook --agent …"), so anchor the token to the YAML key; hermes
            # hook commands are always quoted, the server command is bare.
            token_old, token_new = "command: " + old, "command: " + new
        else:
            token_old, token_new = json.dumps(old), json.dumps(new)
        n = text.count(token_old)
        if n != 1:
            raise SystemExit("cutover-switch: %s: expected 1 occurrence of %s, found %d"
                             % (path, token_old, n))
        replaced = text.replace(token_old, token_new)
        if token_old in replaced:
            raise SystemExit("cutover-switch: %s: post-replace sanity failed" % path)
        if path == mcp_path:
            mcp_text = replaced
        if path == hook_path:
            hook_text = replaced

    # Fail closed before writing: parse everything back and confirm the exact
    # intended command strings are in place.
    if load_mcp(spec["mcp_kind"], mcp_text, mcp_path) != var_mcp(to):
        raise SystemExit("cutover-switch: post-check failed: mcp command did not change to %r" % var_mcp(to))
    want_hooks = {ek: var_hook(to, me, ex) for ek, me, ex in spec["events"]}
    if load_hooks(spec["hook_kind"], hook_text, from_text=True) != want_hooks:
        raise SystemExit("cutover-switch: post-check failed: hooks did not change to %r" % want_hooks)

    for _role, path, old, new in plan:
        print("%s %s : %s -> %s" % ("PLAN" if dry else "EDIT", path, old, new))
    if dry:
        print("STATE dry-run mode=%s desired=%s" % (current, to))
        raise SystemExit(0)

    for path in dict.fromkeys([mcp_path] + ([hook_path] if not single_file else [])):
        text = mcp_text if path == mcp_path else hook_text
        mode = os.stat(path).st_mode & 0o777
        tmp = path + ".cutover-tmp"
        with open(tmp, "w", encoding="utf-8") as fh:
            fh.write(text)
        os.chmod(tmp, mode)
        os.replace(tmp, path)
        print("FILE %s sha256=%s" % (path, sha(path)))
    print("STATE switched mode=%s -> %s" % (current, to))
    raise SystemExit(0)



EVENTS = ["SessionStart", "UserPromptSubmit", "Stop"]
HOOK_EVENTS = {"SessionStart": "session-start", "UserPromptSubmit": "user-prompt", "Stop": "stop"}


def sha(path):
    with open(path, "rb") as fh:
        return hashlib.sha256(fh.read()).hexdigest()


def read(path):
    with open(path, encoding="utf-8") as fh:
        return fh.read()

if harness in NEW_SPECS:
    new_harness_switch()


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
