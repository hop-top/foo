package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"hop.top/foo/internal/tool/gate"
)

// LinkPrefix names the links `foo tool install` creates: foo-tool-<name>.
const LinkPrefix = "foo-tool-"

// MultiCallName reports the tool name when foo was started through a
// foo-tool-<name> link (argv[0]).
func MultiCallName(argv0 string) (string, bool) {
	base := filepath.Base(argv0)
	name, ok := strings.CutPrefix(base, LinkPrefix)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// DenyAll refuses every call with Message: a fail-closed authorizer
// for tests and for engines that must never run a command.
type DenyAll struct{ Message string }

func (d DenyAll) Authorize(context.Context, gate.Request) (gate.Grant, error) {
	return gate.Grant{}, &gate.Error{Kind: gate.KindDenied, Message: d.Message}
}

// ExtInfo is the --ext-info payload for a shim (§10): kit's discovery
// fields, the model-facing parameters, and foo's path annotations
// under foo_tool, which never reach the model.
type ExtInfo struct {
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Description  string          `json:"description"`
	Capabilities []string        `json:"capabilities"`
	Parameters   json.RawMessage `json:"parameters"`
	FooTool      FooTool         `json:"foo_tool"`
}

// FooTool is foo's annotation namespace in --ext-info.
type FooTool struct {
	Spec         int                   `json:"spec"`
	SideEffect   string                `json:"side_effect"`
	SideEffectIf []Escalation          `json:"side_effect_if,omitempty"`
	Network      string                `json:"network"`
	Paths        map[string]PathAnnots `json:"paths"`
	Digest       string                `json:"digest"`
}

// PathAnnots describes one path param to a host.
type PathAnnots struct {
	Op            []string       `json:"op"`
	OpWhen        *OpWhen        `json:"op_when,omitempty"`
	Target        string         `json:"target"`
	IntoDir       bool           `json:"into_dir,omitempty"`
	MustExist     bool           `json:"must_exist,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	Recursive     *bool          `json:"recursive,omitempty"`
	RecursiveWhen map[string]any `json:"recursive_when,omitempty"`
	Recursion     string         `json:"recursion,omitempty"`
	ClobberWhen   map[string]any `json:"clobber_when,omitempty"`
}

// NewExtInfo builds the payload for l on the given flavor variant.
func NewExtInfo(l *Loaded, v *Variant, version string) ExtInfo {
	s := l.Spec
	paths := map[string]PathAnnots{}
	for _, p := range s.Params {
		if p.Type != TypePath {
			continue
		}
		paths[p.Name] = PathAnnots{
			Op: p.Op, OpWhen: p.OpWhen, Target: p.Target, IntoDir: p.IntoDir, MustExist: p.MustExist,
			Kind: p.Kind, Recursive: p.Recursive, RecursiveWhen: p.RecursiveWhen, Recursion: p.Recursion,
			ClobberWhen: p.ClobberWhen,
		}
	}
	return ExtInfo{
		Name:         s.Name,
		Version:      version,
		Description:  modelDescription(s),
		Capabilities: []string{"discover"},
		Parameters:   s.schema(v, "the working directory"),
		FooTool: FooTool{
			Spec: s.Version, SideEffect: s.SideEffect, SideEffectIf: s.SideEffectIf,
			Network: s.Network, Paths: paths, Digest: s.digest,
		},
	}
}

// MultiCall is foo running as a foo-tool-<name> plugin for another
// host: the external plugin protocol over stdin/stdout, with the same
// validation and authorization as in-process calls.
type MultiCall struct {
	Name    string
	Version string
	Catalog *Catalog
	Engine  *Engine
	// Authorizer, when set, builds Engine's authorizer for a request.
	// It runs only once the name and arguments check out, so
	// --ext-info never reads the user's policy files. Its error is a
	// broken setup, not a refusal of the call.
	Authorizer func() (gate.Authorizer, error)
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
}

// maxRequest bounds the stdin request.
const maxRequest = 1 << 20

// Run serves one invocation and returns the process exit code: 0 when a
// protocol response was written (including refusals, which are
// responses), 1 when the authorizer cannot be built (reported on
// Stderr, no response), 2 for usage errors, 3 when no spec has this
// name.
func (m *MultiCall) Run(ctx context.Context, args []string) int {
	l, inv := m.Catalog.Lookup(m.Name)
	if l == nil {
		msg := fmt.Sprintf("no tool spec named %q", m.Name)
		if inv != nil {
			msg = inv.Error()
		}
		_, _ = fmt.Fprintf(m.Stderr, "%s%s: %s\n", LinkPrefix, m.Name, msg)
		return 3
	}
	switch {
	case len(args) == 1 && args[0] == "--ext-info":
		data, _ := json.Marshal(NewExtInfo(l, m.Engine.variant(l), m.Version))
		_, _ = fmt.Fprintln(m.Stdout, string(data))
		return 0
	case len(args) > 0:
		_, _ = fmt.Fprintf(m.Stderr, "usage: %s%s [--ext-info] < request.json\n", LinkPrefix, m.Name)
		return 2
	}

	if m.Authorizer != nil {
		auth, err := m.Authorizer()
		if err != nil {
			_, _ = fmt.Fprintf(m.Stderr, "%s%s: %v\n", LinkPrefix, m.Name, err)
			return 1
		}
		m.Engine.Authorizer = auth
	}

	data, err := io.ReadAll(io.LimitReader(m.Stdin, maxRequest+1))
	if err != nil || len(data) > maxRequest {
		return m.respondErr(&gate.Error{Kind: KindProtocol, Message: "request unreadable or larger than 1 MiB"})
	}
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return m.respondErr(&gate.Error{Kind: KindProtocol, Message: "request: " + err.Error()})
	}
	if req.Name != "" && req.Name != m.Name {
		return m.respondErr(&gate.Error{Kind: KindProtocol, Message: fmt.Sprintf("request names %q; this is %q", req.Name, m.Name)})
	}
	res, err := m.Engine.Call(ctx, l, req.Arguments)
	if err != nil {
		return m.respondErr(err)
	}
	out, _ := json.Marshal(map[string]any{"result": res})
	_, _ = fmt.Fprintln(m.Stdout, string(out))
	return 0
}

// respondErr writes the protocol error: "error" stays a string, as the
// external plugin protocol defines it; the structured form rides in
// "error_detail" for hosts that want it.
func (m *MultiCall) respondErr(err error) int {
	body := NewErrorBody(err)
	out, _ := json.Marshal(map[string]any{
		"error":        body.Kind + ": " + body.Message,
		"error_detail": body,
	})
	_, _ = fmt.Fprintln(m.Stdout, string(out))
	return 0
}
