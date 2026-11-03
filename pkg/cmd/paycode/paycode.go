// Package paycode implements "sepay paycode" (get, set, create).
package paycode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// Server structure rules: prefix 2-5 letters, suffix bounds within 1-30.
var prefixPattern = regexp.MustCompile(`^[a-zA-Z]{2,5}$`)

const (
	suffixMin = 1
	suffixMax = 30
)

// Paycode is the config shape shared by GET and PUT /v2/company/paycode.
type Paycode struct {
	PayCode    string      `json:"PayCode" yaml:"PayCode"`
	Structures []Structure `json:"PayCodeStructures" yaml:"PayCodeStructures"`
	// Optimistic-lock token; set sends it back so a stale write gets 409.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// Set by PUT only: Haravan/Shopify/Sapo connections still carrying a prefix the list no longer has.
	EcomRowsNotSynced int `json:"ecom_rows_not_synced,omitempty" yaml:"ecom_rows_not_synced,omitempty"`
}

// created is the POST /v2/company/paycode answer: the new structure and the list it ends.
type created struct {
	Structure Structure `json:"structure" yaml:"structure"`
	Paycode   `yaml:",inline"`
}

// Structure is one pay-code prefix rule.
type Structure struct {
	IsActive      string `json:"is_active" yaml:"is_active"`
	Prefix        string `json:"prefix" yaml:"prefix"`
	SuffixFrom    int    `json:"suffix_from" yaml:"suffix_from"`
	SuffixTo      int    `json:"suffix_to" yaml:"suffix_to"`
	CharacterType string `json:"character_type" yaml:"character_type"`
}

// NewCmdPaycode builds the "sepay paycode" command group.
func NewCmdPaycode(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "paycode <command>",
		Aliases: []string{"pc"},
		Short:   "View, set or add payment code prefixes",
	}
	cmd.AddCommand(newGetCmd(f), newSetCmd(f), newCreateCmd(f))
	return cmd
}

func newGetCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show the current payment code configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGet(cmd.Context(), f)
		},
	}
}

func newSetCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		suffixFrom, suffixTo int
		characterType        string
		active               bool
		enable, disable      bool
		prefixFlag           string
		structure            string
	)
	cmd := &cobra.Command{
		Use:   "set [prefix]",
		Short: "Update a pay-code structure or toggle pay-code detection",
		Long: `Update one pay-code structure, keeping the rest of the list. Flags refine that
structure; --enable/--disable toggle pay-code detection for the company.

--structure names the structure to edit by its current prefix. Without it, a
terminal with more than one structure asks which one; a non-interactive run edits
the first active structure.

A flag you leave out keeps whatever is stored — this reads the current structure
and writes it back changed, so setting only the prefix does not reset the suffix
range or the character type, and flags alone keep the stored prefix. On a company
with no structure yet there is nothing to keep: the prefix is required, and one is
seeded with suffix 3-10 and NumberOnly.

--prefix is the same value as the argument. It exists for a prefix that reads as
boolean flag syntax — "on", "off", "yes", "no", "true", "false" are all valid
2-5 letter prefixes, and as a bare argument beside --enable they are refused as a
mistyped switch.`,
		Args: cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if enable && disable {
				return &cmdutil.FlagError{Err: errors.New("cannot use --enable and --disable together")}
			}
			if characterType != "" {
				if err := checkCharacterType(characterType); err != nil {
					return err
				}
			}
			if err := validateSuffixRange(cmd, suffixFrom, suffixTo); err != nil {
				return err
			}
			given, err := givenPrefix(cmd, args, prefixFlag)
			if err != nil {
				return err
			}
			if given != "" {
				if err := checkPrefix(given); err != nil {
					return err
				}
			}
			// Flags alone keep the stored prefix; only a bare `set` prompts, after the GET so it can default to the picked structure.
			promptPrefix := given == "" && !enable && !disable && !anyFlagChanged(cmd, "suffix-from", "suffix-to", "character-type", "active")
			if promptPrefix && !f.CanPrompt() {
				return &cmdutil.FlagError{Err: errors.New("a pay-code prefix is required")}
			}

			opts := setOptions{prefix: given, promptPrefix: promptPrefix, structure: structure, characterType: characterType}
			if cmd.Flags().Changed("active") {
				opts.active = onOff(active)
			}
			if cmd.Flags().Changed("suffix-from") {
				opts.suffixFrom = &suffixFrom
			}
			if cmd.Flags().Changed("suffix-to") {
				opts.suffixTo = &suffixTo
			}
			if enable {
				opts.payCode = onOff(true)
			}
			if disable {
				opts.payCode = onOff(false)
			}
			return runSet(cmd.Context(), f, opts)
		},
	}
	fl := cmd.Flags()
	// Zero default so an unset bound is not sent and keeps the stored value.
	fl.IntVar(&suffixFrom, "suffix-from", 0, fmt.Sprintf("Suffix range start (%d-%d, unset keeps the stored bound)", suffixMin, suffixMax))
	fl.IntVar(&suffixTo, "suffix-to", 0, fmt.Sprintf("Suffix range end (%d-%d, unset keeps the stored bound)", suffixMin, suffixMax))
	fl.StringVar(&characterType, "character-type", "", "NumberOnly|NumberAndLetter")
	fl.BoolVar(&active, "active", true, "Turn this structure on (--active) or off (--active=false)")
	fl.BoolVar(&enable, "enable", false, "Turn pay-code detection on")
	fl.BoolVar(&disable, "disable", false, "Turn pay-code detection off")
	fl.StringVar(&structure, "structure", "", "Prefix of the structure to edit (default: ask on a terminal, else the first active one)")
	fl.StringVar(&prefixFlag, "prefix", "", "Pay-code prefix (2-5 letters), same as the argument; use it when the prefix reads as switch syntax (on|off|yes|no|true|false)")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnValueFlag: "prefix",
		cmdutil.AnnScope:     "paycode:write",
		cmdutil.AnnGuidance:  "On production this changes reconciliation matching immediately; transfers in flight with the old prefix stop matching.",
	})
}

