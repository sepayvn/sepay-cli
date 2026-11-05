package listen

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/forward"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoListeners returns two coordinators on one channel and handler. flock is per open file
// description, so they contend exactly as two processes do.
func twoListeners(t *testing.T) (*coordinator, *coordinator) {
	t.Helper()
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	a := newCoordinator("webhook", "http://localhost:3000/", DefaultForwardRetries)
	b := newCoordinator("webhook", "http://localhost:3000/", DefaultForwardRetries)
	require.NotNil(t, a)
	require.NotNil(t, b)
	return a, b
}

var deliveryFrame = Frame{ID: "evt_9f2c1a", Raw: []byte(`{"id":"evt_9f2c1a"}`)}

func TestDeliveryID_PrefersTheHubID(t *testing.T) {
	assert.Equal(t, "evt_9f2c1a", deliveryID(deliveryFrame))
}

func TestDeliveryID_FallsBackToTheRawFrame(t *testing.T) {
	fr := Frame{Raw: []byte(`{"a":1}`)}
	got := deliveryID(fr)
	assert.Equal(t, deliveryID(Frame{Raw: []byte(`{"a":1}`)}), got, "the same bytes name the same delivery")
	assert.NotEqual(t, got, deliveryID(Frame{Raw: []byte(`{"a":2}`)}))
	assert.NotEmpty(t, got)
}

func TestCoordinatorPath_SeparatesScopeOriginAndDelivery(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	base := newCoordinator("webhook", "http://localhost:3000/", 7)
	require.NotNil(t, base)

	otherScope := newCoordinator("pg_ipn", "http://localhost:3000/", 7)
	otherOrigin := newCoordinator("webhook", "http://localhost:4000/", 7)

	assert.NotEqual(t, base.path(deliveryFrame), otherScope.path(deliveryFrame),
		"a channel is an isolation boundary")
	assert.NotEqual(t, base.path(deliveryFrame), otherOrigin.path(deliveryFrame),
		"two handlers are not duplicating each other and must both retry")
	assert.NotEqual(t, base.path(deliveryFrame), base.path(Frame{ID: "evt_other"}))
	first := base.path(deliveryFrame)
	assert.Equal(t, first, base.path(deliveryFrame), "stable across calls")
}

func TestCoordinator_SecondListenerStandsDownOnTheSameAttempt(t *testing.T) {
	a, b := twoListeners(t)

	assert.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1), "the first to arrive owns it")
	assert.Equal(t, standDown, b.beforeAttempt(deliveryFrame, 1), "the second must not send it again")
}

func TestCoordinator_NextAttemptIsClaimableAgain(t *testing.T) {
	a, b := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))
	require.Equal(t, standDown, b.beforeAttempt(deliveryFrame, 1))

	assert.Equal(t, sendIt, b.beforeAttempt(deliveryFrame, 2),
		"the peer takes the next attempt, which is how a dead owner is taken over")
	assert.Equal(t, standDown, a.beforeAttempt(deliveryFrame, 2))
}

func TestCoordinator_FinishEndsPeerChains(t *testing.T) {
	a, b := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))

	a.finish(job{frame: deliveryFrame, ordinal: 1}, false)

	assert.Equal(t, chainOver, b.beforeAttempt(deliveryFrame, 2))
	assert.Equal(t, chainOver, a.beforeAttempt(deliveryFrame, 2))
}

func TestCoordinator_KillEndsPeerChains(t *testing.T) {
	a, b := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))

	a.finishAll([]Frame{deliveryFrame}, true)

	assert.Equal(t, chainOver, b.beforeAttempt(deliveryFrame, 2),
		"the other session must stop retrying a delivery the developer killed")
}

// Most lock holders never advance the attempt, so contention must not skip a rung.
func TestCoordinator_ContentionFailsOpenRatherThanSkippingARung(t *testing.T) {
	a, b := twoListeners(t)

	held, ok, err := config.TryLockFile(a.path(deliveryFrame))
	require.NoError(t, err)
	require.True(t, ok)
	defer held.Release()

	assert.Equal(t, sendIt, b.beforeAttempt(deliveryFrame, 1),
		"a lock held by a poll must not cost the attempt")
}

func TestCoordinator_StandsDownOnARecordedClaimNotOnALock(t *testing.T) {
	a, b := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))
	assert.Equal(t, standDown, b.beforeAttempt(deliveryFrame, 1))
}

