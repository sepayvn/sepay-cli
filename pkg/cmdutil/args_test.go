package cmdutil

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func switchCmd(t *testing.T, argv []string, bools ...string) (*cobra.Command, []string) {
	t.Helper()
	cmd := &cobra.Command{Use: "set"}
	for _, name := range bools {
		cmd.Flags().Bool(name, false, "")
	}
	require.NoError(t, cmd.Flags().Parse(argv))
	return cmd, cmd.Flags().Args()
}

func TestMaximumNArgs_NamesTheSwitch(t *testing.T) {
	cases := []struct {
		name  string
		argv  []string
		bools []string
		want  string
	}{
		{"active off", []string{"xid_1", "--active", "off"}, []string{"active"}, `--active is a switch, not a value: use --active or --active=false (got the extra argument "off")`},
		{"active true", []string{"xid_1", "--active", "true"}, []string{"active"}, `--active is a switch, not a value: use --active or --active=false (got the extra argument "true")`},
		{"value typed before the identifier", []string{"--active", "off", "xid_1"}, []string{"active"}, `--active is a switch, not a value: use --active or --active=false (got the extra argument "off")`},
		{"two switches", []string{"ABC", "--enable", "--active", "off"}, []string{"active", "enable"}, `a switch takes no separate value: write --flag=false, not --flag false (got the extra argument "off")`},
		{"plain surplus", []string{"xid_1", "extra_junk"}, []string{"active"}, "accepts at most 1 arg(s), received 2"},
		{"switch word but no switch set", []string{"xid_1", "off"}, []string{"active"}, "accepts at most 1 arg(s), received 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, args := switchCmd(t, tc.argv, tc.bools...)
			err := MaximumNArgs(1)(cmd, args)
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assert.Equal(t, ExitUsage, ExitCodeForError(err))
		})
	}
}

func TestMaximumNArgs_AllowsWhatCobraAllows(t *testing.T) {
	cmd, args := switchCmd(t, []string{"xid_1", "--active=false"}, "active")
	require.NoError(t, MaximumNArgs(1)(cmd, args))
}

func TestNoArgs(t *testing.T) {
	cmd, args := switchCmd(t, []string{"--active", "off"}, "active")
	err := NoArgs(cmd, args)
	require.Error(t, err)
	assert.Equal(t, `--active is a switch, not a value: use --active or --active=false (got the extra argument "off")`, err.Error())

	cmd, args = switchCmd(t, []string{"--active=false"}, "active")
	require.NoError(t, NoArgs(cmd, args))

	cmd, args = switchCmd(t, []string{"stray"}, "active")
	assert.EqualError(t, NoArgs(cmd, args), `unknown command "stray" for "set"`)
}

func rootedCmd(t *testing.T, argv []string, own ...string) (*cobra.Command, []string) {
	t.Helper()
	root := &cobra.Command{Use: "sepay"}
	root.PersistentFlags().BoolP("yes", "y", false, "")
	root.PersistentFlags().BoolP("quiet", "q", false, "")
	sub := &cobra.Command{Use: "set"}
	for _, name := range own {
		sub.Flags().Bool(name, false, "")
	}
	root.AddCommand(sub)
	require.NoError(t, sub.ParseFlags(argv))
	return sub, sub.Flags().Args()
}

func TestMaximumNArgs_WordFillingTheSlot(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		own  []string
		want string
	}{
		{"switch word taken as the positional", []string{"--enable", "on"}, []string{"enable"}, `--enable is a switch, not a value: use --enable or --enable=false (got the extra argument "on")`},
		{"explicit true form still mistypes the same way", []string{"--active", "off"}, []string{"active"}, `--active is a switch, not a value: use --active or --active=false (got the extra argument "off")`},
		{"two own switches name neither", []string{"--enable", "--active", "off"}, []string{"enable", "active"}, `a switch takes no separate value: write --flag=false, not --flag false (got the extra argument "off")`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, args := rootedCmd(t, tc.argv, tc.own...)
			require.Len(t, args, 1, "the word must fill the slot, not overflow it")
			err := MaximumNArgs(1)(cmd, args)
			require.Error(t, err)
			assert.Equal(t, tc.want, err.Error())
			assert.Equal(t, ExitUsage, ExitCodeForError(err))
		})
	}
}

func TestMaximumNArgs_GlobalSwitchIsNotEvidence(t *testing.T) {
	for _, argv := range [][]string{
		{"off", "--yes"},
		{"off", "--yes=true"},
		{"--yes", "off"},
		{"no", "-q"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			cmd, args := rootedCmd(t, argv, "enable")
			assert.NoError(t, MaximumNArgs(1)(cmd, args))
		})
	}
}

func TestMaximumNArgs_OrdinaryUseIsUntouched(t *testing.T) {
	for _, argv := range [][]string{
		{"ERP", "--enable", "--yes"},
		{"--enable"},
		{"--enable", "--yes"},
		{"ERP"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			cmd, args := rootedCmd(t, argv, "enable")
			assert.NoError(t, MaximumNArgs(1)(cmd, args))
		})
	}
}

func TestMaximumNArgs_SurplusReadsEverySwitch(t *testing.T) {
	cmd, args := rootedCmd(t, []string{"ERP", "off", "--yes"}, "enable")
	require.Len(t, args, 2)
	err := MaximumNArgs(1)(cmd, args)
	require.Error(t, err)
	assert.Equal(t, `--yes is a switch, not a value: use --yes or --yes=false (got the extra argument "off")`, err.Error())
}

func TestMaximumNArgs_NamesTheEscapeFlagWhenThereIsOne(t *testing.T) {
	cmd, args := rootedCmd(t, []string{"--enable", "on"}, "enable")
	Annotate(cmd, map[string]string{AnnValueFlag: "prefix"})

	err := MaximumNArgs(1)(cmd, args)

	require.Error(t, err)
	assert.Equal(t,
		`--enable is a switch, not a value: use --enable or --enable=false (got the extra argument "on"); if "on" is the value you mean, pass --prefix on`,
		err.Error())
}

func TestMaximumNArgs_SaysNothingAboutAnEscapeThatDoesNotExist(t *testing.T) {
	cmd, args := rootedCmd(t, []string{"--enable", "on"}, "enable")

	err := MaximumNArgs(1)(cmd, args)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--prefix")
	assert.NotContains(t, err.Error(), "the value you mean")
}

func TestNoArgs_ReadsEverySwitch(t *testing.T) {
	cmd, args := rootedCmd(t, []string{"off", "--yes"}, "enable")
	err := NoArgs(cmd, args)
	require.Error(t, err)
	assert.Equal(t, `--yes is a switch, not a value: use --yes or --yes=false (got the extra argument "off")`, err.Error())
}
