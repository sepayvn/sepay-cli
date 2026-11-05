// Package apikey implements "sepay apikey".
package apikey

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// maxNameLen mirrors the server's limit.
const maxNameLen = 200

// APIKey is one static API key from GET /v2/apikey; APIKey is masked everywhere except create's reply.
type APIKey struct {
	ID        string      `json:"id" yaml:"id"`
	Name      string      `json:"name" yaml:"name"`
	APIKey    string      `json:"api_key" yaml:"api_key"`
	Active    api.FlexInt `json:"active" yaml:"active"`
	CreatedAt string      `json:"created_at" yaml:"created_at"`
}

// NewCmdAPIKey builds the "sepay apikey" command group.
func NewCmdAPIKey(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "apikey <command>",
		Aliases: []string{"ak"},
		Short:   "API keys for non-CLI integrations",
		Long: `Manage the static API keys a non-CLI integration authenticates with.

An API key is sent as "Authorization: Bearer <api_key>" on SePay API v2 and acts
for the whole company. The CLI authenticates with OAuth and never presents one.
Hosts and headers: https://developer.sepay.vn/en/sepay-api/v2/xac-thuc.

Sandbox only: every subcommand refuses --env production. A production key is
created in the my.sepay.vn portal.

create prints the key in full, and is the only command that can: the server masks
it everywhere else, so store it then or create a replacement.`,
		// Every apikey endpoint is sandbox-only, so refuse production by name rather than as a 404.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return f.AssertSandboxOnly(strings.TrimPrefix(cmd.CommandPath(), "sepay "))
		},
	}
	cmd.AddCommand(
		newListCmd(f),
		newGetCmd(f),
		newCreateCmd(f),
		newUpdateCmd(f),
		newDeleteCmd(f),
	)
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs:     "sandbox",
		cmdutil.AnnGuidance: "These keys are for a non-CLI integration, not for this CLI. Only create returns the key in full; the server masks it on every other endpoint. Host and header: https://developer.sepay.vn/en/sepay-api/v2/xac-thuc.",
	})
}

// fetchAll lists the company's keys (one unpaginated array).
func fetchAll(ctx context.Context, f *cmdutil.Factory) ([]APIKey, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return nil, err
	}
	env, err := client.Get(ctx, "/apikey", nil)
	if err != nil {
		return nil, err
	}
	keys, _, err := api.ListInto[APIKey](env)
	return keys, err
}

// pickOne returns val when set, else prompts on a TTY; never guesses a credential.
func pickOne(ctx context.Context, f *cmdutil.Factory, val string) (string, error) {
	if val != "" {
		return val, nil
	}
	if !f.CanPrompt() {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("an API key xid is required")}
	}
	keys, err := fetchAll(ctx, f)
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("no API keys yet; create one with: sepay apikey create --name <name>")}
	}
	choices := make([]cmdutil.PromptChoice, 0, len(keys))
	for _, k := range keys {
		choices = append(choices, cmdutil.PromptChoice{Label: k.Name + " · " + k.APIKey, Value: k.ID})
	}
	return f.SelectChoiceOrFlag("", "", "API key", choices)
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("--name is required")}
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return "", &cmdutil.FlagError{Err: fmt.Errorf("--name must be at most %d characters", maxNameLen)}
	}
	return name, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newListCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List API keys (sandbox only)",
		Example: `  sepay apikey list
  sepay apikey list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd.Context(), f)
		},
	}
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnEnvs: "sandbox"})
}

func runList(ctx context.Context, f *cmdutil.Factory) error {
	keys, err := fetchAll(ctx, f)
	if err != nil {
		return err
	}
	return f.RenderOutput(keys, func() error {
		if len(keys) == 0 {
			fmt.Fprintln(f.IOStreams.ErrOut, "No API keys found.")
			return nil
		}
		t := jsonfmt.NewTable("ID", "NAME", "KEY", "ACTIVE", "CREATED").Truncatable("NAME")
		for _, k := range keys {
			t.AddRow(k.ID, k.Name, k.APIKey, f.YesNo(int(k.Active) == 1), k.CreatedAt)
		}
		return f.RenderTable(t)
	})
}

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <api-key-xid>",
		Short: "Show an API key (sandbox only)",
		Long: `Show one API key. Its value is masked: only "apikey create" prints a key in full.

<api-key-xid> is the ID column of "sepay apikey list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := pickOne(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			return runGet(cmd.Context(), f, xid)
		},
	}
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnEnvs: "sandbox"})
}

func runGet(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/apikey/"+xidSeg, nil)
	if err != nil {
		return err
	}
	k, err := api.DataInto[APIKey](env)
	if err != nil {
		return err
	}
	return f.RenderOutput(k, func() error { return renderDetail(f, k) })
}

func renderDetail(f *cmdutil.Factory, k APIKey) error {
	t := jsonfmt.NewTable("FIELD", "VALUE").Truncatable("VALUE")
	t.AddRow("ID", k.ID)
	t.AddRow("Name", k.Name)
	t.AddRow("Key", k.APIKey)
	t.AddRow("Active", f.YesNo(int(k.Active) == 1))
	t.AddRow("Created", k.CreatedAt)
	return f.RenderTable(t)
}

// createBody is the POST /v2/apikey request; the server requires active.
type createBody struct {
	Name   string `json:"name"`
	Active int    `json:"active"`
}

