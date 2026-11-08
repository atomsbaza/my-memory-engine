#!/usr/bin/env bash
# P4.2 G7b / AC-P4-07 personal-identifier gate, with the single owner-recorded
# LICENSE exception.
#
# Fails when a tracked file contains the owner's name, personal email/hostname or
# LaunchAgent label (`pisit|koolplukpol|outlook.com|com.pisit`).
#
# Why an exception exists at all: MIT requires the copyright holder's name, and
# the owner refused a pseudonym (`$WF/parity/P4/license-decision.md`,
# `scrub-ledger.md` R6). The exception is therefore a single (path, line) pair —
# it is NOT a path allowlist:
#
#   * only the file named exactly `LICENSE` at the repo root,
#   * only a line of the form `Copyright (c) <4-digit year> Pisit Koolplukpol`,
#   * any other identifier anywhere else fails, including another line of
#     LICENSE, a `LICENSE` of a subdirectory, or a look-alike name.
#
# The second and last exception is this script's own file: a scanner has to
# contain the identifiers it searches for. It is one exact path (never a
# directory), and every skipped file is counted and printed in the summary.
#
# usage: tools/hygiene-gate.sh [--repo DIR] [--self-test]
#
#   (no args)     scan the tracked files of DIR (default: this repo)
#   --self-test   discriminating control: build throwaway repos and assert the
#                 gate is green only for the recorded exception and red for
#                 every other placement of the identifier
#
# Exit: 0 clean (no hits outside the exception), 1 hits found, 2 usage,
#       3 self-test case mismatched its expected verdict.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"
self_test=0

usage() {
  awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="${2:-}"; shift 2 ;;
    --self-test) self_test=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "hygiene-gate: unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# scan <dir>: prints `HIT<TAB>path:line<TAB>text` per offense and one
# `SUMMARY<TAB>tracked=… excepted=… scanner-self=… hits=…` line; exit 0 clean.
scan() {
  python3 - "$1" <<'PY'
import re, subprocess, sys

repo = sys.argv[1]
IDENT = re.compile(rb"pisit|koolplukpol|outlook\.com|com\.pisit", re.IGNORECASE)
# The exception: exactly this path, exactly this line shape.
EXC_PATH = "LICENSE"
EXC_LINE = re.compile(rb"Copyright \(c\) \d{4} Pisit Koolplukpol")
# A scanner must hold the identifiers it looks for, so the scanner's own file is
# the second and last exception. It is a single exact path — never a directory —
# and every skip is counted and printed so the omission is visible, not silent.
SELF_PATH = "tools/hygiene-gate.sh"

files = [f for f in subprocess.run(
    ["git", "-C", repo, "ls-files", "-z"], capture_output=True).stdout.split(b"\0") if f]
hits = 0
excepted = 0
skipped = 0
for raw in files:
    path = raw.decode("utf-8", "surrogateescape")
    if path == SELF_PATH:
        skipped += 1
        continue
    try:
        with open(repo + "/" + path, "rb") as fh:
            blob = fh.read()
    except OSError as err:
        hits += 1
        print("HIT\t%s\tunreadable tracked file: %s" % (path, err))
        continue
    for lineno, line in enumerate(blob.split(b"\n"), 1):
        if not IDENT.search(line):
            continue
        if path == EXC_PATH and EXC_LINE.fullmatch(line.strip()):
            excepted += 1
            continue
        hits += 1
        print("HIT\t%s:%d\t%s" % (path, lineno,
                                  line.decode("utf-8", "replace").strip()[:160]))
print("SUMMARY\ttracked=%d\texcepted=%d\tscanner-self=%d\thits=%d"
      % (len(files), excepted, skipped, hits))
raise SystemExit(1 if hits else 0)
PY
}

