package listen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/forward"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const peerOrigin = "http://localhost:3000/"

// follower returns a fresh registry plus a coordinator standing in for the holding session.
// flock is per open file description, so one process contends with itself as two would.
func follower(t *testing.T) (*registry, *coordinator, *retryScheduler, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())

	ios, _, out, errOut := iostreams.Test()
	ios.SetStdinTTY(true)
	ios.SetStdoutTTY(true)
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	sched, _, _ := testScheduler(t, time.Hour)
	t.Cleanup(func() { sched.stop() })

	own := testCoordinator(t, "webhook", peerOrigin, DefaultForwardRetries)
	peer := testCoordinator(t, "webhook", peerOrigin, DefaultForwardRetries)

	reg := newRegistry(f, own, sched, peerOrigin)
	require.NotNil(t, reg)
	src := Source{Tag: func(io *iostreams.IOStreams, fr Frame) string { return Tag(io, "txn", fr.ID) }}
	reg.control = newController(f, sched, own, reg, src, DefaultForwardRetries)
	require.NotNil(t, reg.control)
	return reg, peer, sched, out, errOut
}

var peerFrame = Frame{ID: "evt_peer_1", Type: "transaction_credit", Raw: []byte(`{"id":"evt_peer_1"}`)}

func armPeer(t *testing.T, c *coordinator, fr Frame, next int, wait time.Duration) int {
	t.Helper()
	ordinal := c.ordinalFor(fr)
	require.NotZero(t, ordinal)
	c.holdChain(fr, ordinal)
	c.armed(fr, chainMeta{
		next:  next,
		cap:   DefaultForwardRetries,
		due:   time.Now().Add(wait),
		event: fr.Type,
		tag:   "   txn=evt_peer",
	})
	return ordinal
}

func sendPeer(t *testing.T, c *coordinator, fr Frame, attempt int, sendErr error) {
	t.Helper()
	require.Equal(t, sendIt, c.beforeAttempt(fr, attempt))
	c.settle(fr, attempt, nil, sendErr)
	if attempt < DefaultForwardRetries {
		c.armed(fr, chainMeta{
			next: attempt + 1, cap: DefaultForwardRetries,
			due: time.Now().Add(time.Minute), event: fr.Type, tag: "   txn=evt_peer",
		})
	}
}

func TestRegistry_FollowsAChainItNeverReceived(t *testing.T) {
	reg, peer, _, out, errOut := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background()) // s2 opens while the chain is waiting on its timer
	sendPeer(t, peer, peerFrame, 1, errors.New("connection refused"))
	reg.scan(context.Background()) // and hears the attempt land

	got := errOut.String()
	assert.Contains(t, got, "following retry chains held by another listen session")
	assert.Contains(t, got, "transaction_credit", "the delivery is named from what the holder recorded")
	assert.Contains(t, got, "not responding", "a handler that never answered is a diagnostic")
	assert.Contains(t, got, "(retry 1/7)")
	assert.Contains(t, got, "txn=evt_peer", "the tag the holder rendered")
	assert.Contains(t, got, "#1 retry 2/7", "and what happens next")
	assert.Empty(t, out.String(), "an unreachable handler never writes to stdout")
}

func TestRegistry_ReportsTheHandlersAnswer(t *testing.T) {
	reg, peer, _, out, _ := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())

	require.Equal(t, sendIt, peer.beforeAttempt(peerFrame, 1))
	peer.settle(peerFrame, 1, &forward.Result{
		StatusCode: 503, Latency: 42 * time.Millisecond, Body: []byte("upstream down"),
	}, nil)
	reg.scan(context.Background())

	assert.Contains(t, out.String(), "503")
	assert.Contains(t, out.String(), "42ms")
	assert.Contains(t, out.String(), "upstream down", "the body snippet the holder kept")
}

func TestRegistry_ReportsEachAttemptOnce(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())
	sendPeer(t, peer, peerFrame, 1, errors.New("connection refused"))

	reg.scan(context.Background())
	reg.scan(context.Background())

	assert.Equal(t, 1, strings.Count(errOut.String(), "(retry 1/7)"))
}

