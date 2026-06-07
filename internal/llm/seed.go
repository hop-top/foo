// Default-pool seeding. First-run UX: an operator who just installed
// foo and ran `foo "hi"` shouldn't have to author a pool config block
// before the picker becomes useful. SeedDefaultPool writes a sensible
// 3-tier default into ~/.config/hop/llm.yaml when the file has no pool
// block, preserving any existing keys (default, providers, fallback).
//
// Idempotent by design: if pool: already exists (even empty), the seed
// is a no-op. Parsing failures leave the file alone — operator config
// stays the source of truth.

package llm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
	"hop.top/kit/go/core/xdg"
)

// defaultPoolYAML is the YAML block appended/written when no pool: is
// present. Three tiers × major providers — enough to exercise the
// picker without overwhelming a first-time user. Operators are
// expected to edit this file (the seed comment says as much).
//
// Model IDs are conservative defaults chosen from each provider's
// publicly-documented GA model list. Verify against the provider's
// current catalog before use:
//   - OpenAI:    https://platform.openai.com/docs/models
//   - Anthropic: https://docs.anthropic.com/en/docs/about-claude/models
//   - Google:    https://ai.google.dev/gemini-api/docs/models/gemini
//
// Google's premium tier is intentionally omitted: the 2.0-generation
// lineup does not include a publicly-released ultra-class model.
// Operators wanting a third premium provider can add it after
// confirming the model ID against the catalog above.
const defaultPoolYAML = `# Default pool seeded by foo on first run. Edit to taste:
# - alias is optional; useful when LLM_POOL_DISABLE references entries.
# - enabled defaults to true; set to false to mute an entry without removing.
# - weight defaults to 1.0; used by future load-distribution policy.
# - model IDs are conservative defaults; verify against each provider's
#   current catalog (links in foo's docs/how-to/route-across-models.md).
pool:
  - alias: cheap-openai
    scheme: openai
    model: gpt-4o-mini
  - alias: cheap-anthropic
    scheme: anthropic
    model: claude-3-5-haiku-latest
  - alias: cheap-google
    scheme: google
    model: gemini-2.0-flash
  - alias: balanced-openai
    scheme: openai
    model: gpt-4o
  - alias: balanced-anthropic
    scheme: anthropic
    model: claude-3-5-sonnet-latest
  - alias: balanced-google
    scheme: google
    model: gemini-1.5-pro
  - alias: premium-openai
    scheme: openai
    model: o1
  - alias: premium-anthropic
    scheme: anthropic
    model: claude-opus-latest
  # Google premium tier intentionally omitted; add after confirming
  # the model ID against Google's current GA catalog.
`

// SeedDefaultPool inspects ~/.config/hop/llm.yaml (or
// $XDG_CONFIG_HOME/hop/llm.yaml) and writes the default pool block
// when missing. Returns (wrote, err). wrote == true means the file was
// touched on disk; err signals a hard failure (path resolution,
// permission). Soft conditions — file unreadable, YAML invalid — are
// surfaced via wrote=false, err=nil so a corrupt operator config never
// stops foo from running.
//
// When wrote == true, callers should print one informational line on
// stderr so the seeding is discoverable. SeedDefaultPool itself does
// not write to any stream.
func SeedDefaultPool() (wrote bool, err error) {
	dir, err := xdg.ConfigDir("hop")
	if err != nil {
		return false, fmt.Errorf("resolve XDG config dir: %w", err)
	}
	path := filepath.Join(dir, "llm.yaml")

	data, readErr := os.ReadFile(path)
	switch {
	case errors.Is(readErr, fs.ErrNotExist):
		// File does not exist — write a fresh one with just the pool
		// block. Operators can add default/providers/fallback later.
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return false, fmt.Errorf("create config dir: %w", mkErr)
		}
		if writeErr := atomicWrite(path, []byte(defaultPoolYAML)); writeErr != nil {
			return false, fmt.Errorf("write seed: %w", writeErr)
		}
		return true, nil
	case readErr != nil:
		// Unreadable for some other reason (permissions). Leave
		// alone — the operator's environment is in a weirder state
		// than we can fix from here.
		return false, nil
	}

	// File exists. Check whether a pool: key is already present.
	// Parse loosely; YAML errors leave the file untouched.
	var raw map[string]any
	if unmarshalErr := yaml.Unmarshal(data, &raw); unmarshalErr != nil {
		return false, nil
	}
	if _, has := raw["pool"]; has {
		// Operator already authored a pool — even an empty one
		// expresses intent. Respect it.
		return false, nil
	}

	// Append the pool block. Always prepend a newline so we don't
	// smash a key onto the operator's last line; a doubled newline
	// when the file already ends with one is cosmetic.
	suffix := "\n" + defaultPoolYAML
	if writeErr := atomicWrite(path, append(data, []byte(suffix)...)); writeErr != nil {
		return false, fmt.Errorf("append seed: %w", writeErr)
	}
	return true, nil
}

// atomicWrite writes data to path via a same-directory tempfile + rename.
// os.Rename within a directory is atomic on POSIX, so concurrent first-
// run invocations can race the seed without corrupting the destination.
// The tempfile is best-effort cleaned up on rename failure.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".llm-seed-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// SeedPath returns the path SeedDefaultPool writes to. Used by tests
// and by the info-line printer to avoid duplicating the xdg.ConfigDir
// dance.
func SeedPath() (string, error) {
	dir, err := xdg.ConfigDir("hop")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llm.yaml"), nil
}