// givenPrefix is the prefix from the argument or --prefix, refusing both; "" when neither was given.
func givenPrefix(cmd *cobra.Command, args []string, prefixFlag string) (string, error) {
	prefix := cmdutil.FirstArg(args)
	if !cmd.Flags().Changed("prefix") {
		return prefix, nil
	}
	if prefix != "" {
		return "", &cmdutil.FlagError{Err: errors.New("give the pay-code prefix once: as the argument or as --prefix, not both")}
	}
	return prefixFlag, nil
}

func checkPrefix(prefix string) error {
	if !prefixPattern.MatchString(prefix) {
		return &cmdutil.FlagError{Err: fmt.Errorf("prefix must be 2-5 letters, got %q", prefix)}
	}
	return nil
}

func checkCharacterType(characterType string) error {
	if characterType != "NumberOnly" && characterType != "NumberAndLetter" {
		return &cmdutil.FlagError{Err: errors.New("--character-type must be NumberOnly or NumberAndLetter")}
	}
	return nil
}

func anyFlagChanged(cmd *cobra.Command, names ...string) bool {
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}

	return false
}

// validateSuffixRange checks from <= to only when both are set; otherwise runSet's merge decides.
func validateSuffixRange(cmd *cobra.Command, from, to int) error {
	fromSet, toSet := cmd.Flags().Changed("suffix-from"), cmd.Flags().Changed("suffix-to")
	for _, b := range []struct {
		flag  string
		set   bool
		value int
	}{
		{"suffix-from", fromSet, from},
		{"suffix-to", toSet, to},
	} {
		if b.set && (b.value < suffixMin || b.value > suffixMax) {
			return &cmdutil.FlagError{Err: fmt.Errorf("--%s must be between %d and %d", b.flag, suffixMin, suffixMax)}
		}
	}
	if fromSet && toSet && from > to {
		return &cmdutil.FlagError{Err: errors.New("--suffix-from must be less than or equal to --suffix-to")}
	}
	return nil
}

// setOptions holds set inputs; nil pointers leave the stored value unchanged.
type setOptions struct {
	prefix        string
	promptPrefix  bool
	structure     string
	suffixFrom    *int
	suffixTo      *int
	characterType string
	active        *string
	payCode       *string
}

// onOff renders a bool as the API's "on"/"off" string.
func onOff(v bool) *string {
	s := "off"
	if v {
		s = "on"
	}

	return &s
}

func runGet(ctx context.Context, f *cmdutil.Factory) error {

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	env, err := client.Get(ctx, "/company/paycode", nil)
	if err != nil {
		return err
	}
	pc, err := api.DataInto[Paycode](env)
	if err != nil {
		return err
	}

	return f.RenderOutput(pc, func() error {
		return renderPaycode(f, pc)
	})
}

