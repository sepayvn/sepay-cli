package listen

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseControl(t *testing.T) {
	cases := []struct {
		in       string
		action   controlAction
		ordinals []int
	}{
		{"", actionNone, nil},
		{"   ", actionNone, nil},
		{"?", actionHelp, nil},
		{"help", actionHelp, nil}, // the word people reach for before "?"
		{"help me", actionUnknown, nil},
		{"retries", actionList, nil},
		{"  retries  ", actionList, nil},
		{"kill 1", actionCancel, []int{1}},
		{"kill 1 3", actionCancel, []int{1, 3}},
		{"kill 12  7", actionCancel, []int{12, 7}},
		{"kill all", actionCancelAll, nil},
		{"retries off", actionArmingOff, nil},
		{"retries on", actionArmingOn, nil},
		{"kill", actionCancelNoTarget, nil},
		{"kill 0", actionUnknown, nil},
		{"kill -1", actionUnknown, nil},
		{"kill abc", actionUnknown, nil},
		{"kill 1 abc", actionUnknown, nil},
		{"kill all off", actionUnknown, nil},
		{"retries all", actionUnknown, nil},
		{"? retries", actionUnknown, nil},
		{"quit", actionUnknown, nil},
		{"KILL 1", actionUnknown, nil},
		// `r 1` would read as "retry 1 now".
		{"r", actionUnknown, nil},
		{"r 1", actionUnknown, nil},
		// Unapplied erase byte and a bare arrow-key escape: keystrokes, not commands.
		{"\x7f", actionNone, nil},
		{"\x1b", actionNone, nil},
		{"\x08\x08", actionNone, nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			action, ordinals := parseControl(tc.in)
			assert.Equal(t, tc.action, action)
			assert.Equal(t, tc.ordinals, ordinals)
		})
	}
}

func TestParseControl_AcceptsAnyPositiveNumber(t *testing.T) {
	action, ordinals := parseControl("kill 999")
	assert.Equal(t, actionCancel, action)
	assert.Equal(t, []int{999}, ordinals)
}

func testController(t *testing.T) (*controller, *retryScheduler, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	ios, in, _, errOut := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	sched, _, _ := testScheduler(t, time.Hour)
	t.Cleanup(func() { sched.stop() })

	src := Source{Tag: func(io *iostreams.IOStreams, fr Frame) string { return Tag(io, "txn", fr.ID) }}
	c := newController(f, sched, nil, nil, src, DefaultForwardRetries)
	require.NotNil(t, c)
	return c, sched, in, errOut
}

func TestControl_UnknownInputIsNotFatal(t *testing.T) {
	c, sched, in, errOut := testController(t)
	_, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	in.WriteString("bogus\nkill 0\nkill all\n")
	c.run(context.Background())

	assert.Equal(t, 2, bytes.Count(errOut.Bytes(), []byte("? for help")))
	assert.Contains(t, errOut.String(), `unknown input "bogus"`)
	assert.Contains(t, errOut.String(), "killed 1 chain(s)",
		"a bad line must not stop the reader")
}

func TestHintFor(t *testing.T) {
	cases := map[string]string{
		"bogus":                 `unknown input "bogus" — ? for help`,
		"  kill all 1  ":        `unknown input "kill all 1" — ? for help`,
		"\x1b[Aretries":         `unknown input "[Aretries" — ? for help`,
		"":                      "unknown input — ? for help",
		strings.Repeat("x", 45): `unknown input "` + strings.Repeat("x", 40) + `"… — ? for help`,
		strings.Repeat("đ", 45): `unknown input "` + strings.Repeat("đ", 40) + `"… — ? for help`,
	}
	for in, want := range cases {
		assert.Equal(t, want, hintFor(in), "input %q", in)
	}
}

