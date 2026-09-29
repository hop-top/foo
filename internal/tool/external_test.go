package tool

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hop.top/kit/go/ai/ext/discover"
	"hop.top/kit/go/ai/llm"
)

const weatherSchema = `{"type":"object","properties":{"city":{"type":"string"},"days":{"type":"integer"}},"required":["city"]}`

// writePlugin drops an executable foo-tool-<file> script into a temp
// dir. --ext-info prints extInfo and appends a line to extinfo.log;
// a call copies its stdin to stdin.json and prints result.
func writePlugin(t *testing.T, file, extInfo, result string) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, "foo-tool-"+file)
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--ext-info\" ]; then\n" +
		"  echo x >> '" + filepath.Join(dir, "extinfo.log") + "'\n" +
		"  cat <<'JSON'\n" + extInfo + "\nJSON\n" +
		"  exit 0\n" +
		"fi\n" +
		"cat > '" + filepath.Join(dir, "stdin.json") + "'\n" +
		"cat <<'JSON'\n" + result + "\nJSON\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func foundAt(path, name string) *discover.Found {
	return &discover.Found{Name: name, Path: path}
}

func TestExternalToolFromFound_DeclaredParameters(t *testing.T) {
	path, dir := writePlugin(t, "weather",
		`{"name":"weather","version":"0.1.0","description":"Forecast","parameters":`+weatherSchema+`}`,
		`{"result":{}}`)

	got, err := ExternalToolFromFound(foundAt(path, "weather"))
	if err != nil {
		t.Fatalf("ExternalToolFromFound: %v", err)
	}
	if got.Name() != "weather" || got.Description() != "Forecast" || got.Path() != path {
		t.Errorf("tool = (%q, %q, %q); want (weather, Forecast, %s)",
			got.Name(), got.Description(), got.Path(), path)
	}
	assertJSONEqual(t, got.Parameters(), weatherSchema)

	log, err := os.ReadFile(filepath.Join(dir, "extinfo.log"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "\n"); n != 1 {
		t.Errorf("--ext-info ran %d times; want exactly 1", n)
	}
}

func TestExternalToolFromFound_NoParameters(t *testing.T) {
	for _, tc := range []struct{ name, extInfo string }{
		{"absent", `{"name":"demo","version":"0.1.0","description":"d"}`},
		{"null", `{"name":"demo","version":"0.1.0","description":"d","parameters":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := writePlugin(t, "demo", tc.extInfo, `{"result":{}}`)
			got, err := ExternalToolFromFound(foundAt(path, "demo"))
			if err != nil {
				t.Fatalf("ExternalToolFromFound: %v", err)
			}
			assertJSONEqual(t, got.Parameters(), `{"type":"object","properties":{}}`)
		})
	}
}

func TestExternalToolFromFound_InvalidParameters(t *testing.T) {
	for _, tc := range []struct{ name, params string }{
		{"string", `"city"`},
		{"array", `[{"type":"string"}]`},
		{"number", `3`},
		{"non-object type", `{"type":"string"}`},
		{"missing type", `{"properties":{"city":{"type":"string"}}}`},
		{"properties not an object", `{"type":"object","properties":["city"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := writePlugin(t, "bad",
				`{"name":"bad","version":"0.1.0","description":"d","parameters":`+tc.params+`}`,
				`{"result":{}}`)
			got, err := ExternalToolFromFound(foundAt(path, "bad"))
			if err == nil {
				t.Fatalf("invalid parameters %s accepted as %s", tc.params, got.Parameters())
			}
			var invalid *InvalidParametersError
			if !errors.As(err, &invalid) {
				t.Fatalf("error %T (%v); want *InvalidParametersError", err, err)
			}
			if invalid.Name != "bad" {
				t.Errorf("error name = %q; want %q", invalid.Name, "bad")
			}
			if invalid.Path != path {
				t.Errorf("error path = %q; want %q", invalid.Path, path)
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "parameters") {
				t.Errorf("error %q must name the binary and the parameters field", err)
			}
		})
	}
}

func TestExternalToolFromFound_BlankNameFallsBackToFilename(t *testing.T) {
	for _, name := range []string{``, `   `} {
		path, _ := writePlugin(t, "weather",
			`{"name":"`+name+`","version":"0.1.0","description":"Forecast"}`,
			`{"result":{}}`)
		got, err := ExternalToolFromFound(foundAt(path, "weather"))
		if err != nil {
			t.Fatalf("ExternalToolFromFound: %v", err)
		}
		if got.Name() != "weather" {
			t.Errorf("ext-info name %q registered as %q; want filename-derived %q", name, got.Name(), "weather")
		}
		if got.Description() != "Forecast" {
			t.Errorf("description = %q; want the --ext-info description", got.Description())
		}
	}
}

func TestExternalToolFromFound_NoExtInfoFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "foo-tool-legacy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ExternalToolFromFound(foundAt(path, "legacy"))
	if err != nil {
		t.Fatalf("a binary without --ext-info must still register: %v", err)
	}
	if got.Name() != "legacy" {
		t.Errorf("name = %q; want filename-derived %q", got.Name(), "legacy")
	}
	assertJSONEqual(t, got.Parameters(), `{"type":"object","properties":{}}`)
}

func TestDeclaresParameters(t *testing.T) {
	for _, tc := range []struct {
		schema string
		want   bool
	}{
		{weatherSchema, true},
		{`{"type":"object","properties":{}}`, false},
		{`{"type":"object"}`, false},
		{`{}`, false},
	} {
		tl := NewExternalTool("x", "x", "/x", json.RawMessage(tc.schema))
		if got := DeclaresParameters(tl); got != tc.want {
			t.Errorf("DeclaresParameters(%s) = %v; want %v", tc.schema, got, tc.want)
		}
	}
}

// scriptedClient is a ToolClient that asks for one tool call, then
// answers with whatever the tool returned. It records every request.
type scriptedClient struct {
	call      llm.ToolCall
	toolsSeen [][]llm.ToolDef
	msgsSeen  [][]llm.Message
}

func (c *scriptedClient) CallWithTools(
	_ context.Context, msgs []llm.Message, tools []llm.ToolDef,
) (llm.ToolResponse, error) {
	c.toolsSeen = append(c.toolsSeen, tools)
	c.msgsSeen = append(c.msgsSeen, append([]llm.Message(nil), msgs...))
	if len(c.toolsSeen) == 1 {
		return llm.ToolResponse{ToolCalls: []llm.ToolCall{c.call}}, nil
	}
	return llm.ToolResponse{Content: "final: " + msgs[len(msgs)-1].Content}, nil
}

// TestDispatch_PluginReceivesModelArguments runs a discovered plugin
// through the dispatcher: the model sees the declared schema, and the
// arguments it sends arrive on the plugin's stdin as
// {"name","arguments"}.
func TestDispatch_PluginReceivesModelArguments(t *testing.T) {
	path, dir := writePlugin(t, "weather",
		`{"name":"weather","version":"0.1.0","description":"Forecast","parameters":`+weatherSchema+`}`,
		`{"result":{"high":21}}`)
	ext, err := ExternalToolFromFound(foundAt(path, "weather"))
	if err != nil {
		t.Fatalf("ExternalToolFromFound: %v", err)
	}
	reg := NewRegistry()
	if err := reg.Register(ext); err != nil {
		t.Fatal(err)
	}

	args := `{"city":"Toronto","days":3}`
	client := &scriptedClient{call: llm.ToolCall{ID: "c1", Name: "weather", Arguments: json.RawMessage(args)}}
	out, err := NewDispatcher(client, reg, DispatchConfig{}).Run(context.Background(), "pack for Toronto")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(client.toolsSeen) == 0 || len(client.toolsSeen[0]) != 1 {
		t.Fatalf("model saw tools %+v; want the weather tool", client.toolsSeen)
	}
	assertJSONEqual(t, client.toolsSeen[0][0].Parameters, weatherSchema)

	stdin, err := os.ReadFile(filepath.Join(dir, "stdin.json"))
	if err != nil {
		t.Fatalf("plugin never ran: %v", err)
	}
	assertJSONEqual(t, stdin, `{"name":"weather","arguments":`+args+`}`)

	if !strings.Contains(out, `"high":21`) {
		t.Errorf("final answer %q does not carry the plugin result", out)
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON (%v): %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON (%v): %s", err, want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("JSON mismatch:\n got  %s\n want %s", gb, wb)
	}
}