func TestRegistry_IgnoresAChainThisSessionHolds(t *testing.T) {
	reg, peer, sched, _, errOut := follower(t)
	ordinal := armPeer(t, peer, peerFrame, 1, time.Minute)
	sendPeer(t, peer, peerFrame, 1, errors.New("connection refused"))

	_, _, ok := sched.arm(context.Background(), job{frame: peerFrame}, ordinal)
	require.True(t, ok)
	require.True(t, reg.coord.holdChain(peerFrame, ordinal))

	reg.scan(context.Background())
	assert.Empty(t, errOut.String())
	assert.Empty(t, reg.views())
}

func TestRegistry_IgnoresAChainItHoldsUnderAPrivateNumber(t *testing.T) {
	reg, peer, sched, _, errOut := follower(t)
	ordinal := armPeer(t, peer, peerFrame, 1, time.Minute)
	sendPeer(t, peer, peerFrame, 1, errors.New("connection refused"))

	// A failed allocation left a private chain on the number the sequence now hands out.
	blocker, _, ok := sched.arm(context.Background(), job{frame: Frame{ID: "other", Raw: []byte(`{}`)}}, ordinal)
	require.True(t, ok)
	local, _, ok := sched.arm(context.Background(), job{frame: peerFrame}, ordinal)
	require.True(t, ok)
	require.NotEqual(t, ordinal, local.ordinal, "the local number must diverge for this test to mean anything")
	require.True(t, reg.coord.holdChain(peerFrame, local.ordinal))

	sched.release(blocker)
	require.False(t, sched.has(ordinal))

	reg.scan(context.Background())
	assert.Empty(t, errOut.String(), "its own forwarder reports this delivery")
	assert.Empty(t, reg.views())
}

// The marker can fail while a peer's orphan check holds it, though the peer armed the state.
func TestRegistry_IgnoresAChainItHoldsWithoutAMarker(t *testing.T) {
	reg, peer, sched, _, errOut := follower(t)
	ordinal := armPeer(t, peer, peerFrame, 1, time.Minute)
	sendPeer(t, peer, peerFrame, 1, errors.New("connection refused"))

	_, _, ok := sched.arm(context.Background(), job{frame: peerFrame}, ordinal)
	require.True(t, ok)
	require.False(t, reg.coord.holdsClaim(reg.coord.path(peerFrame)), "the marker was never taken")

	reg.scan(context.Background())
	assert.Empty(t, errOut.String(), "its own forwarder reports this delivery")
	assert.Empty(t, reg.views())
}

func TestRegistry_IgnoresAStateWithNoArmedAttempt(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	require.NotZero(t, peer.ordinalFor(peerFrame))

	for i := 0; i < orphanConfirmations+1; i++ {
		reg.scan(context.Background())
	}

	assert.Empty(t, errOut.String())
	st, ok := peer.stateAt(peer.path(peerFrame))
	require.True(t, ok)
	assert.False(t, st.Done, "an unarmed delivery must not be ended by a follower")
}

func TestRegistry_NeverAbandonsAChainAPeerStillHolds(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)

	for i := 0; i < orphanConfirmations+2; i++ {
		reg.scan(context.Background())
	}

	assert.NotContains(t, errOut.String(), "abandoned")
	st, ok := peer.stateAt(peer.path(peerFrame))
	require.True(t, ok)
	assert.False(t, st.Done)
	assert.Len(t, reg.views(), 1, "still followed, still listed")
}

func TestRegistry_ChainSurvivesOneOfTwoHoldersLeaving(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	other := testCoordinator(t, "webhook", peerOrigin, DefaultForwardRetries)

	ordinal := armPeer(t, peer, peerFrame, 1, time.Minute)
	other.holdChain(peerFrame, ordinal)
	peer.releaseChain(ordinal) // one session exits

	for i := 0; i < orphanConfirmations+2; i++ {
		reg.scan(context.Background())
	}
	assert.NotContains(t, errOut.String(), "abandoned")
}

func TestRegistry_AbandonsAChainWithNoHolderLeft(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	ordinal := armPeer(t, peer, peerFrame, 1, time.Minute)
	peer.releaseChain(ordinal) // the session holding it exited

	reg.scan(context.Background())
	assert.NotContains(t, errOut.String(), "abandoned",
		"one tick is not proof: a holder can be turned away for a moment")

	reg.scan(context.Background())
	assert.Contains(t, errOut.String(), "#1 abandoned — the session holding it exited")

	st, ok := peer.stateAt(peer.path(peerFrame))
	require.True(t, ok)
	assert.True(t, st.Done)
	assert.False(t, st.Killed, "nobody killed it; its session went away")

	errOut.Reset()
	reg.scan(context.Background())
	assert.Empty(t, errOut.String(), "said once")
}

