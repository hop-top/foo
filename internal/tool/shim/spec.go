// Package shim runs OS commands as LLM tools from declarative YAML
// specs. A spec fixes everything the model cannot choose: the pinned
// binary, the argv shape, which arguments are paths and what the command
// does to them. The engine validates the model's arguments against the
// spec, asks a gate.Authorizer to resolve and authorize every path, and
// builds argv only from the canonical paths the authorizer granted.
package shim

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"hop.top/kit/go/core/scope"
)

// SpecVersion is the only spec schema version this engine reads.
const SpecVersion = 1

// Param types. Nothing else is accepted: there is no free-form argv type.
const (
	TypePath   = "path"
	TypeBool   = "bool"
	TypeEnum   = "enum"
	TypeInt    = "int"
	TypeString = "string"
)

// Output modes.
const (
	OutputText   = "text"
	OutputPaths0 = "paths0"
)

// Recursion modes a recursive path param declares.
const (
	RecursionFilterBefore = "filter_before"
	RecursionFilterAfter  = "filter_after"
	RecursionAllOrNothing = "all_or_nothing"
)

// Flavors of a command binary (§8).
const (
	FlavorGNU     = "gnu"
	FlavorBSD     = "bsd"
	FlavorBusyBox = "busybox"
)

const (
	defaultTimeout  = 30 * time.Second
	maxTimeout      = 120 * time.Second
	defaultMaxItems = 64
	maxMaxItems     = 256
	maxStringLen    = 4096
)

var (
	nameRe      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	enumValueRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
)

// Spec is one parsed, linted tool spec.
type Spec struct {
	Version              int          `yaml:"spec"`
	Name                 string       `yaml:"name"`
	Description          string       `yaml:"description"`
	Command              Command      `yaml:"command"`
	SideEffect           string       `yaml:"side_effect"`
	SideEffectIf         []Escalation `yaml:"side_effect_if"`
	Network              string       `yaml:"network"`
	Timeout              string       `yaml:"timeout"`
	OkExitCodes          []int        `yaml:"ok_exit_codes"`
	Output               string       `yaml:"output"`
	Params               []*Param     `yaml:"params"`
	Exclusive            [][]string   `yaml:"exclusive"`
	Argv                 []string     `yaml:"argv"`
	ExpressionAfterPaths bool         `yaml:"expression_after_paths"`
	Script               *Script      `yaml:"script"`

	timeout time.Duration
	digest  string
	byName  map[string]*Param
}

// Command names the binary and its per-flavor overrides.
type Command struct {
	Bin      []string           `yaml:"bin"`
	Variants map[string]Variant `yaml:"variants"`
}

// Variant overrides parts of a spec for one binary flavor.
type Variant struct {
	Prefix      []string                 `yaml:"prefix"`
	Params      map[string]ParamOverride `yaml:"params"`
	OkExitCodes []int                    `yaml:"ok_exit_codes"`
	Unsupported []string                 `yaml:"unsupported"`
}

// ParamOverride replaces a param's argv mapping on one flavor.
type ParamOverride struct {
	Argv   yaml.Node           `yaml:"argv"`
	Values map[string][]string `yaml:"values"`

	boolArgv map[bool][]string
	fragment []string
}

// Escalation changes the side effect when every `when` value matches.
type Escalation struct {
	When       map[string]any `yaml:"when" json:"when"`
	SideEffect string         `yaml:"side_effect" json:"side_effect"`
}

// OpWhen replaces a path param's op on calls where every `when` value
// matches. It may only narrow op, and dropping write needs a read side
// effect under the same condition (sed dry_run only reads).
type OpWhen struct {
	When map[string]any `yaml:"when" json:"when"`
	Op   []string       `yaml:"op" json:"op"`

	op scope.Op
}

