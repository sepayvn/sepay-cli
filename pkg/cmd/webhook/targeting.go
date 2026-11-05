package webhook

import (
	"context"
	"fmt"

	"github.com/sepayvn/sepay-cli/pkg/cmd/bankaccount"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
)

// resolveTargeting fills bank_mode and its accounts; update touches them only when a targeting flag is set.
func resolveTargeting(ctx context.Context, f *cmdutil.Factory, cmd *cobra.Command, body map[string]any, opts writeOptions) error {
	fl := cmd.Flags()
	// Without prompting, a partial flag set must stay a usage error.
	prompting := opts.forCreate || opts.targeting
	touched := fl.Changed("bank-mode") || fl.Changed("bank-account") || fl.Changed("va-xid") || fl.Changed("sub-account") || fl.Changed("only-va") || fl.Changed("bank-config")
	if !prompting && !touched {
		return nil
	}

	banks, _ := fl.GetStringSlice("bank-account")
	subs, _ := fl.GetStringSlice("sub-account")
	mode, _ := fl.GetString("bank-mode")
	if mode == "" {
		switch {
		case len(banks) > 1:
			mode = "multi"
		case len(banks) == 1:
			mode = "single"
		}
	}
	interactive := prompting && f.CanPrompt()
	var stored *Webhook
	if interactive {
		stored = opts.current
	}
	if prompting && mode == "" {
		var err error
		if interactive {
			mode, err = f.SelectChoiceDefault("Targeting mode", bankModeChoices, storedMode(stored))
		} else {
			mode, err = f.SelectOrFlag("", "bank-mode", "Targeting mode", []string{"single", "multi", "all"})
		}
		if err != nil {
			return err
		}
	}
	if mode != "" {
		body["bank_mode"] = mode
	}

	// Reject extras for the mode rather than drop them and widen the webhook's scope.
	switch mode {
	case "single":
		if len(banks) > 1 {
			return &cmdutil.FlagError{Err: fmt.Errorf("--bank-mode single takes a single --bank-account; use --bank-mode multi for several")}
		}
		xid := ""
		if len(banks) > 0 {
			xid = banks[0]
		}
		if interactive && xid == "" {
			var def []string
			if stored != nil && stored.BankAccountXID != nil {
				def = []string{*stored.BankAccountXID}
			}
			picked, err := pickAccounts(ctx, f, false, def)
			if err != nil {
				return err
			}
			xid = picked[0]
		}
		if prompting && xid == "" {
			var err error
			if xid, err = bankaccount.PickOne(ctx, f, "", "bank-account", "Bank account"); err != nil {
				return err
			}
		}
		if xid != "" {
			body["bank_account_xid"] = xid
		}
	case "multi":
		if interactive && len(banks) == 0 {
			var def []string
			if stored != nil {
				def = stored.BankAccountXIDs
			}
			picked, err := pickAccounts(ctx, f, true, def)
			if err != nil {
				return err
			}
			banks = picked
		}
		if len(banks) == 0 {
			return &cmdutil.FlagError{Err: fmt.Errorf("--bank-mode multi needs at least one --bank-account")}
		}
		body["bank_account_xids"] = banks
	case "all":
		// The server discards per-account narrowing in all mode, only_va included.
		if len(banks) > 0 || len(subs) > 0 || fl.Changed("bank-config") || fl.Changed("only-va") {
			return &cmdutil.FlagError{Err: fmt.Errorf("--bank-mode all targets every account; remove --bank-account/--va-xid/--bank-config/--only-va")}
		}
	}

	if len(subs) > 0 {
		body["bank_sub_account_xids"] = subs
	}
	if fl.Changed("only-va") {
		v, _ := fl.GetBool("only-va")
		// In multi mode the server rewrites only_va to 0 when the VA list is empty, so the flag alone is a no-op.
		// Single mode reads it as receive_primary=0 + va_mode=all.
		if v && len(subs) == 0 && mode == "multi" {
			return &cmdutil.FlagError{Err: fmt.Errorf(
				"--bank-mode multi ignores --only-va without --va-xid; use --bank-config <xid>=off:all to skip the account number itself")}
		}
		body["only_va"] = v
	}
	var cfg map[string]any
	if fl.Changed("bank-config") {
		raw, _ := fl.GetStringSlice("bank-config")
		var err error
		if cfg, err = parseBankConfig(raw); err != nil {
			return err
		}
		for _, xid := range sortedKeys(cfg) {
			if entryOf(cfg[xid]).VaMode == vaModeList && opts.forCreate && len(subs) == 0 {
				return &cmdutil.FlagError{Err: fmt.Errorf("--bank-config %s uses list: name its virtual accounts with --va-xid", xid)}
			}
		}
		body["bank_config"] = cfg
	}
	carry := (mode == "" && (cfg != nil || len(subs) > 0)) || (mode != "" && mode != "all" && len(subs) > 0)
	if opts.xid != "" && !interactive && carry {
		if err := carryStoredTargeting(ctx, f, body, opts.xid, mode == ""); err != nil {
			return err
		}
	}
	if cfg != nil {
		// An update without --va-xid keeps the stored VA list, which the CLI has not read.
		_, subsKnown := body["bank_sub_account_xids"]
		return checkBankConfig(ctx, f, body, opts.forCreate || subsKnown)
	}

	// Ask what each targeted account notifies unless a flag already said so.
	if interactive && (mode == "single" || mode == "multi") && !fl.Changed("only-va") {
		return askTargetedConfig(ctx, f, body, bodyAccounts(body), stored, subs)
	}
	return nil
}

