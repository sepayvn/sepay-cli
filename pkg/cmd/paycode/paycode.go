// Package paycode implements "sepay paycode" (list, update, create).
package paycode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
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
	// Optimistic-lock token; update sends it back so a stale write gets 409.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// Set by PUT only: Haravan/Shopify/Sapo connections still carrying a prefix the list no longer has.
	EcomRowsNotSynced int `json:"ecom_rows_not_synced,omitempty" yaml:"ecom_rows_not_synced,omitempty"`
}

// created is the POST /v2/company/paycode answer: the new structure and the list it ends.
type created struct {
	Structure Structure `json:"structure" yaml:"structure"`
	Paycode   `yaml:",inline"`
}

// Structure is one pay code prefix rule.
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
		Short:   "Pay code prefixes that match transfers to orders",
		Long: `A pay code is a short code the payer types into the transfer memo, such as DH1234,
so SePay can match the transfer to your order. You configure its shape, not the
codes: a prefix of 2 to 5 letters plus a suffix of 1 to 30 characters.

"paycode list" shows the structures and whether detection is on; "paycode create"
adds one and "paycode update" changes one or turns detection on or off. On production a changed
prefix changes matching immediately.`,
	}
	cmd.AddCommand(newListCmd(f), newUpdateCmd(f), newCreateCmd(f))
	return cmd
}

func newListCmd(f *cmdutil.Factory) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List pay code structures and whether detection is on",
		Example: `  sepay paycode list
  sepay paycode list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd.Context(), f)
		},
	}
}

func newUpdateCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		suffixFrom, suffixTo int
		characterType        string
		active               bool
		enable, disable      bool
		prefixFlag           string
		structure            string
	)
	cmd := &cobra.Command{
		Use:     "update [prefix]",
		Aliases: []string{"edit"},
		Short:   "Update a pay code structure or toggle pay code detection",
		Long: `Update one pay code structure, keeping the rest of the list. Flags refine that
structure; --enable/--disable toggle pay code detection for the company.

--structure names the structure to edit by its current prefix. Without it, a
terminal with more than one structure asks which one; a non-interactive run edits
the first active structure. A bare "sepay paycode update" on a terminal then asks
which fields to change, offering the stored values.

A flag you leave out keeps whatever is stored — this reads the current structure
and writes it back changed, so setting only the prefix does not reset the suffix
range or the character type, and flags alone keep the stored prefix. On a company
with no structure yet there is nothing to keep: the prefix is required, and one is
seeded with suffix 3-10 and NumberOnly.

