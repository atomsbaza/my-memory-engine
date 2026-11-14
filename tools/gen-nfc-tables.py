#!/usr/bin/env python3
"""Generate internal/norm/tables_nfc.go — the vendored Unicode canonical
normalization data this repo needs for content_sha256, with zero new module
dependencies (AGENTS.md: pure Go, no CGO, no new deps).

Source of truth: the running interpreter's unicodedata (Unicode version
printed below). Go's stdlib ships no normalization; golang.org/x/text is
forbidden by the no-new-deps rule, so the engine vendors the small subset
NFC needs:

  * nfdTable — full canonical decomposition (Python NFD string) of every
    code point that decomposes canonically, Hangul syllables excluded
    (they are handled algorithmically, UAX#15 §3.12);
  * cccTable — nonzero canonical combining classes;
  * compTable — canonical composition pairs (starter, combining) -> cp,
    extracted by construction: a pair (A, B) composes to C iff
    NFC(A + B) == C, so the table mirrors this Python's NFC exactly
    (composition exclusions fall out for free).

The script then re-implements the Go algorithm (decompose -> canonical
order -> compose) in Python and validates it against
unicodedata.normalize('NFC') over: every single code point, every
(starter, combining) pair, and a seeded random corpus over the ranges a
memory body plausibly carries (Latin, Thai, CJK, marks, Hangul). It prints golden test vectors for internal/norm/nfc_test.go
to stderr (stdout stays the generated table file).

usage: tools/gen-nfc-tables.py > internal/norm/tables_nfc.go
       (golden vectors are printed to stderr; redirect as needed)
"""

import random
import sys
import unicodedata

UNIVERSION = unicodedata.unidata_version

HANGUL_SBASE = 0xAC00
HANGUL_LBASE = 0x1100
HANGUL_VBASE = 0x1161
HANGUL_TBASE = 0x11A7
HANGUL_LCOUNT = 19
HANGUL_VCOUNT = 21
HANGUL_TCOUNT = 28
HANGUL_NCOUNT = HANGUL_VCOUNT * HANGUL_TCOUNT
HANGUL_SCOUNT = HANGUL_LCOUNT * HANGUL_NCOUNT


def is_hangul_syllable(cp):
    return HANGUL_SBASE <= cp < HANGUL_SBASE + HANGUL_SCOUNT


def build_tables():
    nfd = {}
    ccc = {}
    comp = {}
    for cp in range(0x110000):
        if 0xD800 <= cp <= 0xDFFF:
            continue  # surrogates never appear in a Go string
        ch = chr(cp)
        cb = unicodedata.combining(ch)
        if cb != 0:
            ccc[cp] = cb
        if is_hangul_syllable(cp):
            continue  # algorithmic
        d = unicodedata.normalize("NFD", ch)
        if d != ch:
            nfd[cp] = d
        # Composition pairs come from the *primary* canonical decomposition,
        # not the full NFD: a cp like U+01ED (NFD: o + ogonek + macron, 3
        # chars) still has the 2-element primary decomposition [01EB, 0304]
        # and must compose. Keep a pair only when this interpreter's NFC
        # actually composes it (composition exclusions excluded).
        prim = unicodedata.decomposition(ch)
        if prim and not prim.startswith("<"):
            parts = [int(x, 16) for x in prim.split()]
            if len(parts) == 2 and not any(is_hangul_syllable(x) for x in parts):
                a, b = chr(parts[0]), chr(parts[1])
                # Canonical composition pairs A (starter) + B (any class: the
                # Indic signs compose with ccc(B) == 0) that this
                # interpreter's NFC actually composes.
                if unicodedata.combining(a) == 0 and \
                        unicodedata.normalize("NFC", a + b) == ch:
                    comp[(parts[0], parts[1])] = cp
    return nfd, ccc, comp


# --- the Go algorithm, mirrored exactly, for validation ----------------------

