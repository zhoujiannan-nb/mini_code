package session

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("MINICODE_NO_PERSISTENT_SHELL", "1")
	os.Exit(m.Run())
}
