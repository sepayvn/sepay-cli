package root

import (
	"fmt"
	"io"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var mapFormats = []string{"tree", "paths", "json"}

// commandInfo is one command in `--map=json`.
type commandInfo struct {
	Path        string     `json:"path"`
	Short       string     `json:"short,omitempty"`
	Aliases     []string   `json:"aliases,omitempty"`
	Args        string     `json:"args,omitempty"`
	Implemented bool       `json:"implemented"`
	Envs        string     `json:"envs"`
	Destructive string     `json:"destructive,omitempty"`
	Confirms    string     `json:"confirms,omitempty"`
	Scope       string     `json:"scope,omitempty"`
	Guidance    string     `json:"guidance,omitempty"`
	Flags       []flagInfo `json:"flags,omitempty"`
}

type flagInfo struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Desc      string `json:"desc,omitempty"`
	// Absent is not optional: conditional requirements are stated in Desc.
	Required bool `json:"required,omitempty"`
}

type commandMap struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// resolved, not the built-in default
	ActiveEnv string `json:"active_env"`
	// false until a profile is stored
	Authenticated bool          `json:"authenticated"`
	ActiveProfile string        `json:"active_profile,omitempty"`
	Commands      []commandInfo `json:"commands"`
}

// authState reads hosts.yml only (the keychain is too slow for every --help), following the profile a command
// would resolve, so a logout that leaves no active profile reports false.
func authState(f *cmdutil.Factory) (bool, string) {
	if f.Credentials == nil {
		return false, ""
	}
	mgr, err := f.Credentials()
	if err != nil {
		return false, ""
	}
	hosts := mgr.Hosts()
	name := f.ProfileName(hosts)
	return name != "" && hosts.Profiles[name] != nil, hosts.Active
}

// writeCommandMap omits stubs from tree and paths but lists them in json with implemented:false.
func writeCommandMap(w io.Writer, root *cobra.Command, f *cmdutil.Factory, format string) error {
	switch format {
	case "tree":
		writeTree(w, root, "")
		return nil
	case "paths":
		for _, c := range collect(root, true) {
			fmt.Fprintln(w, c.Path)
		}
		return nil
	case "json":
		loggedIn, profile := authState(f)
		return jsonfmt.RenderJSON(w, commandMap{
			Name:          root.Name(),
			Version:       build.Version,
			ActiveEnv:     f.ActiveEnv(),
			Authenticated: loggedIn,
			ActiveProfile: profile,
			Commands:      collect(root, false),
		})
	}
	return &cmdutil.FlagError{Err: fmt.Errorf(
		"invalid value %q for --map (must be %s)", format, strings.Join(mapFormats, ", "))}
}

func writeTree(w io.Writer, cmd *cobra.Command, indent string) {
	for _, c := range children(cmd, true) {
		line := indent + c.NameAndAliases()
		if c.Short != "" {
			line += "  " + c.Short
		}
		fmt.Fprintln(w, line)
		writeTree(w, c, indent+"  ")
	}
}

func collect(cmd *cobra.Command, runnableOnly bool) []commandInfo {
	var out []commandInfo
	for _, c := range children(cmd, runnableOnly) {
		out = append(out, describe(c))
		out = append(out, collect(c, runnableOnly)...)
	}
	return out
}

func children(cmd *cobra.Command, runnableOnly bool) []*cobra.Command {
	var out []*cobra.Command
	for _, c := range cmd.Commands() {
		if c.Name() == "help" || c.IsAdditionalHelpTopicCommand() {
			continue
		}
		if runnableOnly && c.Hidden {
			continue
		}
		out = append(out, c)
	}
	return out
}

func describe(cmd *cobra.Command) commandInfo {
	info := commandInfo{
		Path:        cmd.CommandPath(),
		Short:       cmd.Short,
		Aliases:     cmd.Aliases,
		Implemented: cmdutil.Annotation(cmd, cmdutil.AnnImplemented) != "no",
		Envs:        cmdutil.Annotation(cmd, cmdutil.AnnEnvs),
		Destructive: cmdutil.Annotation(cmd, cmdutil.AnnDestructive),
		Confirms:    cmdutil.Annotation(cmd, cmdutil.AnnConfirms),
		Scope:       cmdutil.Annotation(cmd, cmdutil.AnnScope),
		Guidance:    cmdutil.Annotation(cmd, cmdutil.AnnGuidance),
	}
	if info.Envs == "" {
		info.Envs = "both"
	}
	// The positional-arg spec only appears in UseLine.
	if use := strings.TrimSpace(strings.TrimPrefix(cmd.UseLine(), cmd.CommandPath())); use != "" && use != "[flags]" {
		info.Args = strings.TrimSuffix(use, " [flags]")
	}
	required := cmdutil.RequiredFlags(cmd)
	cmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		info.Flags = append(info.Flags, flagInfo{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
			Desc:      f.Usage,
			Required:  required[f.Name],
		})
	})
	return info
}