--prefix is the same value as the argument. It exists for a prefix that reads as
boolean flag syntax — "on", "off", "yes", "no", "true", "false" are all valid
2-5 letter prefixes, and as a bare argument beside --enable they are refused as a
mistyped switch.`,
		Example: `  sepay paycode update DH --suffix-to 10
  sepay paycode update --disable`,
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
			// Flags alone keep the stored prefix; only a bare `update` asks, after the GET so each question
			// can default to the picked structure.
			interactive := given == "" && !enable && !disable && !anyFlagChanged(cmd, "suffix-from", "suffix-to", "character-type", "active")
			if interactive && !f.CanPrompt() {
				return &cmdutil.FlagError{Err: errors.New("a pay code prefix is required")}
			}

			opts := updateOptions{prefix: given, interactive: interactive, structure: structure, characterType: characterType}
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
			return runUpdate(cmd.Context(), f, opts)
		},
	}
	fl := cmd.Flags()
	// Zero default so an unset bound is not sent and keeps the stored value.
	fl.IntVar(&suffixFrom, "suffix-from", 0, fmt.Sprintf("Shortest suffix length (%d-%d, unset keeps the stored bound; equal to --suffix-to for a fixed length)", suffixMin, suffixMax))
	fl.IntVar(&suffixTo, "suffix-to", 0, fmt.Sprintf("Longest suffix length (%d-%d, not below --suffix-from; unset keeps the stored bound)", suffixMin, suffixMax))
	fl.StringVar(&characterType, "character-type", "", "NumberOnly|NumberAndLetter")
	fl.BoolVar(&active, "active", true, "Turn this structure on (--active) or off (--active=false)")
	fl.BoolVar(&enable, "enable", false, "Turn pay code detection on")
	fl.BoolVar(&disable, "disable", false, "Turn pay code detection off")
	fl.StringVar(&structure, "structure", "", "Prefix of the structure to edit (default: ask on a terminal, else the first active one)")
	fl.StringVar(&prefixFlag, "prefix", "", "Pay code prefix (2-5 letters), same as the argument; use it when the prefix reads as switch syntax (on|off|yes|no|true|false)")
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
		return "", &cmdutil.FlagError{Err: errors.New("give the pay code prefix once: as the argument or as --prefix, not both")}
	}
	return prefixFlag, nil
}

func checkPrefix(prefix string) error {
	if !prefixPattern.MatchString(prefix) {
		return &cmdutil.FlagError{Err: fmt.Errorf("prefix must be 2-5 letters, got %q", prefix)}
	}
	return nil
}

var characterTypes = []string{"NumberOnly", "NumberAndLetter"}

const prefixLabel = "Pay code prefix (2-5 letters)"

func checkCharacterType(characterType string) error {
	if !slices.Contains(characterTypes, characterType) {
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

// validateSuffixRange checks from <= to only when both are set; otherwise runUpdate's merge decides.
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

// updateOptions holds update inputs; nil pointers leave the stored value unchanged.
type updateOptions struct {
	prefix string
	// interactive is a bare update on a terminal: it asks which fields to change.
	interactive   bool
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

func runList(ctx context.Context, f *cmdutil.Factory) error {
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

func runUpdate(ctx context.Context, f *cmdutil.Factory, opts updateOptions) error {
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
	if opts.prefix == "" && !opts.interactive && len(cur.Structures) == 0 {
		return &cmdutil.FlagError{Err: errors.New("a pay code prefix is required: this company has no pay code structure to keep")}
	}
	target, chosen, err := pickStructure(f, cur.Structures, opts)
	if err != nil {
		return err
	}
	if opts.interactive {
		if err := askUpdateFields(f, cur, target, &opts); err != nil {
			return err
		}
	}

	// Merge onto the GET result so the PUT carries its version and a concurrent replace gets 409.
	body := mergePaycode(cur, opts, target)
	if len(cur.Structures) == 0 {
		target = 0
	}
	if err := checkEdited(body.Structures, target); err != nil {
		return err
	}

	// The server requires explicit on/off per structure; fail locally instead of a 422.
	for i, st := range body.Structures {
		if st.IsActive != "on" && st.IsActive != "off" {
			return fmt.Errorf("structure %d (%s) has no on/off state; run `sepay paycode list` and set it with --active", i+1, st.Prefix)
		}
	}
	if !chosen && opts.active == nil && len(body.Structures) > 0 &&
		activeIndex(body.Structures) == 0 && body.Structures[0].IsActive != "on" {
		f.ErrPrintf("Warning: no pay code structure is active; editing the first one, which stays off.\n")
	}

	// Sent as If-Match, not a body field: an old instance mid-deploy rejects unknown body fields but ignores headers.
	version := body.Version
	body.Version = ""

	// No idempotency key: If-Match refuses a stale write, and the client does not resend a conditional
	// write whose outcome it never learned (a landed one would come back as 409).
	if err := f.ConfirmProdWrite("update pay code"); err != nil {
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
		f.ErrPrintf("Warning: %d Haravan/Shopify/Sapo connection(s) still use a pay code prefix this list no longer has; update them in my.sepay.vn.\n", res.EcomRowsNotSynced)
	}

	return f.RenderOutput(res, func() error {
		fmt.Fprintln(f.IOStreams.ErrOut, "Pay code updated.")
		return renderPaycode(f, res)
	})
}

// askUpdateFields asks which fields of structure target to change and then for each new value,
// offering the stored one. A company with no structure yet is asked only for the prefix: the rest
// is seeded.
func askUpdateFields(f *cmdutil.Factory, cur Paycode, target int, opts *updateOptions) error {
	if len(cur.Structures) == 0 {
		prefix, err := askPrefix(f, "")
		opts.prefix = prefix
		return err
	}
	st := cur.Structures[target]
	fields := []struct {
		label string
		ask   func() error
	}{
		{"Prefix (" + st.Prefix + ")", func() (err error) {
			opts.prefix, err = askPrefix(f, st.Prefix)
			return err
		}},
		{fmt.Sprintf("Suffix length (%d-%d)", st.SuffixFrom, st.SuffixTo), func() error {
			from, err := askSuffixBound(f, "suffix-from", st.SuffixFrom, suffixMin, suffixMax)
			if err != nil {
				return err
			}
			to, err := askSuffixBound(f, "suffix-to", max(st.SuffixTo, from), from, suffixMax)
			if err != nil {
				return err
			}
			opts.suffixFrom, opts.suffixTo = &from, &to
			return nil
		}},
		{"Character type (" + st.CharacterType + ")", func() (err error) {
			opts.characterType, err = f.SelectDefault("Character type", characterTypes, st.CharacterType)
			return err
		}},
		{"Active (" + st.IsActive + ")", func() error {
			on, err := f.AskYesNo("Active", st.IsActive == "on")
			opts.active = onOff(on)
			return err
		}},
		{"Pay code detection (" + cur.PayCode + ")", func() error {
			on, err := f.AskYesNo("Pay code detection", cur.PayCode != "off")
			opts.payCode = onOff(on)
			return err
		}},
	}
	choices := make([]cmdutil.PromptChoice, len(fields))
	for i, fd := range fields {
		choices[i] = cmdutil.PromptChoice{Label: fd.label, Value: strconv.Itoa(i)}
	}
	picked, err := f.MultiSelectChoicesOrFlag(nil, "", "What do you want to edit?", choices)
	if err != nil {
		return err
	}
	if len(picked) == 0 {
		return &cmdutil.FlagError{Err: errors.New("provide at least one field to update")}
	}
	for _, p := range picked {
		i, _ := strconv.Atoi(p)
		if err := fields[i].ask(); err != nil {
			return err
		}
	}
	return nil
}

func askPrefix(f *cmdutil.Factory, def string) (string, error) {
	prefix, err := f.InputWithDefault(prefixLabel, def)
	if err != nil {
		return "", err
	}
	return prefix, checkPrefix(prefix)
}

// askSuffixBound asks for the suffix bound flag names within lo..hi, offering def when it is set.
// The other bound narrows the range, so from never ends up above to.
func askSuffixBound(f *cmdutil.Factory, flag string, def, lo, hi int) (int, error) {
	shown := ""
	if def > 0 {
		shown = strconv.Itoa(def)
	}
	label := fmt.Sprintf("Suffix length %s (%d-%d)", strings.TrimPrefix(flag, "suffix-"), lo, hi)
	answer, err := f.InputWithDefault(label, shown)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil || n < lo || n > hi {
		return 0, &cmdutil.FlagError{Err: fmt.Errorf("--%s must be between %d and %d", flag, lo, hi)}
	}
	return n, nil
}

func newCreateCmd(f *cmdutil.Factory) *cobra.Command {
	var (
		prefixFlag, characterType string
		suffixFrom, suffixTo      int
		active                    bool
	)
	cmd := &cobra.Command{
		Use:   "create [prefix]",
		Short: "Add a pay code structure",
		Long: `Add one pay code structure at the end of the list. The other structures and the
