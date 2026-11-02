package envvar

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEnabled(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"yes", true},
		{"anything", true},
		{" 1 ", true},
		{"", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{"no", false},
		{"off", false},
		{" 0 ", false},
	}
	for _, c := range cases {
		t.Run(c.value, func(t *testing.T) {
			t.Setenv("SEPAY_TEST_SWITCH", c.value)
			assert.Equal(t, c.want, Enabled("SEPAY_TEST_SWITCH"))
		})
	}
}

func TestEnabled_unset(t *testing.T) {
	assert.False(t, Enabled("SEPAY_TEST_SWITCH_UNSET"))
}