func TestControl_ListShowsEveryArmedChain(t *testing.T) {
	c, sched, in, errOut := testController(t)
	for _, id := range []string{"txn_a", "txn_b"} {
		_, _, ok := sched.arm(context.Background(), job{frame: Frame{ID: id, Type: "payment_in", Raw: []byte(`{}`)}}, 0)
		require.True(t, ok)
	}

	in.WriteString("retries\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, "#1")
	assert.Contains(t, out, "#2")
	assert.Contains(t, out, "1/7", "the attempt column names the budget it is spending")
	assert.Contains(t, out, "payment_in")
	assert.Contains(t, out, "txn=txn_a", "the row carries the same tag as its delivery line")
	assert.Contains(t, out, controlHelp)
}

func TestControl_ListSaysWhenThereIsNothingArmed(t *testing.T) {
	c, _, in, errOut := testController(t)
	in.WriteString("retries\n")
	c.run(context.Background())
	assert.Contains(t, errOut.String(), "no armed retry chains")
}

func TestControl_CancelReportsAnAttemptAlreadySent(t *testing.T) {
	c, sched, in, errOut := testController(t)
	queued, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)
	sched.markInFlight(queued) // the forwarder is now sending it

	in.WriteString("kill 1\n")
	c.run(context.Background())

	assert.Contains(t, errOut.String(), "killed #1")
	assert.Contains(t, errOut.String(), "already sent will still finish")
	assert.False(t, sched.has(queued.ordinal))
}

func TestControl_CancelIsSilentAboutAnUnsentAttempt(t *testing.T) {
	c, sched, in, errOut := testController(t)
	sched.unit = time.Millisecond
	queued, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		views := sched.list()
		return len(views) == 1 && views[0].sending
	}, time.Second, 5*time.Millisecond, "the attempt never left the timer")

	in.WriteString("kill 1\n")
	c.run(context.Background())

	assert.Contains(t, errOut.String(), "killed #1")
	assert.NotContains(t, errOut.String(), "already sent")
	assert.False(t, sched.has(queued.ordinal))
}

func TestControl_StrayControlByteIsSilent(t *testing.T) {
	c, _, in, errOut := testController(t)
	in.WriteString("\x7f\n")
	c.run(context.Background())
	assert.Empty(t, errOut.String())
}

func TestControl_OutputCarriesTheChannelPrefix(t *testing.T) {
	c, _, in, errOut := testController(t)
	in.WriteString("?\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, controlPrefix+controlHelp)
	assert.True(t, strings.HasPrefix(out, "\n"), "a block stands off the stream above it")
	assert.True(t, strings.HasSuffix(out, "\n\n"), "and off the stream below it")
}

// Without the shared screen lock this also fails under -race.
func TestControl_BlockIsNotSplitByConcurrentDeliveries(t *testing.T) {
	c, sched, in, errOut := testController(t)
	for range 3 {
		_, _, ok := sched.arm(context.Background(), job{frame: Frame{ID: "evt", Type: "payment_in", Raw: []byte(`{}`)}}, 0)
		require.True(t, ok)
	}

	const note = "retry 1/7 in ~1m"
	noteFactory := &cmdutil.Factory{IOStreams: c.f.IOStreams, Global: &cmdutil.GlobalOptions{}}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					printRetryNote(noteFactory, errors.New("unreachable"), note)
				}
			}
		}()
	}

	in.WriteString("retries\n")
	c.run(context.Background())
	close(stop)
	wg.Wait()

	var prefixed []int
	for i, line := range strings.Split(errOut.String(), "\n") {
		if strings.HasPrefix(line, controlPrefix) {
			assert.NotContains(t, line, note, "a delivery note landed inside a control line")
			prefixed = append(prefixed, i)
		}
	}
	require.Len(t, prefixed, 5, "header, three chains, help")
	for i := 1; i < len(prefixed); i++ {
		assert.Equal(t, prefixed[i-1]+1, prefixed[i], "the table was split by a concurrent delivery")
	}
}

func TestControl_CancelAllReportsAnAttemptAlreadySent(t *testing.T) {
	c, sched, in, errOut := testController(t)
	queued, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)
	_, _, ok = sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)
	sched.markInFlight(queued)

	in.WriteString("kill all\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, "killed 2 chain(s)")
	assert.Contains(t, out, "1 attempt(s) already sent will still finish")
}

func TestControl_CancelAllIsSilentWhenNothingWasSent(t *testing.T) {
	c, sched, in, errOut := testController(t)
	_, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	in.WriteString("kill all\n")
	c.run(context.Background())

	assert.Contains(t, errOut.String(), "killed 1 chain(s)")
	assert.NotContains(t, errOut.String(), "already sent")
}

// kill 1 9 for a typo'd kill 1 8 must not read as a full success.
func TestControl_CancelNamesTheNumbersThatMissed(t *testing.T) {
	c, sched, in, errOut := testController(t)
	first, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)
	second, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	in.WriteString("kill 1 9\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, "killed #1")
	assert.Contains(t, out, "#9 — no armed chain by that number")
	assert.False(t, sched.has(first.ordinal))
	assert.True(t, sched.has(second.ordinal), "the miss must not take a bystander with it")
}