func TestRegistry_ClosesAChainThatEndedElsewhere(t *testing.T) {
	cases := []struct {
		name    string
		end     func(c *coordinator, fr Frame)
		attempt int
		want    string
	}{
		{
			name: "accepted or refused by the channel",
			end:  func(c *coordinator, fr Frame) { c.finishFrame(fr, false) },
			want: "stopped — the delivery ended in another session",
		},
		{
			name: "killed by the developer",
			end:  func(c *coordinator, fr Frame) { c.finishFrame(fr, true) },
			want: "#1 killed in another session",
		},
		{
			name:    "budget spent",
			end:     func(c *coordinator, fr Frame) { c.finishFrame(fr, false) },
			attempt: DefaultForwardRetries,
			want:    "gave up after 7 retries",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, peer, _, _, errOut := follower(t)
			armPeer(t, peer, peerFrame, 1, time.Minute)
			if tc.attempt > 0 {
				require.Equal(t, sendIt, peer.beforeAttempt(peerFrame, tc.attempt))
			}
			reg.scan(context.Background())
			require.Len(t, reg.views(), 1)

			tc.end(peer, peerFrame)
			reg.scan(context.Background())

			assert.Contains(t, errOut.String(), tc.want)
			assert.Empty(t, reg.views(), "an ended chain leaves the listing")
		})
	}
}

func TestRegistry_SaysNothingAboutAChainItNeverFollowed(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	peer.finishFrame(peerFrame, true)

	reg.scan(context.Background())
	assert.Empty(t, errOut.String())
}

func TestRegistry_KillEndsAPeerChain(t *testing.T) {
	reg, peer, _, _, _ := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())

	assert.Equal(t, []int{1}, reg.kill([]int{1}))
	assert.Empty(t, reg.views())

	st, ok := peer.stateAt(peer.path(peerFrame))
	require.True(t, ok)
	assert.True(t, st.Killed)
	assert.True(t, st.Done)
	assert.Equal(t, chainOver, peer.beforeAttempt(peerFrame, 2),
		"the session holding it stops at its next attempt")
}

func TestRegistry_KillIgnoresANumberItDoesNotFollow(t *testing.T) {
	reg, peer, _, _, _ := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())
	assert.Empty(t, reg.kill([]int{9}))
	assert.Len(t, reg.views(), 1)
}

func TestRegistry_KillAllEndsEveryFollowedChain(t *testing.T) {
	reg, peer, _, _, _ := follower(t)
	second := Frame{ID: "evt_peer_2", Type: "transaction_credit", Raw: []byte(`{"id":"evt_peer_2"}`)}
	armPeer(t, peer, peerFrame, 1, time.Minute)
	armPeer(t, peer, second, 1, time.Minute)
	reg.scan(context.Background())
	require.Len(t, reg.views(), 2)

	assert.Len(t, reg.killAll(), 2)
	assert.Empty(t, reg.views())
}

func TestRegistry_SanitizesLabelsItReadsBack(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	ordinal := armPeer(t, peer, peerFrame, 1, time.Minute)
	require.Equal(t, 1, ordinal)
	reg.scan(context.Background())

	hostile := deliveryState{
		Ordinal: 1, Channel: peer.channelID(),
		Attempt: 1, Next: 2, Cap: 7, Settled: true, Failure: "refused",
		DueMS: time.Now().Add(time.Minute).UnixMilli(),
		Event: "\x1b[2Jwiped" + strings.Repeat("e", maxMetaLen),
		Tag:   "\x1b]0;title\x07" + strings.Repeat("t", maxMetaLen),
	}
	data, err := json.Marshal(hostile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(peer.path(peerFrame), data, 0o600))

	reg.scan(context.Background())

	got := errOut.String()
	assert.NotContains(t, got, "\x1b[2J", "a screen clear is not a label")
	assert.NotContains(t, got, "\x1b]0;", "nor is a window title")
	assert.Contains(t, got, "wiped")
	assert.Less(t, len(got), 4*maxMetaLen, "and neither label is unbounded")
}