// Script declares an argument foo generates from typed params. The model
// never supplies a script; it supplies the params, and foo's generator
// for Kind renders them into one argv token under the {script}
// placeholder.
type Script struct {
	// Kind selects the generator. Only "sed_substitute" exists.
	Kind string `yaml:"kind"`
	// Find and Replace name string params.
	Find    string `yaml:"find"`
	Replace string `yaml:"replace"`
	// Flags maps bool params to the s-command flag they add.
	Flags map[string]string `yaml:"flags"`
	// Occurrence names an int param rendered as the numeric flag.
	Occurrence string `yaml:"occurrence"`
	// Backrefs names a bool param that opts into sed's replacement
	// syntax (& and \1..\9). Without it, or when it is false, Replace
	// is inserted literally: the generator escapes & and backslashes.
	Backrefs string `yaml:"backrefs"`
}

// scriptPlaceholder is the argv placeholder a Script renders into.
const scriptPlaceholder = "script"

// Param is one model-facing argument.
type Param struct {
	Name        string    `yaml:"name"`
	Type        string    `yaml:"type"`
	Description string    `yaml:"description"`
	Required    bool      `yaml:"required"`
	Default     yaml.Node `yaml:"default"`
	Argv        yaml.Node `yaml:"argv"`

	// path
	Op            []string       `yaml:"op"`
	OpWhen        *OpWhen        `yaml:"op_when"`
	Repeated      bool           `yaml:"repeated"`
	MaxItems      int            `yaml:"max_items"`
	MustExist     bool           `yaml:"must_exist"`
	Kind          string         `yaml:"kind"`
	Target        string         `yaml:"target"`
	IntoDir       bool           `yaml:"into_dir"`
	Recursive     *bool          `yaml:"recursive"`
	RecursiveWhen map[string]any `yaml:"recursive_when"`
	Recursion     string         `yaml:"recursion"`
	Parents       *bool          `yaml:"parents"`
	ParentsWhen   map[string]any `yaml:"parents_when"`
	// ClobberWhen allows an existing destination only when it matches
	// (e.g. {overwrite: true}); otherwise the engine refuses the call.
	ClobberWhen map[string]any `yaml:"clobber_when"`
	// ProtectRoots refuses "/", the home directory and every entry
	// directly under "/" (or what such an entry links to) as a value,
	// whatever the scope (rm, mv).
	ProtectRoots bool `yaml:"protect_roots"`

	// enum
	Values map[string][]string `yaml:"values"`

	// int
	Min *int64 `yaml:"min"`
	Max *int64 `yaml:"max"`

	// string
	MaxLen  int    `yaml:"max_len"`
	Pattern string `yaml:"pattern"`
	Newline bool   `yaml:"newline"`

	op       scope.Op
	def      any // typed default, nil when none
	boolArgv map[bool][]string
	fragment []string
	re       *regexp.Regexp
	script   bool // consumed by the Script generator
}

// Timeout is the parsed per-call timeout.
func (s *Spec) TimeoutDuration() time.Duration { return s.timeout }

// Digest is "sha256:<hex>" of the spec source bytes.
func (s *Spec) Digest() string { return s.digest }

// Param returns the named param.
func (s *Spec) Param(name string) (*Param, bool) {
	p, ok := s.byName[name]
	return p, ok
}

// Ops returns the param's operations as scope bits.
func (p *Param) Ops() scope.Op { return p.op }

// LintError is a spec rejected at load time.
type LintError struct {
	Problems []string
}

func (e *LintError) Error() string {
	return "invalid tool spec: " + strings.Join(e.Problems, "; ")
}

// Parse decodes and lints one spec. Unknown keys are errors. Warnings are
// returned for constructs that are allowed but worth a second look.
func Parse(data []byte) (*Spec, error) {
	var s Spec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, &LintError{Problems: []string{"parse: " + err.Error()}}
	}
	sum := sha256.Sum256(data)
	s.digest = "sha256:" + hex.EncodeToString(sum[:])
	if probs := s.lint(); len(probs) > 0 {
		return nil, &LintError{Problems: probs}
	}
	return &s, nil
}
