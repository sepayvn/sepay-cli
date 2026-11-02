package cmdutil

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// BindListFlags registers each flag plus --page/--per-page and returns a collector of the non-empty
// values. A flag's query param is its name with dashes as underscores.
func BindListFlags(fs *pflag.FlagSet, flags map[string]string) func() url.Values {
	vals := make(map[string]*string, len(flags)) // API param → bound value
	for flag, help := range flags {
		p := new(string)
		fs.StringVar(p, flag, "", help)
		vals[strings.ReplaceAll(flag, "-", "_")] = p
	}
	var page, perPage int
	fs.IntVar(&page, "page", 0, "Page number")
	fs.IntVar(&perPage, "per-page", 0, "Rows per page (max 100)")

	return func() url.Values {
		q := url.Values{}
		for param, p := range vals {
			if *p != "" {
				q.Set(param, *p)
			}
		}
		if page > 0 {
			q.Set("page", strconv.Itoa(page))
		}
		if perPage > 0 {
			q.Set("per_page", strconv.Itoa(perPage))
		}
		return q
	}
}