func TestNewCoordinator_NilWhereThereIsNothingToCoordinate(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	assert.Nil(t, newCoordinator("webhook", "http://localhost:3000/", 0), "retry off")
	assert.Nil(t, newCoordinator("", "http://localhost:3000/", 7), "no channel identity")
	assert.Nil(t, newCoordinator("webhook", "", 7), "no handler identity")

	var absent *coordinator
	assert.Equal(t, sendIt, absent.beforeAttempt(deliveryFrame, 1),
		"an uncoordinated session sends every attempt, as it did before coordination existed")
	absent.finish(job{frame: deliveryFrame, ordinal: 1}, true)
	absent.finishAll([]Frame{deliveryFrame}, false)
}

func TestCoordinator_SweepsOnlyStaleClaims(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	claims := filepath.Join(dir, claimDirName)
	require.NoError(t, os.MkdirAll(claims, 0o700))

	stale := filepath.Join(claims, "stale.json")
	fresh := filepath.Join(claims, "fresh.json")
	require.NoError(t, os.WriteFile(stale, []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(fresh, []byte(`{}`), 0o600))
	old := time.Now().Add(-claimStaleAge - time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	require.NotNil(t, newCoordinator("webhook", "http://localhost:3000/", 7))

	assert.NoFileExists(t, stale)
	assert.FileExists(t, fresh, "a claim a live chain could still own must survive")
}

// The ending write refreshes the mtime, so an ended chain needs the short threshold.
func TestCoordinator_SweepsEndedChainsEarlyAndKeepsLiveOnes(t *testing.T) {
	claims := filepath.Join(t.TempDir(), claimDirName)
	require.NoError(t, os.MkdirAll(claims, 0o700))
	t.Setenv("SEPAY_CONFIG_DIR", filepath.Dir(claims))

	write := func(name, body string, age time.Duration) string {
		path := filepath.Join(claims, name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		require.NoError(t, os.WriteFile(ownPathFor(path), nil, 0o600))
		at := time.Now().Add(-age)
		require.NoError(t, os.Chtimes(path, at, at))
		return path
	}

	mid := claimEndedAge + time.Minute
	done := write("done.json", `{"ordinal":1,"attempt":3,"cap":3,"done":true}`, mid)
	killed := write("killed.json", `{"ordinal":2,"attempt":1,"cap":5,"done":true,"killed":true}`, mid)
	abandoned := write("abandoned.json", `{"ordinal":3,"attempt":1,"cap":5,"done":true,"abandoned":true}`, mid)
	live := write("live.json", `{"ordinal":4,"attempt":1,"next":2,"cap":5}`, mid)
	torn := write("torn.json", `{"ordinal":5,"done":tr`, mid)
	fresh := write("fresh.json", `{"ordinal":6,"done":true}`, claimEndedAge/2)

	require.NotNil(t, newCoordinator("webhook", "http://localhost:3000/", 7))

	for _, path := range []string{done, killed, abandoned} {
		assert.NoFileExists(t, path, "an ended chain goes on the short threshold")
		assert.NoFileExists(t, ownPathFor(path), "and takes its marker with it")
	}
	assert.FileExists(t, live, "a chain still running keeps the long threshold")
	assert.FileExists(t, torn, "a torn write reads as not-ended, so it is kept")
	assert.FileExists(t, fresh, "inside the grace, a peer can still read the ending")
}

func TestCoordinator_ClaimFileIsOpaqueAndPrivate(t *testing.T) {
	a, _ := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))

	path := a.path(deliveryFrame)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.NotContains(t, string(data), deliveryFrame.ID, "the id is hashed into the name, never stored")
	assert.NotContains(t, filepath.Base(path), deliveryFrame.ID)

	if info, err := os.Stat(path); err == nil && runtime.GOOS != "windows" {
		assert.Zero(t, info.Mode().Perm()&0o077, "no group or world access")
	}
}

func TestCoordinator_TwoListenersSendOneAttemptPerStep(t *testing.T) {
	a, b := twoListeners(t)

	sends := 0
	for attempt := 1; attempt <= DefaultForwardRetries; attempt++ {
		// A real pair is never in lockstep, so the order alternates.
		first, second := a, b
		if attempt%2 == 0 {
			first, second = b, a
		}
		if first.beforeAttempt(deliveryFrame, attempt) == sendIt {
			sends++
		}
		if second.beforeAttempt(deliveryFrame, attempt) == sendIt {
			sends++
		}
	}

	assert.Equal(t, DefaultForwardRetries, sends,
		"one send per step: %d sessions must not multiply the handler's load", 2)
}

// The kernel drops the lock when the owner dies, so no lease has to expire.
func TestCoordinator_PeerTakesOverWhenTheOwnerStops(t *testing.T) {
	a, b := twoListeners(t)

	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))
	require.Equal(t, standDown, b.beforeAttempt(deliveryFrame, 1))

	for attempt := 2; attempt <= DefaultForwardRetries; attempt++ {
		assert.Equal(t, sendIt, b.beforeAttempt(deliveryFrame, attempt),
			"attempt %d must still go out with the owner gone", attempt)
	}
}