pay code on/off switch stay as they are; to change those, use sepay paycode update.

Every field is required: the prefix (argument or --prefix), --suffix-from,
--suffix-to, --character-type, and --active or --active=false. On a terminal any
of them left out is asked for. A prefix the company already has, in any letter
case, is refused.`,
		Example: `  # codes like DH1234 to DH12345678
  sepay paycode create DH --character-type NumberOnly --suffix-from 4 --suffix-to 8 --active`,
		Args: cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if f.CanPrompt() {
				// A bad flag fails before any question is asked.
				if cmd.Flags().Changed("character-type") {
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
				if err := askCreateFields(f, cmd, args, prefixFlag); err != nil {
					return err
				}
			}
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
				return &cmdutil.FlagError{Err: errors.New("a pay code prefix is required")}
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
	fl.StringVar(&prefixFlag, "prefix", "", "Pay code prefix (2-5 letters), same as the argument; use it when the prefix reads as switch syntax (on|off|yes|no|true|false)")
	fl.IntVar(&suffixFrom, "suffix-from", 0, fmt.Sprintf("Shortest suffix length (%d-%d; equal to --suffix-to for a fixed length)", suffixMin, suffixMax))
	fl.IntVar(&suffixTo, "suffix-to", 0, fmt.Sprintf("Longest suffix length (%d-%d, not below --suffix-from)", suffixMin, suffixMax))
	fl.StringVar(&characterType, "character-type", "", "NumberOnly|NumberAndLetter")
	fl.BoolVar(&active, "active", false, "Turn the structure on (--active) or off (--active=false)")
	cmdutil.MarkRequired(cmd, "suffix-from", "suffix-to", "character-type", "active")
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnValueFlag: "prefix",
		cmdutil.AnnScope:     "paycode:write",
		cmdutil.AnnGuidance:  "On production an active structure starts matching transfers immediately.",
	})
}

// askCreateFields asks for each create field left off the command line, in the order a structure
// reads, and writes the answer onto its flag so the flag checks that follow cover it.
func askCreateFields(f *cmdutil.Factory, cmd *cobra.Command, args []string, prefixFlag string) error {
	given, err := givenPrefix(cmd, args, prefixFlag)
	if err != nil {
		return err
	}
	fl := cmd.Flags()
	if given == "" {
		prefix, err := askPrefix(f, "")
		if err != nil {
			return err
		}
		if err := fl.Set("prefix", prefix); err != nil {
			return err
		}
	}
	if !fl.Changed("character-type") {
		t, err := f.SelectOrFlag("", "character-type", "Character type", characterTypes)
		if err != nil {
			return err
		}
		if err := fl.Set("character-type", t); err != nil {
			return err
		}
	}
	from, _ := fl.GetInt("suffix-from")
	to, _ := fl.GetInt("suffix-to")
	if !fl.Changed("suffix-from") {
		hi := suffixMax
		if fl.Changed("suffix-to") {
			hi = to
		}
		if from, err = askSuffixBound(f, "suffix-from", 0, suffixMin, hi); err != nil {
			return err
		}
		if err := fl.Set("suffix-from", strconv.Itoa(from)); err != nil {
			return err
		}
	}
	if !fl.Changed("suffix-to") {
		if to, err = askSuffixBound(f, "suffix-to", 0, from, suffixMax); err != nil {
			return err
		}
		if err := fl.Set("suffix-to", strconv.Itoa(to)); err != nil {
			return err
		}
	}
	if !fl.Changed("active") {
		on, err := f.AskYesNo("Active", true)
		if err != nil {
			return err
		}
		if err := fl.Set("active", strconv.FormatBool(on)); err != nil {
			return err
		}
	}
	return nil
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
	if err := f.ConfirmProdWrite("create pay code structure " + st.Prefix); err != nil {
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
		f.ErrPrintf("%s Created pay code structure %s.\n", f.SuccessIcon(), res.Structure.Prefix)
		return renderPaycode(f, res.Paycode)
	})
}

// pickStructure returns the structure index to edit and whether the user chose it; defaults to the first active one.
func pickStructure(f *cmdutil.Factory, structures []Structure, opts updateOptions) (int, bool, error) {
	if opts.structure != "" {
		if len(structures) == 0 {
			return 0, false, &cmdutil.FlagError{Err: fmt.Errorf("no pay code structure with prefix %q: this company has none", opts.structure)}
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
		return 0, false, &cmdutil.FlagError{Err: fmt.Errorf("no pay code structure with prefix %q (have: %s)", opts.structure, strings.Join(prefixes, ", "))}
	}
	editsStructure := opts.prefix != "" || opts.interactive || opts.suffixFrom != nil ||
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
func mergePaycode(cur Paycode, opts updateOptions, i int) Paycode {
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

// checkEdited refuses what the merge can make of a single flag: one suffix bound crossing the
// stored other one, or a prefix another structure already has.
func checkEdited(structures []Structure, i int) error {
	st := structures[i]
	if st.SuffixFrom > st.SuffixTo {
		return &cmdutil.FlagError{Err: fmt.Errorf(
			"suffix length from %d is greater than to %d; pass --suffix-from and --suffix-to together", st.SuffixFrom, st.SuffixTo)}
	}
	for j, other := range structures {
		if j != i && strings.EqualFold(other.Prefix, st.Prefix) {
			return &cmdutil.FlagError{Err: fmt.Errorf("prefix %q is already used by another pay code structure", st.Prefix)}
		}
	}
	return nil
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
	f.ErrPrintf("Pay code: %s\n", pc.PayCode)
	structures := pc.Structures
	if len(structures) == 0 {
		fmt.Fprintln(f.IOStreams.ErrOut, "No pay code structures.")
		return nil
	}
	t := jsonfmt.NewTable("PREFIX", "FROM", "TO", "TYPE", "ACTIVE")
	for _, s := range structures {
		t.AddRow(s.Prefix, strconv.Itoa(s.SuffixFrom), strconv.Itoa(s.SuffixTo), s.CharacterType, s.IsActive)
	}
	return f.RenderTable(t)
}
