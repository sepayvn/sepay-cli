package cmdutil

import (
	"fmt"
	"net/url"
	"strings"
)

// PathSegment escapes value as one URL path segment, refusing empty, ".", ".." or a slash,
// checked before and after percent-decoding since the server decodes before routing.
func PathSegment(name, value string) (string, error) {
	forms := []string{value}
	if decoded, err := url.PathUnescape(value); err == nil && decoded != value {
		forms = append(forms, decoded)
	}
	for _, v := range forms {
		if v == "" || v == "." || v == ".." || strings.ContainsAny(v, `/\`) {
			return "", &FlagError{Err: fmt.Errorf(`%s must be a single id: not empty, "." or "..", and without "/"`, name)}
		}
	}
	return url.PathEscape(value), nil
}
