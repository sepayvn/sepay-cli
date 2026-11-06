//go:build !windows

package locale

// systemLanguage has nothing beyond the environment to read outside Windows.
func systemLanguage() string { return "" }