func runSet(ctx context.Context, f *cmdutil.Factory, opts setOptions) error {

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}

	env, err := client.Get(ctx, "/company/paycode", nil)
	if err != nil {
		return err
	}
	cur, err := api.DataInto[Paycode](env)
	if err != nil {
		return err
	}
	if opts.prefix == "" && !opts.promptPrefix && len(cur.Structures) == 0 {
		return &cmdutil.FlagError{Err: errors.New("a pay-code prefix is required: this company has no pay-code structure to keep")}
	}
	target, chosen, err := pickStructure(f, cur.Structures, opts)
	if err != nil {
		return err
	}
	if opts.promptPrefix {
		def := ""
		if target < len(cur.Structures) {
			def = jsonfmt.Clean(cur.Structures[target].Prefix)
		}
		if opts.prefix, err = f.InputWithDefault("Pay-code prefix (2-5 letters)", def); err != nil {
			return err
		}
		if err := checkPrefix(opts.prefix); err != nil {
			return err
		}
	}

	// Merge onto the GET result so the PUT carries its version and a concurrent replace gets 409.
	body := mergePaycode(cur, opts, target)

	// The server requires explicit on/off per structure; fail locally instead of a 422.
	for i, st := range body.Structures {
		if st.IsActive != "on" && st.IsActive != "off" {
			return fmt.Errorf("structure %d (%s) has no on/off state; run `sepay paycode get` and set it with --active", i+1, st.Prefix)
		}
	}
	if !chosen && opts.active == nil && len(body.Structures) > 0 &&
		activeIndex(body.Structures) == 0 && body.Structures[0].IsActive != "on" {
		f.ErrPrintf("Warning: no pay-code structure is active; editing the first one, which stays off.\n")
	}

	// Sent as If-Match, not a body field: an old instance mid-deploy rejects unknown body fields but ignores headers.
	version := body.Version
	body.Version = ""

	// No idempotency key: If-Match refuses a stale write, and the client does not resend a conditional
	// write whose outcome it never learned (a landed one would come back as 409).
	if err := f.ConfirmProdWrite("set pay-code"); err != nil {
		return err
	}
	write := api.WriteOptions{Body: body}
	if version != "" {
		write.Header = http.Header{"If-Match": []string{version}}
	}

	env, err = client.Put(ctx, "/company/paycode", write)
	if err != nil {
		return err
	}
	res, err := api.DataInto[Paycode](env)
	if err != nil {
		return err
	}
	if res.EcomRowsNotSynced > 0 {
		f.ErrPrintf("Warning: %d Haravan/Shopify/Sapo connection(s) still use a pay-code prefix this list no longer has; update them in my.sepay.vn.\n", res.EcomRowsNotSynced)
	}

	return f.RenderOutput(res, func() error {
		fmt.Fprintln(f.IOStreams.ErrOut, "Pay-code updated.")
		return renderPaycode(f, res)
	})
}

func newCreateCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		prefixFlag, characterType string
		suffixFrom, suffixTo      int
		active                    bool
	)
	cmd := &cobra.Command{
		Use:   "create [prefix]",
		Short: "Add a pay-code structure",
		Long: `Add one pay-code structure at the end of the list. The other structures and the
pay-code on/off switch stay as they are; to change those, use sepay paycode set.

Every field is required: the prefix (argument or --prefix), --suffix-from,
--suffix-to, --character-type, and --active or --active=false. On a terminal a
missing prefix is asked for. A prefix the company already has, in any letter case,
is refused.`,
		Args: cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range []string{"suffix-from", "suffix-to", "character-type", "active"} {
				if !cmd.Flags().Changed(name) {
					return &cmdutil.FlagError{Err: fmt.Errorf("--%s is required", name)}
				}
			}
			if err := checkCharacterType(characterType); err != nil {
				return err
			}
			if err := validateSuffixRange(cmd, suffixFrom, suffixTo); err != nil {
				return err
			}
			prefix, err := givenPrefix(cmd, args, prefixFlag)
			if err != nil {
				return err
			}
			if prefix == "" {
				if !f.CanPrompt() {
					return &cmdutil.FlagError{Err: errors.New("a pay-code prefix is required")}
				}
				if prefix, err = f.Input("Pay-code prefix (2-5 letters)"); err != nil {
					return err
				}
			}
			if err := checkPrefix(prefix); err != nil {
				return err
			}
			return runCreate(cmd.Context(), f, Structure{
				IsActive:      *onOff(active),
				Prefix:        prefix,
				SuffixFrom:    suffixFrom,
				SuffixTo:      suffixTo,
				CharacterType: characterType,
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&prefixFlag, "prefix", "", "Pay-code prefix (2-5 letters), same as the argument; use it when the prefix reads as switch syntax (on|off|yes|no|true|false)")
	fl.IntVar(&suffixFrom, "suffix-from", 0, fmt.Sprintf("Suffix range start (%d-%d)", suffixMin, suffixMax))
	fl.IntVar(&suffixTo, "suffix-to", 0, fmt.Sprintf("Suffix range end (%d-%d)", suffixMin, suffixMax))
	fl.StringVar(&characterType, "character-type", "", "NumberOnly|NumberAndLetter")
	fl.BoolVar(&active, "active", false, "Turn the structure on (--active) or off (--active=false)")
	cmdutil.MarkRequired(cmd, "suffix-from", "suffix-to", "character-type", "active")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnValueFlag: "prefix",
		cmdutil.AnnScope:     "paycode:write",
		cmdutil.AnnGuidance:  "On production an active structure starts matching transfers immediately.",
	})
}