def g_decompose(runes):
    out = []
    for c in runes:
        if is_hangul_syllable(c):
            s = c - HANGUL_SBASE
            l = HANGUL_LBASE + s // HANGUL_NCOUNT
            v = HANGUL_VBASE + (s % HANGUL_NCOUNT) // HANGUL_TCOUNT
            t = HANGUL_TBASE + s % HANGUL_TCOUNT
            out.append(l)
            out.append(v)
            if t != HANGUL_TBASE:
                out.append(t)
        elif c in NFD:
            out.extend(ord(x) for x in NFD[c])
        else:
            out.append(c)
    return out


def g_ccc(cp):
    return CCC.get(cp, 0)


def g_reorder(runes):
    # stable insertion sort within each combining run (canonical ordering).
    for i in range(1, len(runes)):
        c = runes[i]
        if g_ccc(c) == 0:
            continue
        j = i
        while j > 0:
            p = g_ccc(runes[j - 1])
            if p == 0 or p <= g_ccc(c):
                break
            runes[j] = runes[j - 1]
            j -= 1
        runes[j] = c
    return runes


def g_compose(runes):
    if not runes:
        return runes
    out = [runes[0]]
    starter_pos = 0
    last_class = g_ccc(runes[0])
    for ch in runes[1:]:
        cl = g_ccc(ch)
        done = False
        starter = out[starter_pos]
        if not done:
            target = COMP.get((starter, ch))
            if target is not None and (last_class < cl or last_class == 0):
                out[starter_pos] = target
                done = True
        if not done:
            # Hangul algorithmic composition (starter jamo -> LV, LV -> LVT).
            if HANGUL_LBASE <= starter < HANGUL_LBASE + HANGUL_LCOUNT and \
               HANGUL_VBASE <= ch < HANGUL_VBASE + HANGUL_VCOUNT:
                out[starter_pos] = HANGUL_SBASE + (starter - HANGUL_LBASE) * HANGUL_NCOUNT + \
                    (ch - HANGUL_VBASE) * HANGUL_TCOUNT
                done = True
            elif HANGUL_SBASE <= starter < HANGUL_SBASE + HANGUL_SCOUNT and \
                    (starter - HANGUL_SBASE) % HANGUL_TCOUNT == 0 and \
                    HANGUL_TBASE < ch < HANGUL_TBASE + HANGUL_TCOUNT:
                out[starter_pos] = starter + (ch - HANGUL_TBASE)
                done = True
        if not done:
            if cl == 0:
                starter_pos = len(out)
            last_class = cl
            out.append(ch)
    return out


def g_nfc(s):
    runes = [ord(x) for x in s]
    return "".join(chr(x) for x in g_compose(g_reorder(g_decompose(runes))))


def validate(golden):
    failures = 0

    def check(s, where):
        nonlocal failures
        want = unicodedata.normalize("NFC", s)
        got = g_nfc(s)
        if got != want:
            failures += 1
            if failures <= 20:
                print(f"MISMATCH {where}: U+{'+'.join(f'{ord(c):04X}' for c in s)} "
                      f"got U+{'+'.join(f'{ord(c):04X}' for c in got)} "
                      f"want U+{'+'.join(f'{ord(c):04X}' for c in want)}", file=sys.stderr)

    # every single code point
    for cp in range(0x110000):
        if 0xD800 <= cp <= 0xDFFF:
            continue
        check(chr(cp), "single-cp")
    # every composition pair, forwards and against an unrelated mark
    for (a, b) in COMP:
        check(chr(a) + chr(b), "comp-pair")
        probe = chr(a) + chr(0x0300) + chr(b)
        check(probe, "comp-probe")
    # seeded random corpus over the code points a memory body carries
    rng = random.Random(20261114)
    pools = [
        range(0x0020, 0x0180),   # Latin + Latin marks
        range(0x0E00, 0x0E80),   # Thai
        range(0x1100, 0x1800),   # Hangul jamo
        range(0xAC00, 0xAC00 + HANGUL_SCOUNT),
        range(0x3000, 0x3200),  # CJK punctuation
        range(0x4E00, 0x4F00),   # CJK ideographs
        range(0x0300, 0x0370),   # combining marks
        range(0xF900, 0xFB00),   # CJK compatibility
    ]
    for _ in range(30000):
        n = rng.randint(2, 8)
        s = "".join(chr(rng.choice(pools[rng.randrange(len(pools))])) for _ in range(n))
        check(s, "random")
    if failures:
        print(f"validate: {failures} mismatches", file=sys.stderr)
        sys.exit(1)
    for s in golden:
        want = unicodedata.normalize("NFC", s)
        got = g_nfc(s)
        assert got == want, s
        in_hex = ", ".join("0x%04X" % ord(c) for c in s)
        out_hex = ", ".join("0x%04X" % ord(c) for c in got)
        print("\t{%s}, {%s}," % ('"' + in_hex + '"', '"' + out_hex + '"'), file=sys.stderr)
    print("validate: OK (unidata %s)" % UNIVERSION, file=sys.stderr)