func newCreateCmd(f *cmdutil.Factory) *cobra.Command {
	var name string
	var active bool
	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create an API key (sandbox only)",
		Example: `  sepay apikey create --name "billing service"`,
		Args:    cmdutil.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			if name, err = f.InputOrFlag(name, "name", "Key name"); err != nil {
				return err
			}
			if name, err = validateName(name); err != nil {
				return err
			}
			return runCreate(cmd.Context(), f, createBody{Name: name, Active: boolToInt(active)})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "Key name")
	fl.BoolVar(&active, "active", true, "Create the key enabled")
	cmdutil.MarkRequired(cmd, "name")
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnEnvs: "sandbox"})
}

func runCreate(ctx context.Context, f *cmdutil.Factory, body createBody) error {
	if err := f.ConfirmMutation("create API key", false); err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	// The reply holds a plaintext secret, so the server refuses a retry rather than minting a second key.
	key, err := f.IdempotencyKey()
	if err != nil {
		return err
	}
	env, err := client.Post(ctx, "/apikey", api.WriteOptions{Body: body, IdempotencyKey: key})
	if err != nil {
		return err
	}
	k, err := api.DataInto[APIKey](env)
	if err != nil {
		return err
	}
	return f.RenderOutput(k, func() error {
		f.ErrPrintf("Created API key %s.\n", k.ID)
		return renderDetail(f, k)
	})
}

func newUpdateCmd(f *cmdutil.Factory) *cobra.Command {
	var name string
	var active bool
	cmd := &cobra.Command{
		Use:     "update <api-key-xid>",
		Aliases: []string{"edit"},
		Short:   "Update an API key's name or status (sandbox only)",
		Long: `Change an API key's name or active status; only the flags you pass change. With
no flags on a terminal, each field is asked for, blank to keep.

<api-key-xid> is the ID column of "sepay apikey list". Prompted for on a terminal; required otherwise.`,
		Args: cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			body := map[string]any{}
			if fl.Changed("name") {
				validated, err := validateName(name)
				if err != nil {
					return err
				}
				body["name"] = validated
			}
			if fl.Changed("active") {
				body["active"] = boolToInt(active)
			}
			// The server rejects an empty patch.
			if len(body) == 0 && !f.CanPrompt() {
				return &cmdutil.FlagError{Err: fmt.Errorf("nothing to update; pass --name and/or --active")}
			}
			xid, err := pickOne(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			// After the picker, so "blank to keep" refers to a chosen key.
			if len(body) == 0 {
				if err := promptUpdateFields(f, body); err != nil {
					return err
				}
				if len(body) == 0 {
					return &cmdutil.FlagError{Err: fmt.Errorf("nothing to update; pass --name and/or --active")}
				}
			}
			return runUpdate(cmd.Context(), f, xid, body)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&name, "name", "", "New key name")
	fl.BoolVar(&active, "active", true, "Enable (--active) or disable (--active=false) the key")
	return cmdutil.Annotate(cmd, map[string]string{cmdutil.AnnEnvs: "sandbox"})
}

// promptUpdateFields fills body with only the fields the user changed.
func promptUpdateFields(f *cmdutil.Factory, body map[string]any) error {
	name, err := f.Input("New key name (blank to keep)")
	if err != nil {
		return err
	}
	// Trim first, or whitespace reads as a rename and fails validateName.
	if name = strings.TrimSpace(name); name != "" {
		validated, err := validateName(name)
		if err != nil {
			return err
		}
		body["name"] = validated
	}
	status, err := f.SelectOrFlag("", "active", "Status", []string{"keep", "enable", "disable"})
	if err != nil {
		return err
	}
	if status != "keep" {
		body["active"] = boolToInt(status == "enable")
	}
	return nil
}

func runUpdate(ctx context.Context, f *cmdutil.Factory, xid string, body map[string]any) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation("update API key "+xid, false); err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Put(ctx, "/apikey/"+xidSeg, api.WriteOptions{Body: body})
	if err != nil {
		return err
	}
	k, err := api.DataInto[APIKey](env)
	if err != nil {
		return err
	}
	// The response echoes the key; an update must not print it.
	return f.RenderOutput(k, func() error {
		f.ErrPrintf("Updated API key %s.\n", k.ID)
		return renderDetail(f, k)
	})
}

func newDeleteCmd(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "delete <api-key-xid>",
		Aliases: []string{"rm", "del"},
		Short:   "Delete an API key (sandbox only)",
		Long: `Delete an API key.

<api-key-xid> is the ID column of "sepay apikey list". Prompted for on a terminal; required otherwise.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			xid, err := pickOne(cmd.Context(), f, cmdutil.FirstArg(args))
			if err != nil {
				return err
			}
			return runDelete(cmd.Context(), f, xid)
		},
	}
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnEnvs:        "sandbox",
		cmdutil.AnnDestructive: "irreversible",
		cmdutil.AnnConfirms:    "always",
	})
}

func runDelete(ctx context.Context, f *cmdutil.Factory, xid string) error {
	xidSeg, err := cmdutil.PathSegment("xid", xid)
	if err != nil {
		return err
	}
	if err := f.ConfirmMutation(fmt.Sprintf("delete API key %s (anything authenticating with it stops working at once)", xid), true); err != nil {
		return err
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	if _, err := client.Delete(ctx, "/apikey/"+xidSeg, api.WriteOptions{}); err != nil {
		return err
	}
	res := struct {
		ID      string `json:"id" yaml:"id"`
		Deleted bool   `json:"deleted" yaml:"deleted"`
	}{xid, true}
	return f.RenderOutput(res, func() error {
		f.ErrPrintf("Deleted API key %s.\n", xid)
		return nil
	})
}