func TestNewRegistry_MirrorsOnlyWhereStdoutIsForAPerson(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	coord := testCoordinator(t, "webhook", peerOrigin, DefaultForwardRetries)
	sched, _, _ := testScheduler(t, time.Hour)
	t.Cleanup(func() { sched.stop() })

	terminal := func() (*cmdutil.Factory, *iostreams.IOStreams) {
		ios, _, _, _ := iostreams.Test()
		ios.SetStdinTTY(true)
		ios.SetStdoutTTY(true)
		return &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}, ios
	}

	f, _ := terminal()
	r := newRegistry(f, coord, sched, peerOrigin)
	require.NotNil(t, r)
	assert.True(t, r.mirrors(), "a person watching sees a peer's delivery")

	for _, tc := range []struct {
		name    string
		degrade func(*cmdutil.Factory, *iostreams.IOStreams)
	}{
		{"--json", func(f *cmdutil.Factory, _ *iostreams.IOStreams) { f.Global.Output = "json" }},
		{"--quiet", func(f *cmdutil.Factory, _ *iostreams.IOStreams) { f.Global.Quiet = true }},
		{"a pipe", func(_ *cmdutil.Factory, ios *iostreams.IOStreams) { ios.SetStdoutTTY(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ios := terminal()
			tc.degrade(f, ios)
			r := newRegistry(f, coord, sched, peerOrigin)
			require.NotNil(t, r)
			assert.False(t, r.mirrors())
		})
	}

	f, _ = terminal()
	assert.Nil(t, newRegistry(f, nil, sched, peerOrigin), "retry off")
	assert.Nil(t, newRegistry(f, coord, nil, peerOrigin), "no scheduler")
}

func TestRegistry_PeerRetryNoteStaysOffAParsedStdout(t *testing.T) {
	drive := func(t *testing.T, reg *registry, peer *coordinator) {
		t.Helper()
		armPeer(t, peer, peerFrame, 1, time.Minute)
		reg.scan(context.Background())
		require.Equal(t, sendIt, peer.beforeAttempt(peerFrame, 1))
		// A non-2xx is not a send error, so st.Failure stays empty.
		peer.settle(peerFrame, 1, &forward.Result{StatusCode: 503, Latency: time.Millisecond}, nil)
		peer.armed(peerFrame, chainMeta{
			next: 2, cap: DefaultForwardRetries,
			due: time.Now().Add(time.Minute), event: peerFrame.Type, tag: "   txn=evt_peer",
		})
		reg.scan(context.Background())
	}

	t.Run("piped", func(t *testing.T) {
		reg, peer, _, out, errOut := follower(t)
		reg.f.IOStreams.SetStdoutTTY(false)
		drive(t, reg, peer)
		assert.Empty(t, out.String(), "nothing this session forwarded, so nothing on stdout")
		assert.Contains(t, errOut.String(), "retry 2/7", "and the note still lands")
	})

	t.Run("a person watching", func(t *testing.T) {
		reg, peer, _, out, errOut := follower(t)
		drive(t, reg, peer)
		assert.Contains(t, out.String(), "503", "a handler that answered is still data on stdout")
		assert.Contains(t, errOut.String(), "retry 2/7")
	})

	t.Run("quiet", func(t *testing.T) {
		reg, peer, _, out, errOut := follower(t)
		reg.f.Global.Quiet = true
		drive(t, reg, peer)
		assert.Empty(t, out.String())
		assert.NotContains(t, errOut.String(), "retry 2/7", "per-attempt chatter is what --quiet drops")
	})
}

func TestRegistry_NilIsInert(t *testing.T) {
	var r *registry
	assert.NotPanics(t, func() {
		r.scan(context.Background())
		r.run(context.Background())
		assert.Nil(t, r.views())
		assert.Nil(t, r.kill([]int{1}))
		assert.Nil(t, r.killAll())
	})
}

func TestRegistry_RunStopsWithTheSession(t *testing.T) {
	reg, _, _, _, _ := follower(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reg.run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return when the session ended")
	}
}