func TestControl_CancelReportsAnUnknownNumber(t *testing.T) {
	c, _, in, errOut := testController(t)
	in.WriteString("kill 7\n")
	c.run(context.Background())
	assert.Contains(t, errOut.String(), "no armed retry chain by that number")
}

func TestControl_ArmingOffAndOn(t *testing.T) {
	c, sched, in, errOut := testController(t)

	in.WriteString("retries off\nretries\nretries on\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, "no longer retried")
	assert.Contains(t, out, "new failures are not being retried",
		"the listing states the session is no longer arming")
	assert.Contains(t, out, "retried again")
	assert.True(t, sched.arming())
}

func TestControl_ArmingOnWithNothingToResumeSaysSo(t *testing.T) {
	c, sched, in, errOut := testController(t)
	_, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	in.WriteString("kill all\nretries on\nretries off\nretries off\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, "a killed chain does not come back")
	assert.NotContains(t, out, "✓ new failures are retried again",
		"retries on must not claim a change it did not make")
	assert.Contains(t, out, "already not retried", "and neither must a second retries off")
}

func TestControl_WritesToStderrOnly(t *testing.T) {
	ios, in, out, errOut := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	sched, _, _ := testScheduler(t, time.Hour)
	defer sched.stop()

	c := newController(f, sched, nil, nil, Source{}, DefaultForwardRetries)
	require.NotNil(t, c)
	in.WriteString("r\n?\nbogus\n")
	c.run(context.Background())

	assert.Empty(t, out.String())
	assert.NotEmpty(t, errOut.String())
}

func TestNewController_DisabledWhereInputIsNotAvailable(t *testing.T) {
	cases := []struct {
		name    string
		global  cmdutil.GlobalOptions
		stdin   bool
		stdout  bool
		enabled bool
	}{
		{"a terminal", cmdutil.GlobalOptions{Output: "table"}, true, true, true},
		{"piped stdin", cmdutil.GlobalOptions{Output: "table"}, false, true, false},
		{"piped stdout", cmdutil.GlobalOptions{Output: "table"}, true, false, false},
		{"--json", cmdutil.GlobalOptions{Output: "json"}, true, true, false},
		{"-o yaml", cmdutil.GlobalOptions{Output: "yaml"}, true, true, false},
		{"--quiet", cmdutil.GlobalOptions{Output: "table", Quiet: true}, true, true, false},
		{"--no-input", cmdutil.GlobalOptions{Output: "table", NoInput: true}, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, in, _, _ := iostreams.Test()
			ios.SetStdinTTY(tc.stdin)
			ios.SetStdoutTTY(tc.stdout)
			in.WriteString("kill all\n")
			global := tc.global
			f := &cmdutil.Factory{IOStreams: ios, Global: &global}

			sched, _, _ := testScheduler(t, time.Hour)
			defer sched.stop()

			c := newController(f, sched, nil, nil, Source{}, DefaultForwardRetries)
			if !tc.enabled {
				require.Nil(t, c)
				assert.Equal(t, "kill all\n", in.String(), "a disabled control layer reads no stdin")
				return
			}
			require.NotNil(t, c)
		})
	}
}

func TestNewController_DisabledWithoutAScheduler(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	assert.Nil(t, newController(f, nil, nil, nil, Source{}, 0))
}

// errReader stands in for the EIO a backgrounded read returns once SIGTTIN is ignored.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("input/output error") }

func TestControl_ReadErrorDoesNotEndTheSession(t *testing.T) {
	c, sched, _, _ := testController(t)
	c.f.IOStreams.In = errReader{}

	queued, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	done := make(chan struct{})
	go func() {
		c.run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the control reader never gave up on a stdin it cannot read")
	}
	assert.True(t, sched.has(queued.ordinal), "a dead control layer must not disturb the retries")
	assert.False(t, c.active(),
		"a reader that gave up must stop the banner and the #n prefix advertising it")
}

func TestController_ActiveTracksTheReader(t *testing.T) {
	var absent *controller
	assert.False(t, absent.active(), "a session with no control layer never advertises one")

	c, _, in, _ := testController(t)
	assert.True(t, c.active(), "a live reader advertises control")

	in.WriteString("?\n")
	c.run(context.Background())
	assert.False(t, c.active(), "stdin ended, so nothing reads the input any more")
}

func TestControlHelp_EveryCommandItAdvertisesParses(t *testing.T) {
	for _, item := range strings.Split(controlHelp, "·") {
		item = strings.TrimSpace(item)
		if i := strings.Index(item, "("); i >= 0 {
			item = strings.TrimSpace(item[:i]) // drop the parenthetical gloss
		}
		for _, form := range strings.Split(item, "/") {
			form = strings.TrimSpace(form)
			if !strings.HasPrefix(form, "retries") && !strings.HasPrefix(form, "kill") {
				form = "retries " + form
			}
			form = strings.ReplaceAll(form, "<n>…", "3")
			action, _ := parseControl(form)
			assert.NotEqual(t, actionUnknown, action, "help advertises %q, which the parser rejects", form)
		}
	}
}

func TestControl_SaysWhenTheReaderDies(t *testing.T) {
	c, _, _, errOut := testController(t)
	c.f.IOStreams.In = errReader{}
	c.run(context.Background())
	assert.Contains(t, errOut.String(), "no longer reading input")
	assert.False(t, c.active())
}

func followerController(t *testing.T) (*controller, *registry, *coordinator, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	reg, peer, _, _, errOut := follower(t)
	in, ok := reg.f.IOStreams.In.(*bytes.Buffer)
	require.True(t, ok)
	return reg.control, reg, peer, in, errOut
}

func TestControl_ListMarksChainsAnotherSessionHolds(t *testing.T) {
	c, reg, peer, in, errOut := followerController(t)
	armPeer(t, peer, peerFrame, 3, 5*time.Minute)
	reg.scan(context.Background())
	mine := Frame{ID: "mine", Type: "payment_in", Raw: []byte(`{}`)}
	_, _, ok := c.sched.arm(context.Background(), job{frame: mine}, reg.coord.ordinalFor(mine))
	require.True(t, ok)

	in.WriteString("retries\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, "HELD BY")
	assert.Contains(t, out, "this session")
	assert.Contains(t, out, "another session")
	assert.Contains(t, out, "transaction_credit", "the followed chain is named")
	assert.Contains(t, out, "3/7", "and spends the budget its own session was started with")
	assert.Less(t, strings.Index(out, "#1"), strings.Index(out, "#2"), "one sequence, in order")
}

func TestControl_ListHasNoHeldByColumnWithoutPeers(t *testing.T) {
	c, _, _, in, errOut := followerController(t)
	_, _, ok := c.sched.arm(context.Background(), job{frame: Frame{ID: "mine", Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	in.WriteString("retries\n")
	c.run(context.Background())
	assert.NotContains(t, errOut.String(), "HELD BY")
}

func TestControl_KillReachesAChainAnotherSessionHolds(t *testing.T) {
	c, reg, peer, in, errOut := followerController(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())

	in.WriteString("kill 1\n")
	c.run(context.Background())

	assert.Contains(t, errOut.String(), "killed #1")
	assert.Equal(t, chainOver, peer.beforeAttempt(peerFrame, 1),
		"the sending session stops at its next attempt")
}

func TestControl_KillAllReachesFollowedChains(t *testing.T) {
	c, reg, peer, in, errOut := followerController(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())

	in.WriteString("kill all\n")
	c.run(context.Background())

	assert.Contains(t, errOut.String(), "killed 1 chain(s)")
	assert.Equal(t, chainOver, peer.beforeAttempt(peerFrame, 1))
}

func TestControl_KillReportsANumberNobodyHolds(t *testing.T) {
	c, reg, peer, in, errOut := followerController(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())

	in.WriteString("kill 1 9\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Contains(t, out, "killed #1")
	assert.Contains(t, out, "#9")
}

// A private fallback count can clash with a shared number.
func TestControl_ListPrefersTheLocalChainOnAClashingNumber(t *testing.T) {
	c, reg, peer, in, errOut := followerController(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())
	_, _, ok := c.sched.arm(context.Background(), job{frame: Frame{ID: "mine", Type: "payment_in", Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	in.WriteString("retries\n")
	c.run(context.Background())

	out := errOut.String()
	assert.Equal(t, 1, strings.Count(out, "#1"))
	assert.Contains(t, out, "payment_in")
	assert.NotContains(t, out, "transaction_credit")
}
