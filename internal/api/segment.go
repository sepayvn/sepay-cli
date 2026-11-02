package api

import (
	"errors"
	"net/url"
	"strings"
)

// ErrDotSegment refuses a path with a "." or ".." segment, which the server would resolve elsewhere.
var ErrDotSegment = errors.New(`refusing to send a request whose path has a "." or ".." segment`)

// HasDotSegment reports a "." or ".." segment in rawPath, decoded or not; query and fragment are ignored.
func HasDotSegment(rawPath string) bool {
	if i := strings.IndexAny(rawPath, "?#"); i >= 0 {
		rawPath = rawPath[:i]
	}
	forms := []string{rawPath}
	if decoded, err := url.PathUnescape(rawPath); err == nil && decoded != rawPath {
		forms = append(forms, decoded)
	}
	for _, form := range forms {
		for _, seg := range strings.Split(form, "/") {
			if seg == "." || seg == ".." {
				return true
			}
		}
	}
	return false
}
