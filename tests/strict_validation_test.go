package tests

import (
	"testing"

	"hop.top/foo/cmd/foo/commands"
)

// TestRoot_StrictValidate_Passes locks in kit's strict-validation
// contract for the foo command tree.
//
// Root.Validate() walks every runnable leaf and enforces:
//   - kit/side-effect annotation set to a valid value
//   - kit/idempotent annotation set (after auto-apply of verb defaults)
//   - cmd.Short on every leaf and group; cmd.Long on every leaf
//   - reserved `status` subcommand mounted on the root
//   - shape rules: depth-1 leaves carry kit/top-level-verb,
//     depth>=3 leaves require kit/hierarchical on every intermediate
//   - passthrough rules where applicable
//
// A non-nil return is a regression: it means a new command was added
// without kit annotations, or a kit-required surface was removed.
// Reproduce the exact failure list with `./bin/foo --help`.
func TestRoot_StrictValidate_Passes(t *testing.T) {
	root := commands.New("test")
	if err := root.Validate(); err != nil {
		t.Fatalf("Root.Validate must return nil; got: %v", err)
	}
}
