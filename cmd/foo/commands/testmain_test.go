package commands

import (
	"fmt"
	"os"
	"testing"

	gokeyring "github.com/zalando/go-keyring"

	"hop.top/foo/internal/egress"
)

// TestMain makes the suite hermetic before any test runs.
//
// The OS keychain is swapped for go-keyring's in-memory mock: the
// keyring secrets backend is linked, so a test that loads a config
// naming it would otherwise read and write the operator's real
// keychain.
//
// The passive update notice is opted out the way a user would, through
// its environment switch: every command run through the root's pre-run
// would otherwise ask api.github.com for the latest release. A test of
// the notice itself clears the switch with t.Setenv.
//
// Every non-loopback request is routed to a refusing egress recorder,
// and the suite fails if anything reached it: a test that needs a
// server stands one up on loopback.
func TestMain(m *testing.M) {
	gokeyring.MockInit()
	_ = os.Setenv(upgradeNoticeOptOutEnv, "1")

	rec, err := egress.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "egress recorder:", err)
		os.Exit(1)
	}
	rec.Install()

	code := m.Run()
	if seen := rec.Stop(); len(seen) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: suite attempted network egress: %v\n", seen)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