if [ "$self_test" = 1 ]; then
  tmp="$(mktemp -d -t hygiene-gate)"
  trap 'rm -rf "$tmp"' EXIT
  failures=0
  cases=0

  new_repo() { # name -> $(next_repo)
    next_repo="$tmp/$1"
    mkdir -p "$next_repo"
    git -C "$next_repo" init -q 2>/dev/null
  }

  license_text() { # path [extra-line]
    cat > "$1" <<EOF
MIT License

Copyright (c) 2026 Pisit Koolplukpol
${2:-}
Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software").
EOF
  }

  case_check() { # name expected-exit
    name="$1"; want="$2"
    cases=$((cases + 1))
    out="$(scan "$next_repo")"; code=$?
    if [ "$code" -eq "$want" ]; then
      printf 'SELF-TEST %-22s PASS expected=%s got=%s  %s\n' "$name" "$want" "$code" \
        "$(printf '%s\n' "$out" | grep -c '^HIT' | tr -d ' ') hit(s)"
    else
      printf 'SELF-TEST %-22s FAIL expected=%s got=%s\n' "$name" "$want" "$code"
      printf '%s\n' "$out" | sed 's/^/    /'
      failures=$((failures + 1))
    fi
  }

  # 1. green: only the recorded copyright-holder line carries the name.
  new_repo license-only
  license_text "$next_repo/LICENSE"
  git -C "$next_repo" add -A 2>/dev/null
  case_check license-only 0

  # 2. red: the name in any other tracked file.
  new_repo name-in-docs
  license_text "$next_repo/LICENSE"
  mkdir -p "$next_repo/docs"
  printf 'Maintained by Pisit Koolplukpol.\n' > "$next_repo/docs/note.md"
  git -C "$next_repo" add -A 2>/dev/null
  case_check name-in-docs 1

  # 3. red: a second line of LICENSE — the exception is line-shaped, not path-wide.
  new_repo name-extra-line-license
  license_text "$next_repo/LICENSE"
  printf '\nContact: Pisit Koolplukpol\n' >> "$next_repo/LICENSE"
  git -C "$next_repo" add -A 2>/dev/null
  case_check name-extra-line-license 1

  # 4. red: a LICENSE in a subdirectory is not the root LICENSE.
  new_repo license-lookalike
  license_text "$next_repo/LICENSE"
  mkdir -p "$next_repo/copy"
  license_text "$next_repo/copy/LICENSE"
  git -C "$next_repo" add -A 2>/dev/null
  case_check license-lookalike 1

  # 5. red: the pre-P4.2 LaunchAgent label anywhere.
  new_repo legacy-label
  license_text "$next_repo/LICENSE"
  mkdir -p "$next_repo/ops"
  printf '<key>Label</key><string>com.pisit.mem-backup</string>\n' > "$next_repo/ops/launchd.plist"
  git -C "$next_repo" add -A 2>/dev/null
  case_check legacy-label 1

  # 6. red: personal email.
  new_repo personal-email
  license_text "$next_repo/LICENSE"
  printf 'author: someone@outlook.com\n' > "$next_repo/NOTES.txt"
  git -C "$next_repo" add -A 2>/dev/null
  case_check personal-email 1

  # 7. red: another file under tools/ — the self-skip is one exact path, not a
  #    directory, so the scanner cannot be widened into a tools/ allowlist.
  new_repo sibling-tools-file
  license_text "$next_repo/LICENSE"
  mkdir -p "$next_repo/tools"
  printf '# helper that mentions Pisit Koolplukpol\n' > "$next_repo/tools/other.sh"
  git -C "$next_repo" add -A 2>/dev/null
  case_check sibling-tools-file 1

  if [ "$failures" -ne 0 ]; then
    printf 'G7b SELF-TEST FAIL %d/%d case(s) mismatched\n' "$failures" "$cases"
    exit 3
  fi
  printf 'G7b SELF-TEST PASS %d/%d cases (1 recorded exception, %d discriminating reds)\n' \
    "$cases" "$cases" "$((cases - 1))"
  exit 0
fi

[ -n "$repo" ] && [ -d "$repo/.git" ] || { echo "hygiene-gate: not a git repo: $repo" >&2; exit 2; }

out="$(scan "$repo")"; code=$?
printf '%s\n' "$out" | grep '^HIT' | cut -f2,3 | sed 's/^/  /'
summary="$(printf '%s\n' "$out" | grep '^SUMMARY' | cut -f2- | tr '\t' ' ')"
if [ "$code" -eq 0 ]; then
  printf 'G7b PASS %s (exceptions: root LICENSE copyright line · own scanner file)\n' "$summary"
  exit 0
fi
printf 'G7b FAIL %s\n' "$summary"
exit 1
