package commands

import (
	"os"
	"testing"

	gokeyring "github.com/zalando/go-keyring"
)

// TestMain swaps the OS keychain for go-keyring's in-memory mock
// before any test runs. The keyring secrets backend is linked, so a
// test that loads a config naming it would otherwise read and write
// the operator's real keychain.
func TestMain(m *testing.M) {
	gokeyring.MockInit()
	os.Exit(m.Run())
}