var bankModeChoices = []cmdutil.PromptChoice{
	{Label: "single", Value: "single"}, {Label: "multi", Value: "multi"}, {Label: "all", Value: "all"},
}

func storedMode(w *Webhook) string {
	if w == nil {
		return ""
	}
	return w.BankMode
}

// pickAccounts asks for one or several bank accounts, starting on the ones in def.
func pickAccounts(ctx context.Context, f *cmdutil.Factory, many bool, def []string) ([]string, error) {
	client, err := f.NewAPIClient()
	if err != nil {
		return nil, err
	}
	accts, err := bankaccount.FetchAll(ctx, client)
	if err != nil {
		return nil, err
	}
	if len(accts) == 0 {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("no bank accounts found; create one first (sepay bank-account create)")}
	}
	choices := make([]cmdutil.PromptChoice, len(accts))
	for i, a := range accts {
		choices[i] = cmdutil.PromptChoice{Label: bankaccount.Label(a), Value: a.ID}
	}
	if !many {
		one := ""
		if len(def) > 0 {
			one = def[0]
		}
		xid, err := f.SelectChoiceDefault("Bank account", choices, one)
		return []string{xid}, err
	}
	picked, err := f.MultiSelectChoicesDefault("Bank accounts to target", choices, def)
	if err != nil {
		return nil, err
	}
	if len(picked) == 0 {
		return nil, &cmdutil.FlagError{Err: fmt.Errorf("--bank-mode multi needs at least one --bank-account")}
	}
	return picked, nil
}

// askTargetedConfig fills bank_config and bank_sub_account_xids from the per-account questions.
// The VA list is always sent, so switching an account off "list" clears its stale entries.
func askTargetedConfig(ctx context.Context, f *cmdutil.Factory, body map[string]any, xids []string, stored *Webhook, subsFlag []string) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	accounts := make([]bankaccount.BankAccount, 0, len(xids))
	for _, xid := range xids {
		a, err := bankaccount.Fetch(ctx, client, xid)
		if err != nil {
			return err
		}
		accounts = append(accounts, a)
	}
	cfg, subs, err := askBankConfig(ctx, f, client, accounts, stored, subsFlag)
	if err != nil {
		return err
	}
	if len(cfg) > 0 {
		body["bank_config"] = cfg
	}
	if subs == nil {
		subs = []string{}
	}
	body["bank_sub_account_xids"] = subs
	return nil
}