func TestRegistry_RendersTheHoldersBudget(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	require.NotZero(t, peer.ordinalFor(peerFrame))
	peer.holdChain(peerFrame, 1)
	peer.armed(peerFrame, chainMeta{next: 2, cap: 10, due: time.Now().Add(time.Minute), event: "x"})
	reg.scan(context.Background())

	require.Equal(t, sendIt, peer.beforeAttempt(peerFrame, 2))
	peer.settle(peerFrame, 2, nil, errors.New("refused"))
	peer.armed(peerFrame, chainMeta{next: 3, cap: 10, due: time.Now().Add(time.Minute), event: "x"})
	reg.scan(context.Background())
	assert.Contains(t, errOut.String(), "(retry 2/10)")
	assert.Contains(t, errOut.String(), "#1 retry 3/10")
}

func TestCoordinator_HoldMarkerIsSharedAndReleased(t *testing.T) {
	a, b := twoListeners(t)
	path := a.path(deliveryFrame)
	assert.True(t, a.unheld(path), "nobody has armed anything yet")

	a.holdChain(deliveryFrame, 1)
	b.holdChain(deliveryFrame, 1)
	assert.False(t, a.unheld(path))

	a.releaseChain(1)
	assert.False(t, b.unheld(path), "one holder left is still a holder")

	b.releaseChain(1)
	assert.True(t, a.unheld(path))
}

func TestCoordinator_ArmedRecordsWhatAFollowerNeeds(t *testing.T) {
	a, _ := twoListeners(t)
	due := time.Now().Add(90 * time.Second)
	a.armed(deliveryFrame, chainMeta{next: 4, cap: 9, due: due, event: "payment_in", tag: "   txn=1"})

	st, ok := a.stateAt(a.path(deliveryFrame))
	require.True(t, ok)
	assert.Equal(t, 4, st.Next)
	assert.Equal(t, 9, st.Cap)
	assert.Equal(t, "payment_in", st.Event)
	assert.Equal(t, "   txn=1", st.Tag)
	assert.Equal(t, due.UnixMilli(), st.DueMS)

	a.armed(deliveryFrame, chainMeta{next: 0, cap: 9, due: due, event: "payment_in"})
	st, _ = a.stateAt(a.path(deliveryFrame))
	assert.Equal(t, 4, st.Next, "an unarmed write is not an arm")
}

func TestSane_ClampsTheFollowerFields(t *testing.T) {
	got := sane(deliveryState{
		Cap: MaxForwardRetries + 1, Next: -3, DueMS: -1,
		Event: strings.Repeat("e", maxMetaLen*2), Tag: "\x1b[2Jx",
	})
	assert.Zero(t, got.Cap)
	assert.Zero(t, got.Next)
	assert.Zero(t, got.DueMS)
	assert.Len(t, got.Event, maxMetaLen)
	assert.Equal(t, "[2Jx", got.Tag)
	assert.NotContains(t, got.Tag, "\x1b")
}

func TestClaimPaths_ListsStatesOnlyAndIsBounded(t *testing.T) {
	a, _ := twoListeners(t)
	for i := 0; i < 3; i++ {
		require.NotZero(t, a.ordinalFor(Frame{ID: "evt_" + strings.Repeat("x", i+1)}))
	}
	paths := a.claimPaths(maxScanEntries)
	assert.Len(t, paths, 3, "the sequence file is not a delivery")
	for _, p := range paths {
		assert.True(t, strings.HasSuffix(p, ".json"))
	}
	assert.Len(t, a.claimPaths(2), 2)
}

func TestOwnPathFor_SitsBesideTheState(t *testing.T) {
	assert.Equal(t, "/x/abc.own", ownPathFor("/x/abc.json"))
}

func TestPlainTag_CarriesNoColor(t *testing.T) {
	src := Source{Tag: func(io *iostreams.IOStreams, fr Frame) string { return Tag(io, "txn", fr.ID) }}
	assert.Equal(t, "   txn=evt_peer_1", plainTag(src, peerFrame))
	assert.Empty(t, plainTag(Source{}, peerFrame))
}

func TestCoordinator_FollowerHelpersAreNilSafe(t *testing.T) {
	var c *coordinator
	assert.NotPanics(t, func() {
		c.holdChain(peerFrame, 1)
		c.releaseChain(1)
		c.releaseChains([]int{1})
		c.armed(peerFrame, chainMeta{next: 1})
		assert.Nil(t, c.claimPaths(10))
		assert.False(t, c.endAt("nope.json", endedByKill))
		assert.False(t, c.unheld("nope.json"))
	})
}

