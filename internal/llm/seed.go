// Default-pool seeding. First-run UX: an operator who just installed
// foo and ran `foo "hi"` shouldn't have to author a pool config block
// before the picker becomes useful. SeedDefaultPool writes a sensible
// 3-tier default to ~/.config/hop/llm.yaml when that file is absent.
//
// An llm.yaml that exists is the operator's, whatever it holds — no
// pool block, an empty file, a symlink (even a dangling one), a file
// foo cannot read or parse. foo never edits it; the seed is a one-time
// create, not a merge.

package llm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"hop.top/kit/go/core/xdg"
)

// defaultPoolYAML is the content of a freshly seeded llm.yaml. Three
// tiers × major providers — enough to exercise the picker without
// overwhelming a first-time user. Operators are
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

// SeedDefaultPool creates ~/.config/hop/llm.yaml (or
// $XDG_CONFIG_HOME/hop/llm.yaml) holding the default pool when nothing
// is at that path. Returns (wrote, err). wrote == true means foo
// created the file; err signals a hard failure (path resolution,
// permission, write). Anything already at the path yields wrote=false,
// err=nil and is never touched.
//
// When wrote == true, callers should print one informational line on
// stderr so the seeding is discoverable. SeedDefaultPool itself does
// not write to any stream.
func SeedDefaultPool() (wrote bool, err error) {
	path, err := SeedPath()
	if err != nil {
		return false, fmt.Errorf("resolve XDG config dir: %w", err)
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return false, fmt.Errorf("create config dir: %w", mkErr)
	}
	if writeErr := writeNew(path, []byte(defaultPoolYAML)); writeErr != nil {
		if errors.Is(writeErr, fs.ErrExist) {
			// The operator's file, or a concurrent first run's.
			return false, nil
		}
		return false, fmt.Errorf("write seed: %w", writeErr)
	}
	return true, nil
}

// writeNew creates path with data, owner-only (0600: llm.yaml is where
// operators put providers.<scheme>.api_key), and fails with
// fs.ErrExist when anything is already there. O_EXCL makes the
// existence check and the create one atomic step, so there is no
// window for a concurrent run or an editor save to be overwritten; it also refuses a symlink,
// dangling or not, where a check-then-write would follow the link. A
// failed write removes the partial file so a half-written seed never
// passes for an operator's config on the next run.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
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
