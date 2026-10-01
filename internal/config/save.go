package config

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	kitconfig "hop.top/kit/go/core/config"
	"hop.top/kit/go/core/xdg"
)

// UserConfigPath returns the user layer's file,
// $XDG_CONFIG_HOME/foo/config.yaml: the one file foo writes settings to.
func UserConfigPath() (string, error) {
	dir, err := xdg.ConfigDir("foo")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// SetUser saves one setting: key (a top-level or dotted config key)
// set to value in the user config file, and nothing else.
//
// The merged Config is never written back. It carries the project
// file, FOO_* variables, -c overrides, --profile and computed defaults,
// none of which belong in the user's file; writing it also dropped the
// file's comments and any key foo does not know.
//
// An existing file is edited in place, so every other byte, its mode
// and a symlink to it survive. A single-line value at the top level is
// replaced on its own line and a missing top-level key is appended;
// the result is checked by decoding both versions, and anything this
// cannot do exactly (a block scalar, a dotted key, a flow-style file)
// goes through kit's config setter, which keeps every key, value,
// comment and their order but re-indents the file. A missing file is
// created holding just the key. A file that does not parse is an error
// and stays as it was.
func SetUser(key string, value any) error {
	path, err := UserConfigPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if out, ok := setTopLevelLine(data, key, value); ok {
			// Truncates in place: the mode is kept and a symlink is
			// followed, where a rename would replace either.
			return os.WriteFile(path, out, 0o644)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return kitconfig.SetValue(key, value, kitconfig.ScopeUser, kitconfig.Options{UserConfigPath: path})
}

// setTopLevelLine returns data with top-level key set to value by a
// single-line edit, and whether it could make one that changes nothing
// else. The candidate is accepted only when it decodes to data's
// document with key set to value.
func setTopLevelLine(data []byte, key string, value any) ([]byte, bool) {
	if strings.Contains(key, ".") {
		return nil, false
	}
	encoded, ok := inlineYAML(value)
	if !ok {
		return nil, false
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, false
	}
	var out []byte
	switch {
	case len(doc.Content) == 0:
		// Empty or comments only.
		out = appendKey(data, key, encoded)
	case doc.Content[0].Kind == yaml.MappingNode && doc.Content[0].Style&yaml.FlowStyle == 0:
		root := doc.Content[0]
		var val *yaml.Node
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == key {
				val = root.Content[i+1]
			}
		}
		if val == nil {
			out = appendKey(data, key, encoded)
			break
		}
		start, end, ok := scalarSpan(data, val)
		if !ok {
			return nil, false
		}
		out = append(append(append([]byte{}, data[:start]...), encoded...), data[end:]...)
	default:
		return nil, false
	}

	if !sameExceptKey(data, out, key, value) {
		return nil, false
	}
	return out, true
}

// inlineYAML encodes value as a one-line YAML scalar, quoted when a
// plain scalar would read back as something else.
func inlineYAML(value any) (string, bool) {
	b, err := yaml.Marshal(value)
	if err != nil {
		return "", false
	}
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" || strings.Contains(s, "\n") {
		return "", false
	}
	return s, true
}

// appendKey adds "key: value" as the file's last line.
func appendKey(data []byte, key, encoded string) []byte {
	k, ok := inlineYAML(key)
	if !ok {
		k = key
	}
	out := append([]byte{}, data...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, k+": "+encoded+"\n"...)
}

// scalarSpan returns the byte range of val's source text, for a plain
// or quoted scalar that sits on one line. Anything else reports false.
func scalarSpan(data []byte, val *yaml.Node) (int, int, bool) {
	if val.Kind != yaml.ScalarNode || val.Anchor != "" {
		return 0, 0, false
	}
	style := val.Style
	if style != 0 && style != yaml.DoubleQuotedStyle && style != yaml.SingleQuotedStyle {
		return 0, 0, false
	}

	// Line and Column are 1-based; Column counts characters.
	lineStart := 0
	for l := 1; l < val.Line; l++ {
		i := bytes.IndexByte(data[lineStart:], '\n')
		if i < 0 {
			return 0, 0, false
		}
		lineStart += i + 1
	}
	lineEnd := len(data)
	if i := bytes.IndexByte(data[lineStart:], '\n'); i >= 0 {
		lineEnd = lineStart + i
	}
	line := string(data[lineStart:lineEnd])
	runes := []rune(line)
	if val.Column-1 > len(runes) {
		return 0, 0, false
	}
	start := lineStart + len(string(runes[:val.Column-1]))
	rest := data[start:lineEnd]

	var n int
	switch style {
	case yaml.DoubleQuotedStyle:
		n = closingDouble(rest)
	case yaml.SingleQuotedStyle:
		n = closingSingle(rest)
	default:
		n = len(rest)
		if i := bytes.Index(rest, []byte(" #")); i >= 0 {
			n = i
		}
		if i := bytes.Index(rest, []byte("\t#")); i >= 0 && i < n {
			n = i
		}
		n = len(bytes.TrimRight(rest[:n], " \t\r"))
	}
	if n <= 0 {
		return 0, 0, false
	}
	return start, start + n, true
}

// closingDouble returns the length of the double-quoted scalar at the
// start of s, closing quote included, or 0 when it does not close on
// this line.
func closingDouble(s []byte) int {
	if len(s) == 0 || s[0] != '"' {
		return 0
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return 0
}

// closingSingle is closingDouble for a single-quoted scalar, where a
// doubled single quote is an escaped one.
func closingSingle(s []byte) int {
	if len(s) == 0 || s[0] != '\'' {
		return 0
	}
	for i := 1; i < len(s); i++ {
		if s[i] != '\'' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			i++
			continue
		}
		return i + 1
	}
	return 0
}

// sameExceptKey reports whether after decodes to before's document with
// top-level key set to value.
func sameExceptKey(before, after []byte, key string, value any) bool {
	var want, got map[string]any
	if err := yaml.Unmarshal(before, &want); err != nil {
		return false
	}
	if err := yaml.Unmarshal(after, &got); err != nil {
		return false
	}
	if want == nil {
		want = map[string]any{}
	}
	// Round-trip value so its decoded form compares like for like.
	var typed map[string]any
	b, err := yaml.Marshal(map[string]any{key: value})
	if err != nil || yaml.Unmarshal(b, &typed) != nil {
		return false
	}
	want[key] = typed[key]
	x, err1 := yaml.Marshal(want)
	y, err2 := yaml.Marshal(got)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}
