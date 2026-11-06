package locale

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Lang
	}{
		{"SEPAY_LANG vi beats an English LANG", map[string]string{"SEPAY_LANG": "vi", "LANG": "en_US.UTF-8"}, VI},
		{"SEPAY_LANG en beats a Vietnamese LC_ALL", map[string]string{"SEPAY_LANG": "EN", "LC_ALL": "vi_VN.UTF-8"}, EN},
		{"an unknown SEPAY_LANG is ignored", map[string]string{"SEPAY_LANG": "fr", "LANG": "vi_VN.UTF-8"}, VI},
		{"LC_ALL vi", map[string]string{"LC_ALL": "vi_VN.UTF-8"}, VI},
		{"LC_ALL C hides LANG vi", map[string]string{"LC_ALL": "C", "LANG": "vi_VN.UTF-8"}, EN},
		{"LC_MESSAGES vi", map[string]string{"LC_MESSAGES": "vi_VN", "LANG": "en_US.UTF-8"}, VI},
		{"bare vi", map[string]string{"LANG": "vi"}, VI},
		{"C.UTF-8 is English", map[string]string{"LANG": "C.UTF-8"}, EN},
		{"a vi-prefixed name of another language is not Vietnamese", map[string]string{"LANG": "vix"}, EN},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, Detect(func(k string) string { return c.env[k] }))
		})
	}
}

func TestFromTag_ReadsWindowsTags(t *testing.T) {
	assert.Equal(t, VI, fromTag("vi-VN"))
	assert.Equal(t, EN, fromTag("en-US"))
	assert.Equal(t, EN, fromTag(""))
}
