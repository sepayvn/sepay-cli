package cmdutil

import (
	"fmt"

	"github.com/spf13/pflag"
)

// AnnAliasOf marks a hidden flag kept as another spelling; its value names the visible flag.
const AnnAliasOf = "sepay_alias_of"

// RenameFlag registers name as the visible spelling of the existing flag old, with usage as
// its help text. Both spellings write the same value, so whatever old sends to the API is
// unchanged; old stays accepted but hidden. Passing both is a usage error.
//
// Changed is per spelling: code that tests fs.Changed must test both names.
func RenameFlag(fs *pflag.FlagSet, name, old, usage string) {
	shareFlag(fs, old, name, usage)
	hideAs(fs.Lookup(old), name)
}

// AliasFlag registers alias as a hidden second spelling of the existing flag name, under the
// same rules as RenameFlag.
func AliasFlag(fs *pflag.FlagSet, alias, name string) {
	hideAs(shareFlag(fs, name, alias, fs.Lookup(name).Usage), name)
}

func shareFlag(fs *pflag.FlagSet, existing, added, usage string) *pflag.Flag {
	prev := fs.Lookup(existing)
	if prev == nil {
		panic("cmdutil: no flag --" + existing)
	}
	shared, setBy := prev.Value, new(string)
	prev.Value = &aliasedValue{Value: shared, name: existing, other: added, setBy: setBy}
	fs.Var(&aliasedValue{Value: shared, name: added, other: existing, setBy: setBy}, added, usage)
	fl := fs.Lookup(added)
	fl.DefValue, fl.NoOptDefVal = prev.DefValue, prev.NoOptDefVal
	// pflag hides an empty "[]" default only on its own slice types, not on this wrapper.
	if fl.DefValue == "[]" {
		fl.DefValue = ""
	}
	return fl
}

func hideAs(fl *pflag.Flag, visible string) {
	fl.Hidden = true
	if fl.Annotations == nil {
		fl.Annotations = map[string][]string{}
	}
	fl.Annotations[AnnAliasOf] = []string{visible}
}

// aliasedValue is one spelling of a shared flag value; setBy remembers which spelling wrote it.
type aliasedValue struct {
	pflag.Value
	name, other string
	setBy       *string
}

func (v *aliasedValue) Set(s string) error {
	if *v.setBy != "" && *v.setBy != v.name {
		return &FlagError{Err: fmt.Errorf("--%s and --%s are the same flag; pass one", v.other, v.name)}
	}
	*v.setBy = v.name
	return v.Value.Set(s)
}
