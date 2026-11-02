package webhook

import (
	"os"
	"testing"
)

// TestMain isolates the config dir so listen tests never touch a developer's live retry state.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sepay-webhook-test")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("SEPAY_CONFIG_DIR", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
