// Package envvar reads the CLI's on/off environment switches.
package envvar

import (
	"os"
	"strings"
)

var offValues = map[string]bool{"": true, "0": true, "false": true, "no": true, "off": true}

// Enabled reports whether the switch named by key is on. It reads the value, not
// the presence: someone who writes NO_COLOR=0 means off.
func Enabled(key string) bool {
	return !offValues[strings.ToLower(strings.TrimSpace(os.Getenv(key)))]
}
