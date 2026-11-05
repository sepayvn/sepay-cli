// Package config implements "sepay config" (persistent CLI settings).
package config

import (
	"fmt"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

var settableKeys = []string{
	"default_env",
	"sandbox.api_url",
	"production.api_url",
	"sandbox.pg_api_url",
	"production.pg_api_url",
	"sandbox.pg_checkout_url",
	"production.pg_checkout_url",
	"sandbox.bankhub_api_url",
	"production.bankhub_api_url",
	"sandbox.einvoice_api_url",
	"production.einvoice_api_url",
}

// NewCmdConfig builds the "sepay config" command group.
func NewCmdConfig(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "config <command>",
		Aliases: []string{"cfg"},
		Short:   "Persistent CLI settings",
		Long: `Settings that apply to every command, stored in config.yml (location: sepay help
environment). default_env picks the environment when --env is not passed; the
<env>.*_url keys point one product at another host, for testing.

"config get" prints every set value. "config set" with no arguments asks for the
key and value on a terminal.`,
	}
	cmd.AddCommand(newGetCmd(f), newSetCmd(f))
	return cmd
}

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "get [key]",
		Aliases: []string{"list", "ls"},
		Short:   "Print one setting, or all set values",
		Long: `Print the value of one setting. With no key, print every setting that has a value.
"sepay config set --help" lists the keys.`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: cmdutil.CompleteFirstArg(cobra.FixedCompletions(settableKeys, cobra.ShellCompDirectiveNoFileComp)),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := f.Config()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				if !validKey(args[0]) {
					return &cmdutil.FlagError{Err: fmt.Errorf("unknown key %q (valid: %s)", args[0], strings.Join(settableKeys, ", "))}
				}
				v := cfg.Get(args[0])
				return f.RenderOutput(map[string]string{args[0]: v}, func() error {
					fmt.Fprintln(f.IOStreams.Out, v)
					return nil
				})
			}
			values := map[string]string{}
			for _, k := range settableKeys {
				if v := cfg.Get(k); v != "" {
					values[k] = v
				}
			}
			return f.RenderOutput(values, func() error {
				t := jsonfmt.NewTable("KEY", "VALUE").Truncatable("VALUE")
				for _, k := range settableKeys {
					if v := cfg.Get(k); v != "" {
						t.AddRow(k, v)
					}
				}
				return f.RenderTable(t)
			})
		},
	}
}

func newSetCmd(f *cmdutil.Factory) *cobra.Command {
	return cmdutil.Annotate(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a setting and persist it to config.yml",
		Long: `Set one setting in config.yml. Keys:
  default_env                    sandbox or production; asks to confirm production
  <env>.api_url                  core API host, where <env> is sandbox or production
  <env>.pg_api_url, <env>.pg_checkout_url, <env>.bankhub_api_url, <env>.einvoice_api_url

<key> and <value> are prompted for on a terminal; required otherwise. An empty
value ("") clears the setting.`,
		Example: `  sepay config set default_env production
  sepay config set sandbox.api_url http://localhost:8079/api/v2`,
		Args: cobra.MaximumNArgs(2),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			switch {
			case len(args) == 0:
				return settableKeys, cobra.ShellCompDirectiveNoFileComp
			case len(args) == 1 && args[0] == "default_env":
				return []string{config.EnvSandbox, config.EnvProduction}, cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			// Arity, not emptiness: `set <key> ""` deliberately blanks a setting.
			key, err := resolveKey(f, args)
			if err != nil {
				return err
			}
			if !validKey(key) {
				return &cmdutil.FlagError{Err: fmt.Errorf("unknown key %q (valid: %s)", key, strings.Join(settableKeys, ", "))}
			}
			cfg, err := f.Config()
			if err != nil {
				return err
			}
			value, err := resolveValue(f, cfg, key, args)
			if err != nil {
				return err
			}
			if key == "default_env" && !config.ValidEnv(value) {
				return &cmdutil.FlagError{Err: fmt.Errorf("default_env must be %s or %s", config.EnvSandbox, config.EnvProduction)}
			}
			// A production default silently moves every later command onto real money.
			if key == "default_env" && value == config.EnvProduction {
				if err := f.ConfirmMutation("make production the default environment for every command", true); err != nil {
					return err
				}
			}
			cfg.Set(key, value)
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("could not save config: %w", err)
			}
			f.ErrPrintf("%s set %s = %s\n", f.SuccessIcon(), key, value)
			return nil
		},
	}, map[string]string{
		cmdutil.AnnGuidance: "`config set default_env production` moves every later command onto production (real money) with nothing on the command line to show for it. Check `sepay --map=json` active_env after changing it.",
		cmdutil.AnnConfirms: "conditional",
	})
}

// resolveKey takes the key argument or picks one on a TTY.
func resolveKey(f *cmdutil.Factory, args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("a key and a value are required (valid keys: %s)", strings.Join(settableKeys, ", "))}
	}
	return f.SelectOrFlag("", "", "Setting", settableKeys)
}

// resolveValue takes the value argument or prompts, defaulting to the stored value.
func resolveValue(f *cmdutil.Factory, cfg *config.Config, key string, args []string) (string, error) {
	if len(args) > 1 {
		return args[1], nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("a value for %s is required", key)}
	}
	if key == "default_env" {
		return f.SelectOrFlag("", "", "Environment", []string{config.EnvSandbox, config.EnvProduction})
	}
	value, err := f.InputWithDefault(key, cfg.Get(key))
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("a value for %s is required", key)}
	}
	return value, nil
}

func validKey(key string) bool {
	for _, k := range settableKeys {
		if k == key {
			return true
		}
	}
	return false
}
