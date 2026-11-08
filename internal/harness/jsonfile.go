// Order-preserving JSON config editing.
//
// Harness config files (~/.claude/settings.json, ~/.cursor/mcp.json, …) are
// user-owned documents with hand-tuned key order. The Python installer
// (harness/install.sh) edited them through python dicts, which keep insertion
// order; a plain Go map[string]any would reshuffle every key on each install.
// obj keeps the original textual order (new keys append) so a merge is a
// minimal, reviewable diff and a second run is byte-stable.
package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atomsbaza/my-memory-engine/internal/store"
)

// obj is a JSON object that remembers key order. It satisfies
// json.Marshaler; values inside are plain Go types (string, bool, nil,
// json.Number, []any, *obj) so the standard encoder handles them.
type obj struct {
	keys []string
	vals map[string]any
}

func newObj() *obj {
	return &obj{vals: make(map[string]any)}
}

// Has reports whether key is present.
func (o *obj) Has(key string) bool {
	_, ok := o.vals[key]
	return ok
}

// Get returns the value stored under key.
func (o *obj) Get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// Set stores key → val, appending new keys at the end and keeping the
// original position of existing ones (Python dict setdefault/assign parity).
func (o *obj) Set(key string, val any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = val
}

// Keys returns the keys in their original document order.
func (o *obj) Keys() []string {
	return append([]string(nil), o.keys...)
}

// MarshalJSON writes the object with keys in order.
func (o *obj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(o.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// decodeValue reads one JSON value from dec, decoding objects as *obj and
// numbers as json.Number (so "20" and "2.50" survive a round trip verbatim).
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeToken(dec, tok)
}

func decodeToken(dec *json.Decoder, tok json.Token) (any, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := newObj()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key %v is not a string", kt)
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(key, v)
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return nil, err
			}
			return o, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return nil, err
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected delimiter %v", t)
		}
	default:
		return tok, nil // string, json.Number, bool or nil
	}
}

// loadJSONFile reads path into the ordered model. A missing file decodes to
// an empty object; any other error (unreadable, malformed, trailing garbage)
// is returned.
func loadJSONFile(path string) (any, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return newObj(), nil
		}
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return v, nil
}

// asObj asserts that v is a JSON object, returning nil otherwise.
func asObj(v any) *obj {
	o, _ := v.(*obj)
	return o
}

// saveJSONFile backs up the current file (if any) to
// <path>.bak-<YYYYMMDDHHMMSS> and atomically writes v indented like the
// Python installer (indent=2, unicode kept verbatim) plus a final
// newline. No backup is created when the file does not change content.
func saveJSONFile(path string, v any, now time.Time) (backup string, wrote bool, err error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", false, fmt.Errorf("marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	old, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", false, fmt.Errorf("read %s: %w", path, readErr)
	}
	if readErr == nil && bytes.Equal(old, data) {
		return "", false, nil // already installed — leave the file (and mtime) alone
	}
	if readErr == nil {
		backup = path + ".bak-" + now.Format(backupTimeFormat)
		// One backup per install run: merging several hook events into the
		// same file saves repeatedly within one timestamp, and the first
		// backup already captured the pre-install state — never overwrite it
		// with intermediate merged content.
		if _, err := os.Stat(backup); err != nil {
			if err := copyFile(path, backup); err != nil {
				return "", false, fmt.Errorf("backup %s: %w", path, err)
			}
		}
	}
	if err := store.AtomicWrite(path, data); err != nil {
		return "", false, err
	}
	return backup, true, nil
}

// jsonNumber wraps an int as a json.Number so written configs keep integer
// literals ("20", not "20.0"-prone floats).
func jsonNumber(n int) json.Number {
	return json.Number(fmt.Sprintf("%d", n))
}

// backupTimeFormat mirrors install.sh's `date +%Y%m%d%H%M%S` suffix.
const backupTimeFormat = "20060102150405"

// copyFile duplicates src to dst with src's permission bits.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(src); err == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(dst, data, mode)
}

// relToHome joins a slash-separated config-relative path onto home.
func relToHome(home, rel string) string {
	return filepath.Join(home, filepath.FromSlash(rel))
}

// quoteCmd renders a command line for plan output (install.sh `run` parity).
func quoteCmd(name string, args []string) string {
	parts := append([]string{name}, args...)
	for i, p := range parts {
		if strings.ContainsAny(p, " \t'\"") {
			parts[i] = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}