func TestCoordinator_MirrorsTheRealOutcome(t *testing.T) {
	a, b := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))
	require.Equal(t, standDown, b.beforeAttempt(deliveryFrame, 1))

	a.settle(deliveryFrame, 1, &forward.Result{
		StatusCode: 503,
		Latency:    42 * time.Millisecond,
		Body:       []byte("upstream down"),
	}, nil)

	out, ok := b.awaitOutcome(context.Background(), deliveryFrame, 1, time.Second)
	require.True(t, ok)
	require.NoError(t, out.sendErr)
	assert.Equal(t, 503, out.res.StatusCode)
	assert.Equal(t, 42*time.Millisecond, out.res.Latency)
	assert.Equal(t, "upstream down", string(out.res.Body))
}

func TestCoordinator_MirrorsTheCauseOfATransportFailure(t *testing.T) {
	cases := map[string]error{
		"not responding (is your handler running?)":                        errors.New("connection refused"),
		"did not answer in time":                                           context.DeadlineExceeded,
		"failed TLS verification (an untrusted or mismatched certificate)": &tls.CertificateVerificationError{Err: errors.New("x509: unknown authority")},
	}
	for want, sendErr := range cases {
		t.Run(want, func(t *testing.T) {
			a, b := twoListeners(t)
			require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))
			a.settle(deliveryFrame, 1, nil, sendErr)

			out, ok := b.awaitOutcome(context.Background(), deliveryFrame, 1, time.Second)
			require.True(t, ok)
			require.Error(t, out.sendErr)
			assert.Equal(t, want, forward.Failure(out.sendErr), "the peer names the cause the sender saw")
			assert.Nil(t, out.res, "a handler that never answered has no status to show")
		})
	}
}

func TestDeliveryState_AnUnknownFailureReadsAsDown(t *testing.T) {
	_, err := deliveryState{Settled: true, Failure: "\x1b]0;evil\a"}.outcome()
	assert.Equal(t, &forward.Unanswered{Cause: forward.CauseDown}, err)
}

func TestCoordinator_MirrorGivesUpWhenNobodyReports(t *testing.T) {
	_, b := twoListeners(t)
	_, ok := b.awaitOutcome(context.Background(), deliveryFrame, 1, 80*time.Millisecond)
	assert.False(t, ok)
}

func TestCoordinator_MirrorStopsOnCancel(t *testing.T) {
	_, b := twoListeners(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		_, ok := b.awaitOutcome(ctx, deliveryFrame, 1, time.Minute)
		assert.False(t, ok)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled session was still waiting on a peer")
	}
}

func TestCoordinator_ClaimingTheNextAttemptClearsTheLastOutcome(t *testing.T) {
	a, b := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))
	a.settle(deliveryFrame, 1, &forward.Result{StatusCode: 500}, nil)

	require.Equal(t, sendIt, b.beforeAttempt(deliveryFrame, 2))

	_, ok := a.awaitOutcome(context.Background(), deliveryFrame, 2, 80*time.Millisecond)
	assert.False(t, ok, "attempt 2 has not reported yet, so there is nothing to mirror")
}

// One session's first-attempt success must not settle the delivery for a peer whose first attempt failed.
func TestCoordinator_AFirstDeliveryRecordsNothing(t *testing.T) {
	a, b := twoListeners(t)

	a.finish(job{frame: deliveryFrame}, false)

	entries, err := os.ReadDir(a.dir)
	if err == nil {
		assert.Empty(t, entries, "a first delivery must not leave state behind")
	}

	assert.Equal(t, sendIt, b.beforeAttempt(deliveryFrame, 1),
		"a peer's successful first delivery must not cancel this session's retries")
}

// Ctrl+C reaches the resolved branch, so it must not end every peer's chain.
func TestCoordinator_ShutdownIsNotAnOutcome(t *testing.T) {
	a, b := twoListeners(t)
	require.Equal(t, sendIt, a.beforeAttempt(deliveryFrame, 1))
	require.Equal(t, standDown, b.beforeAttempt(deliveryFrame, 1))

	a.settle(deliveryFrame, 1, nil, context.Canceled)

	_, reported := b.awaitOutcome(context.Background(), deliveryFrame, 1, 80*time.Millisecond)
	assert.False(t, reported, "a cancellation is not a result to mirror")

	for attempt := 2; attempt <= DefaultForwardRetries; attempt++ {
		assert.Equal(t, sendIt, b.beforeAttempt(deliveryFrame, attempt),
			"attempt %d must survive the other session leaving", attempt)
	}
}