func runCreate(ctx context.Context, f *cmdutil.Factory, st Structure) error {
	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	// One key per create: a retried request must not append the structure twice.
	key, err := f.IdempotencyKey()
	if err != nil {
		return err
	}
	if err := f.ConfirmProdWrite("create pay-code structure " + st.Prefix); err != nil {
		return err
	}
	env, err := client.Post(ctx, "/company/paycode", api.WriteOptions{Body: st, IdempotencyKey: key})
	if err != nil {
		return err
	}
	res, err := api.DataInto[created](env)
	if err != nil {
		return err
	}

	return f.RenderOutput(res, func() error {
		f.ErrPrintf("%s Created pay-code structure %s.\n", f.SuccessIcon(), res.Structure.Prefix)
		return renderPaycode(f, res.Paycode)
	})
}

// pickStructure returns the structure index to edit and whether the user chose it; defaults to the first active one.
func pickStructure(f *cmdutil.Factory, structures []Structure, opts setOptions) (int, bool, error) {
	if opts.structure != "" {
		if len(structures) == 0 {
			return 0, false, &cmdutil.FlagError{Err: fmt.Errorf("no pay-code structure with prefix %q: this company has none", opts.structure)}
		}
		// Exact match first, so "dh" and "DH" are both reachable.
		for _, match := range []func(a, b string) bool{func(a, b string) bool { return a == b }, strings.EqualFold} {
			for i, s := range structures {
				if match(s.Prefix, opts.structure) {
					return i, true, nil
				}
			}
		}
		prefixes := make([]string, len(structures))
		for i, s := range structures {
			prefixes[i] = s.Prefix
		}
		return 0, false, &cmdutil.FlagError{Err: fmt.Errorf("no pay-code structure with prefix %q (have: %s)", opts.structure, strings.Join(prefixes, ", "))}
	}
	editsStructure := opts.prefix != "" || opts.promptPrefix || opts.suffixFrom != nil ||
		opts.suffixTo != nil || opts.characterType != "" || opts.active != nil
	if !editsStructure || len(structures) < 2 || !f.CanPrompt() {
		return activeIndex(structures), false, nil
	}
	choices := make([]cmdutil.PromptChoice, len(structures))
	for i, s := range structures {
		choices[i] = cmdutil.PromptChoice{
			Label: fmt.Sprintf("%s · %d-%d · %s · %s", s.Prefix, s.SuffixFrom, s.SuffixTo, s.CharacterType, s.IsActive),
			Value: strconv.Itoa(i),
		}
	}
	picked, err := f.SelectChoiceOrFlag("", "structure", "Structure to edit", choices)
	if err != nil {
		return 0, false, err
	}
	i, err := strconv.Atoi(picked)
	return i, true, err
}

// mergePaycode applies opts onto structure i, keeping the others; seeds one from defaults when empty.
func mergePaycode(cur Paycode, opts setOptions, i int) Paycode {
	structures := cur.Structures
	if len(structures) == 0 {
		structures = []Structure{{IsActive: "on", Prefix: opts.prefix, SuffixFrom: 3, SuffixTo: 10, CharacterType: "NumberOnly"}}
		i = 0
	}

	if opts.prefix != "" {
		structures[i].Prefix = opts.prefix
	}
	if opts.suffixFrom != nil {
		structures[i].SuffixFrom = *opts.suffixFrom
	}
	if opts.suffixTo != nil {
		structures[i].SuffixTo = *opts.suffixTo
	}
	if opts.characterType != "" {
		structures[i].CharacterType = opts.characterType
	}
	if opts.active != nil {
		structures[i].IsActive = *opts.active
	}

	payCode := cur.PayCode
	if payCode == "" {
		payCode = "on"
	}
	if opts.payCode != nil {
		payCode = *opts.payCode
	}

	return Paycode{PayCode: payCode, Structures: structures, Version: cur.Version}
}

func activeIndex(structures []Structure) int {
	for i, s := range structures {
		if s.IsActive == "on" {
			return i
		}
	}
	return 0
}

func renderPaycode(f *cmdutil.Factory, pc Paycode) error {
	f.ErrPrintf("Pay-code: %s\n", pc.PayCode)
	structures := pc.Structures
	if len(structures) == 0 {
		fmt.Fprintln(f.IOStreams.ErrOut, "No pay-code structures.")
		return nil
	}
	t := jsonfmt.NewTable("PREFIX", "FROM", "TO", "TYPE", "ACTIVE")
	for _, s := range structures {
		t.AddRow(s.Prefix, strconv.Itoa(s.SuffixFrom), strconv.Itoa(s.SuffixTo), s.CharacterType, s.IsActive)
	}
	return f.RenderTable(t)
}
