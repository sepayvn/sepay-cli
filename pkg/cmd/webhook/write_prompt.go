package webhook

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// editableField is one editor choice; the answer is written onto flag so it follows the flag-driven path.
// A field with no flag is a compound section prompted by buildWriteBody.
type editableField struct {
	label string
	flag  string
	// false where an empty answer is a slip (url)
	blankClears bool
	current     func(Webhook) string
	// ask takes the stored value as rendered by current, the default for a choice.
	ask     func(f *cmdutil.Factory, label, current string) (string, error)
	section func(*writeOptions)
}

func editableFields() []editableField {
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	askBool := func(f *cmdutil.Factory, label, current string) (string, error) {
		def := "false"
		if current == "yes" {
			def = "true"
		}
		return f.SelectChoiceDefault(label, []cmdutil.PromptChoice{
			{Label: "yes", Value: "true"}, {Label: "no", Value: "false"},
		}, def)
	}
	// The server stores "In_only" and "Json" for the flag values "in" and "json".
	askEnum := func(options []string) func(*cmdutil.Factory, string, string) (string, error) {
		return func(f *cmdutil.Factory, label, current string) (string, error) {
			return f.SelectDefault(label, options, strings.TrimSuffix(strings.ToLower(current), "_only"))
		}
	}
	askText := func(f *cmdutil.Factory, label, _ string) (string, error) { return f.Input(label) }
	return []editableField{
		{
			label: "Name", flag: "name", blankClears: true,
			current: func(w Webhook) string { return w.Name },
			ask:     askText,
		},
		{
			label: "Destination URL", flag: "url",
			current: func(w Webhook) string { return w.urlLabel() },
			ask:     askText,
		},
		{
			label: "Events", flag: "event-type",
			current: func(w Webhook) string { return w.EventType },
			ask:     askEnum([]string{"all", "in", "out"}),
		},
		{
			label: "Content type", flag: "content-type",
			current: func(w Webhook) string { return w.RequestContentType },
			ask:     askEnum([]string{"json", "form", "multipart"}),
		},
		{
			label: "Only verified payments", flag: "verify-payment",
			current: func(w Webhook) string { return yesNo(w.IsVerifyPayment) },
			ask:     askBool,
		},
		{
			label: "Skip events with no pay code", flag: "skip-if-no-code",
			current: func(w Webhook) string { return yesNo(w.SkipIfNoCode) },
			ask:     askBool,
		},
		{
			label: "Pay code prefixes", flag: "prefix-filter", blankClears: true,
			current: func(w Webhook) string { return strings.Join(w.PrefixFilters, ",") },
			ask:     askText,
		},
		{
			label: "Authentication", current: func(w Webhook) string { return w.AuthenType },
			section: func(o *writeOptions) { o.auth = true },
		},
		{
			label: "Targeting", current: targetingSummary,
			section: func(o *writeOptions) { o.targeting = true },
		},
	}
}

func targetingSummary(w Webhook) string {
	switch w.BankMode {
	case "single":
		if w.BankAccountXID != nil {
			return "single · " + *w.BankAccountXID
		}
		return "single"
	case "multi":
		return fmt.Sprintf("multi · %d accounts", len(w.BankAccountXIDs))
	}
	return w.BankMode
}

// anyWriteFlagChanged reports whether any local field flag was passed.
// LocalFlags is a rebuilt set whose NFlag is always 0, so Changed is checked per flag.
func anyWriteFlagChanged(cmd *cobra.Command) bool {
	changed := false
	cmd.LocalFlags().VisitAll(func(fl *pflag.Flag) {
		if fl.Changed && fl.Name != "help" && fl.Name != "dry-run" {
			changed = true
		}
	})
	return changed
}