// The forwarder wraps the HTTP client's error.
func TestIsShutdown(t *testing.T) {
	assert.True(t, isShutdown(context.Canceled))
	assert.True(t, isShutdown(fmt.Errorf("post: %w", context.Canceled)))
	assert.False(t, isShutdown(nil))
	assert.False(t, isShutdown(errors.New("connection refused")))
	assert.False(t, isShutdown(context.DeadlineExceeded), "the forwarder's own timeout is a real failure")
}

// One transaction fans out to every matching webhook; every mirrored delivery needs its line.
func TestCoordinator_MirrorsEveryDeliveryOfAFanOut(t *testing.T) {
	a, b := twoListeners(t)

	const chains = 9
	frames := make([]Frame, chains)
	for i := range frames {
		id := fmt.Sprintf("evt_%02d", i+1)
		frames[i] = Frame{ID: id, Raw: []byte(`{"id":"` + id + `"}`)}
		require.Equal(t, sendIt, a.beforeAttempt(frames[i], 1), "sender claims %s", id)
		require.Equal(t, standDown, b.beforeAttempt(frames[i], 1), "peer stands down on %s", id)
		a.settle(frames[i], 1, nil, errors.New("EOF"))
	}

	var wg sync.WaitGroup
	mirrored := make([]bool, chains)
	for i := range frames {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, ok := b.awaitOutcome(context.Background(), frames[i], 1, 5*time.Second)
			mirrored[i] = ok && out.sendErr != nil
		}(i)
	}
	wg.Wait()

	for i, ok := range mirrored {
		assert.True(t, ok, "delivery %s was never mirrored, so the shells diverge", frames[i].ID)
	}
}

// kill is global, so a private counter would let #5 mean two different deliveries.
func TestCoordinator_OrdinalNamesTheDeliveryNotTheSession(t *testing.T) {
	wide, narrow := twoListeners(t)

	all := make([]Frame, 9)
	for i := range all {
		id := fmt.Sprintf("evt_%02d", i+1)
		all[i] = Frame{ID: id, Raw: []byte(`{"id":"` + id + `"}`)}
	}

	for i, fr := range all {
		assert.Equal(t, i+1, wide.ordinalFor(fr), "the sequence is dense for the session that sees everything")
	}

	assert.Equal(t, 5, narrow.ordinalFor(all[4]),
		"the narrowed session adopts the delivery's number, sparse though its listing is")

	assert.Equal(t, 5, wide.ordinalFor(all[4]))
	assert.Equal(t, 5, narrow.ordinalFor(all[4]))

	own := Frame{ID: "evt_narrow_only", Raw: []byte(`{"id":"evt_narrow_only"}`)}
	assert.Equal(t, 10, narrow.ordinalFor(own))
}

func TestCoordinator_SequenceIsPerChannelAndHandler(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	webhook := newCoordinator("webhook", "http://localhost:3000/", 7)
	otherPort := newCoordinator("webhook", "http://localhost:4000/", 7)
	otherChannel := newCoordinator("pg_ipn", "http://localhost:3000/", 7)
	require.NotNil(t, webhook)

	assert.Equal(t, 1, webhook.ordinalFor(deliveryFrame))
	assert.Equal(t, 1, otherPort.ordinalFor(deliveryFrame), "a different handler counts on its own")
	assert.Equal(t, 1, otherChannel.ordinalFor(deliveryFrame), "so does a different channel")
	assert.Equal(t, 2, webhook.ordinalFor(Frame{ID: "evt_next"}))
}

func TestCoordinator_SweepKeepsTheSequence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEPAY_CONFIG_DIR", dir)
	c := newCoordinator("webhook", "http://localhost:3000/", 7)
	require.NotNil(t, c)
	require.Equal(t, 1, c.ordinalFor(deliveryFrame))

	old := time.Now().Add(-claimStaleAge - time.Hour)
	require.NoError(t, os.Chtimes(c.seqPath(), old, old))
	require.NotNil(t, newCoordinator("webhook", "http://localhost:3000/", 7))

	assert.FileExists(t, c.seqPath(), "an old sequence is still the authority on what comes next")
	assert.Equal(t, 2, c.ordinalFor(Frame{ID: "evt_after_sweep"}), "numbering continues, it does not restart")
}

