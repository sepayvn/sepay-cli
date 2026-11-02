package cmdutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenBrowser_RejectsNonHTTP(t *testing.T) {
	assert.False(t, OpenBrowser("file:///etc/passwd"))
	assert.False(t, OpenBrowser("javascript:alert(1)"))
	assert.False(t, OpenBrowser("-a calc"))
}