func TestRegistry_FollowsOnlyItsOwnChannelAndHandler(t *testing.T) {
	reg, _, _, _, errOut := follower(t)

	otherScope := testCoordinator(t, "bankhub_ipn", peerOrigin, DefaultForwardRetries)
	otherHandler := testCoordinator(t, "webhook", "http://localhost:4000/", DefaultForwardRetries)
	armPeer(t, otherScope, peerFrame, 1, time.Minute)
	armPeer(t, otherHandler, peerFrame, 1, time.Minute)

	for i := 0; i < orphanConfirmations+1; i++ {
		reg.scan(context.Background())
	}

	assert.Empty(t, errOut.String(), "another channel's chain is not this session's news")
	assert.Empty(t, reg.views())
	for _, c := range []*coordinator{otherScope, otherHandler} {
		st, ok := c.stateAt(c.path(peerFrame))
		require.True(t, ok)
		assert.False(t, st.Done, "and must never be ended from outside its channel")
	}
}

func TestRegistry_IgnoresAStateWithNoChannel(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	legacy := deliveryState{
		Ordinal: 1, Next: 1, Cap: 7, Event: "transaction_credit",
		DueMS: time.Now().Add(time.Minute).UnixMilli(),
	}
	data, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(peer.path(peerFrame)), 0o700))
	require.NoError(t, os.WriteFile(peer.path(peerFrame), data, 0o600))

	for i := 0; i < orphanConfirmations+1; i++ {
		reg.scan(context.Background())
	}
	assert.Empty(t, errOut.String())
	st, ok := peer.stateAt(peer.path(peerFrame))
	require.True(t, ok)
	assert.False(t, st.Done)
}

func TestCoordinator_StampsTheChannelOnTheStatesItSetsUp(t *testing.T) {
	a, b := twoListeners(t)
	require.NotZero(t, a.ordinalFor(deliveryFrame))
	st, ok := a.stateAt(a.path(deliveryFrame))
	require.True(t, ok)
	assert.Equal(t, a.channelID(), st.Channel)
	assert.Equal(t, b.channelID(), st.Channel, "two sessions on one channel agree on it")

	other := testCoordinator(t, "pg_ipn", peerOrigin, DefaultForwardRetries)
	assert.NotEqual(t, a.channelID(), other.channelID())

	a.armed(Frame{ID: "evt_armed"}, chainMeta{next: 1, cap: 7, due: time.Now(), event: "x"})
	st, ok = a.stateAt(a.path(Frame{ID: "evt_armed"}))
	require.True(t, ok)
	assert.Equal(t, a.channelID(), st.Channel)
}

func TestRegistry_DoesNotReplayAnAttemptThatPredatesIt(t *testing.T) {
	reg, peer, _, out, errOut := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	sendPeer(t, peer, peerFrame, 1, errors.New("connection refused"))

	reg.scan(context.Background()) // first sight of a chain already one attempt in
	assert.NotContains(t, errOut.String(), "(retry 1/7)", "that attempt is not this session's news")
	assert.Empty(t, out.String())
	require.Len(t, reg.views(), 1, "but the chain is listed, with its current state")

	sendPeer(t, peer, peerFrame, 2, errors.New("connection refused"))
	reg.scan(context.Background())
	assert.Contains(t, errOut.String(), "(retry 2/7)", "and every attempt after it is reported")
}

func TestCoordinator_HoldChainReportsWhetherItTookTheMarker(t *testing.T) {
	a, b := twoListeners(t)
	assert.True(t, a.holdChain(deliveryFrame, 1))
	assert.True(t, b.holdChain(deliveryFrame, 1), "the marker is shared")
	assert.True(t, a.holdChain(deliveryFrame, 1), "already held here")

	other := Frame{ID: "evt_contended"}
	blocker, ok, err := config.TryLockFile(ownPathFor(a.path(other)))
	require.NoError(t, err)
	require.True(t, ok)
	assert.False(t, a.holdChain(other, 2), "someone holds it exclusively right now")
	blocker.Release()
	assert.True(t, a.holdChain(other, 2))
}