func TestRetryScheduler_LocalCountNeverCollidesWithAnAdoptedNumber(t *testing.T) {
	s, _, _ := testScheduler(t, time.Hour)
	defer s.stop()

	frame := Frame{ID: "evt", Raw: []byte(`{"a":1}`)}
	adopted, _, ok := s.arm(context.Background(), job{frame: frame}, 42)
	require.True(t, ok)
	assert.Equal(t, 42, adopted.ordinal, "the assigned number wins")

	local, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)
	assert.Equal(t, 43, local.ordinal, "a private fallback continues past what was adopted")
}

func TestClaimStaleAge_CoversTheLongestPossibleChain(t *testing.T) {
	var curve time.Duration
	for n := 1; n <= MaxForwardRetries; n++ {
		curve += retryDelay(n, false, retryUnit)
	}
	// Each attempt can also sit in the forwarder for its full client timeout.
	longest := curve + MaxForwardRetries*30*time.Second

	assert.Greater(t, claimStaleAge, longest,
		"a chain can live %v, so anything swept sooner may still be in use", longest)
}

func TestCoordinator_GiveUpLeavesAPeerTheRungsItIsOwed(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	small := newCoordinator("webhook", "http://localhost:3000/", 7)
	big := newCoordinator("webhook", "http://localhost:3000/", 10)
	require.NotNil(t, small)
	require.NotNil(t, big)

	ordinal := small.ordinalFor(deliveryFrame)
	require.NotZero(t, ordinal)
	small.armed(deliveryFrame, chainMeta{next: 1, cap: 7, due: time.Now()})
	big.armed(deliveryFrame, chainMeta{next: 1, cap: 10, due: time.Now()})

	small.exhausted(job{frame: deliveryFrame, ordinal: ordinal, attempt: 7})
	assert.Equal(t, sendIt, big.beforeAttempt(deliveryFrame, 8), "rung 8 is still the peer's to send")

	st, ok := small.stateAt(small.path(deliveryFrame))
	require.True(t, ok)
	assert.Zero(t, st.Next, "a session that stopped must not leave an armed attempt behind")
	assert.False(t, st.Done)

	big.exhausted(job{frame: deliveryFrame, ordinal: ordinal, attempt: 10})
	assert.Equal(t, chainOver, small.beforeAttempt(deliveryFrame, 11), "the largest budget ran out")
}

func TestCoordinator_GiveUpEndsTheChainOnAnAgreedBudget(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	a := newCoordinator("webhook", "http://localhost:3000/", 7)
	b := newCoordinator("webhook", "http://localhost:3000/", 7)
	require.NotNil(t, a)
	require.NotNil(t, b)

	ordinal := a.ordinalFor(deliveryFrame)
	require.NotZero(t, ordinal)
	a.armed(deliveryFrame, chainMeta{next: 1, cap: 7, due: time.Now()})
	b.armed(deliveryFrame, chainMeta{next: 1, cap: 7, due: time.Now()})

	a.exhausted(job{frame: deliveryFrame, ordinal: ordinal, attempt: 7})
	assert.Equal(t, chainOver, b.beforeAttempt(deliveryFrame, 8))
}

func TestCoordinator_GiveUpOnAFirstDeliveryRecordsNothing(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	c := newCoordinator("webhook", "http://localhost:3000/", 7)
	require.NotNil(t, c)

	c.exhausted(job{frame: deliveryFrame, attempt: 7})
	assert.Equal(t, sendIt, c.beforeAttempt(deliveryFrame, 1))
}

func TestRegistry_DoesNotFollowAChainItGaveUpOn(t *testing.T) {
	reg, _, sched, _, errOut := follower(t)
	own := reg.coord

	ordinal := own.ordinalFor(peerFrame)
	require.NotZero(t, ordinal)
	queued, _, ok := sched.arm(context.Background(), job{frame: peerFrame}, ordinal)
	require.True(t, ok)
	require.True(t, own.holdChain(peerFrame, ordinal))
	own.armed(peerFrame, chainMeta{next: 1, cap: 10, due: time.Now(), event: peerFrame.Type})
	require.Equal(t, sendIt, own.beforeAttempt(peerFrame, 1))
	own.settle(peerFrame, 1, nil, errors.New("connection refused"))

	own.exhausted(job{frame: peerFrame, ordinal: ordinal, attempt: 1})
	endChain(sched, own, queued)

	reg.scan(context.Background())
	reg.scan(context.Background())
	reg.scan(context.Background())
	assert.Empty(t, errOut.String(), "no following note and no abandonment for its own chain")
	assert.Empty(t, reg.views())
}