GOLDEN = [
    "Caf\u00e9",                     # precomposed stays
    "Cafe\u0301",                    # e + combining acute composes
    "a\u0301\u0328",                # reorder then no-compose (order swap)
    "a\u0328\u0301",                # composes via reordering
    "\u0e01\u0e33\u0e19\u0e2d\u0e07",   # Thai text
    "\u0e33\u0e33",                       # Thai marks
    "\u1100\u1161\u11a8",          # Hangul jamo -> syllable
    "\u00c5",                        # singleton? A-ring composes
    "\ufb01ne",                      # compatibility ligature untouched by NFC
    "e\u0301e\u0301",               # repeated composition
    "\u0041\u030a\u0042\u0323",   # two composable pairs stacked
]


def main():
    global NFD, CCC, COMP
    NFD, CCC, COMP = build_tables()
    validate(GOLDEN)

    out = sys.stdout
    w = out.write
    w("// Code generated from the Python unicodedata tables; DO NOT EDIT.\n")
    w("// Regenerate: tools/gen-nfc-tables.py > internal/norm/tables_nfc.go\n")
    w(f"// Unicode version: {UNIVERSION} (the generator's unicodedata). Source data:\n")
    w("// Unicode Character Database, Copyright (c) Unicode, Inc. — Unicode License\n")
    w("// (permissive: copy, distribute, modify; see unicode.org/license.txt).\n")
    w("package norm\n")
    w("\n// NFD maps every canonically decomposing code point (Hangul syllables\n")
    w("// excepted — algorithmic, see nfc.go) onto its full canonical decomposition\n")
    w("// as Python NFD produces it (Canonical decomposition + canonical order).\n")
    w("var NFD = map[rune]string{\n")
    for cp in sorted(NFD):
        seq = NFD[cp]
        def esc_r(c):
            if 0x20 < ord(c) < 0x7f and c not in ('"', "\\"):
                return c
            if ord(c) <= 0xFFFF:
                return "\\u%04x" % ord(c)
            return "\\U%08x" % ord(c)
        esc = "".join(esc_r(c) for c in seq)
        w(f"\t0x{cp:04X}: \"{esc}\",\n")
    w("}\n\n")
    w("// CCC maps every code point with a nonzero canonical combining class onto\n")
    w("// that class; absent means 0 (starter).\n")
    w("var CCC = map[rune]uint8{\n")
    for cp in sorted(CCC):
        w(f"\t0x{cp:04X}: {CCC[cp]},\n")
    w("}\n\n")
    w("// compPair is one canonical composition pair: A (starter) + B (combining)\n")
    w("// -> C. Extracted by construction from this interpreter's NFC, so the\n")
    w("// composition exclusions of DerivedNormalizationProps are already applied.\n")
    w("type compPair struct{ A, B rune }\n\n")
    w("// COMP is the canonical composition table.\n")
    w("var COMP = map[compPair]rune{\n")
    for (a, b) in sorted(COMP):
        w(f"\t{{0x{a:04X}, 0x{b:04X}}}: 0x{COMP[(a, b)]:04X},\n")
    w("}\n")


if __name__ == "__main__":
    main()