// promptFieldsToEdit runs the interactive editor, writing each answer back onto its flag.
func promptFieldsToEdit(ctx context.Context, f *cmdutil.Factory, cmd *cobra.Command, xid string) (writeOptions, error) {
	var opts writeOptions
	if !f.CanPrompt() {
		return opts, &cmdutil.FlagError{Err: fmt.Errorf("provide at least one field to update")}
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return opts, err
	}
	current, err := fetchWebhook(ctx, client, xid)
	if err != nil {
		return opts, err
	}

	fields := editableFields()
	choices := make([]cmdutil.PromptChoice, len(fields))
	for i, fd := range fields {
		label := fd.label
		if cur := fd.current(current); cur != "" {
			label += " (" + cur + ")"
		}
		choices[i] = cmdutil.PromptChoice{Label: label, Value: strconv.Itoa(i)}
	}
	picked, err := f.MultiSelectChoicesOrFlag(nil, "", "What do you want to edit?", choices)
	if err != nil {
		return opts, err
	}

	// Update never sets --listen-only; the stored transport decides the offerable auth schemes.
	opts.listenOnly = current.listenOnly()
	opts.current = &current

	for _, p := range picked {
		fd := fields[mustAtoi(p)]
		if fd.section != nil {
			fd.section(&opts)
			continue
		}
		v, err := fd.ask(f, fd.promptLabel(), fd.current(current))
		if err != nil {
			return opts, err
		}
		if v == "" && !fd.blankClears {
			continue
		}
		if fd.flag == "prefix-filter" {
			if v, err = normalizePrefixes(v); err != nil {
				return opts, err
			}
		}
		// Set marks the flag Changed even when empty, which is how a blankClears field is emptied.
		if err := cmd.Flags().Set(fd.flag, v); err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func (fd editableField) promptLabel() string {
	if fd.blankClears {
		return fd.label + " (blank to clear)"
	}
	return fd.label
}

// normalizePrefixes turns "A, B" into a CSV pflag parses cleanly; quotes are refused because
// pflag would report them against a flag the user never typed.
func normalizePrefixes(answer string) (string, error) {
	var out []string
	for _, p := range strings.Split(answer, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, `",`) {
			return "", &cmdutil.FlagError{Err: fmt.Errorf("pay code prefix %q cannot contain a quote or comma", p)}
		}
		out = append(out, p)
	}
	return strings.Join(out, ","), nil
}

func mustAtoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}

// authChoices withholds OAuth2 from listen-only webhooks: there is no destination to fetch a token from.
func authChoices(listenOnly bool) []cmdutil.PromptChoice {
	choices := []cmdutil.PromptChoice{
		{Label: "None", Value: "none"},
		{Label: "API key", Value: "apikey"},
		{Label: "HMAC signature", Value: "hmac"},
	}
	if !listenOnly {
		choices = append(choices, cmdutil.PromptChoice{Label: "OAuth2", Value: "oauth2"})
	}
	return choices
}

// authFieldsUnused lists the body keys each scheme ignores; the API would drop them silently.
var authFieldsUnused = map[string][]string{
	"none":   {"secret", "oauth2_client_id", "oauth2_client_secret", "oauth2_access_token_url"},
	"apikey": {"oauth2_client_id", "oauth2_client_secret", "oauth2_access_token_url"},
	"hmac":   {"oauth2_client_id", "oauth2_client_secret", "oauth2_access_token_url"},
	"oauth2": {"secret"},
}

// clearUnusedAuthFields drops keys the chosen scheme cannot use; keys it uses are kept.
func clearUnusedAuthFields(body map[string]any, authType string) {
	for _, k := range authFieldsUnused[authType] {
		delete(body, k)
	}
}

// promptAuth asks for the auth scheme and reads its secrets without echo.
func promptAuth(f *cmdutil.Factory, body map[string]any, listenOnly bool) error {
	authType, err := f.SelectChoiceOrFlag("", "auth-type", "Authentication", authChoices(listenOnly))
	if err != nil {
		return err
	}
	body["auth_type"] = authType
	clearUnusedAuthFields(body, authType)

	switch authType {
	case "apikey", "hmac":
		secret, err := f.Password("Shared secret")
		if err != nil {
			return err
		}
		if secret != "" {
			body["secret"] = secret
		}
	case "oauth2":
		clientID, err := f.InputOrFlag("", "oauth2-client-id", "OAuth2 client id")
		if err != nil {
			return err
		}
		body["oauth2_client_id"] = clientID
		secret, err := f.Password("OAuth2 client secret")
		if err != nil {
			return err
		}
		if secret != "" {
			body["oauth2_client_secret"] = secret
		}
		tokenURL, err := f.InputOrFlag("", "oauth2-token-url", "OAuth2 access-token URL")
		if err != nil {
			return err
		}
		body["oauth2_access_token_url"] = tokenURL
	}
	return nil
}
