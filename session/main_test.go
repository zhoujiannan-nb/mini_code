package session

import (
	"os"
	"testing"
)

// TestMain pins the exec tool to its one-shot cmd.exe engine for every
// session-package test. The loops under test here exercise turn/queue/ledger
// logic with fake models that sometimes run real exec calls; the persistent
// shell those calls would warm up outlives the test and holds the test's
// TempDir working directory, breaking t.TempDir's RemoveAll cleanup. The
// persistent engine itself has its own full coverage in the tools package
// (persistent_shell_test.go).
func TestMain(m *testing.M) {
	_ = os.Setenv("MINICODE_NO_PERSISTENT_SHELL", "1")
	os.Exit(m.Run())
}