func TestRegistry_KeepsAChainWhoseStateWasLockedThisTick(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())
	require.Len(t, reg.views(), 1)

	blocker, ok, err := config.TryLockFile(peer.path(peerFrame))
	require.NoError(t, err)
	require.True(t, ok)
	reg.scan(context.Background()) // this tick cannot read the state
	assert.Len(t, reg.views(), 1, "the delivery still exists, so the chain is still followed")
	blocker.Release()

	sendPeer(t, peer, peerFrame, 1, errors.New("connection refused"))
	reg.scan(context.Background())
	assert.Contains(t, errOut.String(), "(retry 1/7)", "and its outcome is still reported")
}

// A marker's mtime is never refreshed, so age says nothing about its holder.
func TestCoordinator_SweepRemovesAMarkerOnlyWithItsState(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	c := testCoordinator(t, "webhook", peerOrigin, DefaultForwardRetries)

	stale, live := Frame{ID: "evt_stale"}, Frame{ID: "evt_live"}
	for _, fr := range []Frame{stale, live} {
		require.NotZero(t, c.ordinalFor(fr))
		require.True(t, c.holdChain(fr, 1))
		c.releaseChain(1)
	}
	old := time.Now().Add(-2 * claimStaleAge)
	require.NoError(t, os.Chtimes(c.path(stale), old, old))
	require.NoError(t, os.Chtimes(ownPathFor(c.path(stale)), old, old))
	require.NoError(t, os.Chtimes(ownPathFor(c.path(live)), old, old))

	c.sweep()

	for _, path := range []string{c.path(stale), ownPathFor(c.path(stale))} {
		_, err := os.Stat(path)
		assert.True(t, os.IsNotExist(err), "a stale delivery takes its marker with it: %s", path)
	}
	for _, path := range []string{c.path(live), ownPathFor(c.path(live))} {
		_, err := os.Stat(path)
		assert.NoError(t, err, "a live delivery keeps both: %s", path)
	}
}

func TestRegistry_NotesTheNextAttemptWhenAScanLandsMidArm(t *testing.T) {
	reg, peer, _, _, errOut := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())

	require.Equal(t, sendIt, peer.beforeAttempt(peerFrame, 1))
	peer.settle(peerFrame, 1, nil, errors.New("connection refused"))
	reg.scan(context.Background()) // settled, but the next attempt is not armed yet
	assert.Contains(t, errOut.String(), "(retry 1/7)")
	assert.NotContains(t, errOut.String(), "retry 2/7", "nothing is armed to announce")

	peer.armed(peerFrame, chainMeta{
		next: 2, cap: DefaultForwardRetries,
		due: time.Now().Add(time.Minute), event: peerFrame.Type,
	})
	reg.scan(context.Background())
	assert.Contains(t, errOut.String(), "#1 retry 2/7", "announced on the tick that sees it")

	reg.scan(context.Background())
	assert.Equal(t, 1, strings.Count(errOut.String(), "retry 2/7"), "and only once")
}

func TestRegistry_ASecondFollowerReportsAnAbandonmentAsOne(t *testing.T) {
	first, peer, _, _, firstErr := follower(t)
	second := newRegistry(first.f, first.coord, newRetryScheduler(make(chan job, 1), io.Discard, false), peerOrigin)
	require.NotNil(t, second)
	second.control = first.control

	ordinal := armPeer(t, peer, peerFrame, 1, time.Minute)
	first.scan(context.Background())
	second.scan(context.Background())
	peer.releaseChain(ordinal)

	for i := 0; i < orphanConfirmations; i++ {
		first.scan(context.Background())
	}
	require.Contains(t, firstErr.String(), "abandoned — the session holding it exited")

	before := strings.Count(firstErr.String(), "abandoned")
	second.scan(context.Background())
	got := firstErr.String()
	assert.Equal(t, before+1, strings.Count(got, "abandoned"), "the second says the same thing")
	assert.NotContains(t, got, "stopped — the delivery ended in another session")
}

func TestRegistry_KillKeepsTheChainWhenTheStateCannotBeWritten(t *testing.T) {
	reg, peer, _, _, _ := follower(t)
	armPeer(t, peer, peerFrame, 1, time.Minute)
	reg.scan(context.Background())

	blocker, ok, err := config.TryLockFile(peer.path(peerFrame))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, reg.kill([]int{1}), "nothing was killed")
	assert.Len(t, reg.views(), 1, "so the chain is still followed")
	blocker.Release()

	assert.Equal(t, []int{1}, reg.kill([]int{1}))
	assert.Empty(t, reg.views())
}
