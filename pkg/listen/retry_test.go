package listen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/forward"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// get_fibonacci(n) minutes before retry n, for n = 1..8.
func TestRetryDelay_MatchesThePlatformCurve(t *testing.T) {
	want := []int{1, 1, 2, 3, 5, 8, 13, 21}
	for i, minutes := range want {
		n := i + 1
		t.Run(fmt.Sprintf("retry%d", n), func(t *testing.T) {
			assert.Equal(t, time.Duration(minutes)*time.Minute, retryDelay(n, false, time.Minute))
		})
	}
}

func TestRetryDelay_CumulativeWindow(t *testing.T) {
	var total time.Duration
	for n := 1; n <= DefaultForwardRetries; n++ {
		total += retryDelay(n, false, time.Minute)
	}
	assert.Equal(t, 33*time.Minute, total)
}

var retryOn = Source{RetryNon2xx: func(Frame) bool { return true }}

func TestShouldRetryForward(t *testing.T) {
	cases := []struct {
		name string
		src  Source
		res  *forward.Result
		err  error
		want bool
	}{
		{"a 2xx ends the delivery", Source{}, &forward.Result{StatusCode: 200}, nil, false},
		{"a 2xx ends it even where non-2xx would retry", retryOn, &forward.Result{StatusCode: 204}, nil, false},
		{"a non-2xx does not retry on a transport-only channel", Source{}, &forward.Result{StatusCode: 500}, nil, false},
		{"a non-2xx retries where the webhook asks for it", retryOn, &forward.Result{StatusCode: 500}, nil, true},
		{"a 4xx counts the same as a 5xx", retryOn, &forward.Result{StatusCode: 404}, nil, true},
		{"ctrl+c mid-forward is a shutdown", Source{}, nil, context.Canceled, false},
		{"wrapped cancel is still a shutdown", Source{}, nil, fmt.Errorf("post: %w", context.Canceled), false},
		{"the forwarder's own timeout is a transport failure", Source{}, nil, context.DeadlineExceeded, true},
		{"connection refused retries on every channel", Source{}, nil, &url.Error{Op: "Post", Err: os.ErrDeadlineExceeded}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldRetryForward(tc.src, Frame{}, tc.res, tc.err))
		})
	}
}

func TestShouldRetryForward_IsPerFrame(t *testing.T) {
	src := Source{RetryNon2xx: func(fr Frame) bool { return fr.ID == "from_wh_a" }}
	res := &forward.Result{StatusCode: 500}

	assert.True(t, shouldRetryForward(src, Frame{ID: "from_wh_a"}, res, nil))
	assert.False(t, shouldRetryForward(src, Frame{ID: "from_wh_b"}, res, nil))
}

func TestRetryDelay_FirstImmediateOnlyShiftsTheFirst(t *testing.T) {
	assert.Zero(t, retryDelay(1, true, time.Minute))
	assert.Equal(t, time.Minute, retryDelay(1, false, time.Minute))

	for n := 2; n <= DefaultForwardRetries; n++ {
		assert.Equal(t, retryDelay(n, false, time.Minute), retryDelay(n, true, time.Minute),
			"attempt %d is on the curve either way", n)
	}
}

func TestRetryWhen(t *testing.T) {
	assert.Equal(t, "now", retryWhen(0))
	assert.Equal(t, "in ~1m", retryWhen(time.Minute))
	assert.Equal(t, "in ~13m", retryWhen(13*time.Minute))
}

func TestNextJob_ServesBothQueues(t *testing.T) {
	jobs := make(chan job, 1)
	retries := make(chan job, 1)
	jobs <- job{frame: Frame{ID: "fresh"}}
	retries <- job{frame: Frame{ID: "old"}, attempt: 1}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		got, ok := nextJob(context.Background(), jobs, retries)
		require.True(t, ok)
		seen[got.frame.ID] = true
	}
	assert.Equal(t, map[string]bool{"fresh": true, "old": true}, seen,
		"a ready retry must be served, whatever order the two arrive in")
}

func TestNextJob_ARetryIsServedUnderSustainedFreshTraffic(t *testing.T) {
	jobs := make(chan job, 4)
	retries := make(chan job, 1)
	retries <- job{frame: Frame{ID: "old"}, attempt: 1}

	for i := 0; i < 200; i++ {
		for len(jobs) < cap(jobs) {
			jobs <- job{frame: Frame{ID: "fresh"}}
		}
		got, ok := nextJob(context.Background(), jobs, retries)
		require.True(t, ok)
		if got.frame.ID == "old" {
			return
		}
	}
	t.Fatal("the retry was never served while fresh deliveries kept arriving")
}

func TestNextJob_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok := nextJob(ctx, make(chan job), nil)
	assert.False(t, ok)
}

func TestNextJob_NilRetryChannelIsNeverReady(t *testing.T) {
	jobs := make(chan job, 1)
	jobs <- job{frame: Frame{ID: "fresh"}}
	got, ok := nextJob(context.Background(), jobs, nil)
	require.True(t, ok)
	assert.Equal(t, "fresh", got.frame.ID)
}

func testScheduler(t *testing.T, unit time.Duration) (*retryScheduler, chan job, *bytes.Buffer) {
	t.Helper()
	out := make(chan job, 16)
	errOut := &bytes.Buffer{}
	s := newRetryScheduler(out, errOut, false)
	s.unit = unit
	return s, out, errOut
}

func (s *retryScheduler) held() (bytes, chains int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes, len(s.chains)
}

func TestRetryScheduler_WalksTheWholeCurve(t *testing.T) {
	const unit = 2 * time.Millisecond
	s, out, _ := testScheduler(t, unit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	j := job{frame: Frame{ID: "evt_1", Raw: []byte(`{}`)}}
	for want := 1; want <= DefaultForwardRetries; want++ {
		armedAt := time.Now()
		next, wait, ok := s.arm(ctx, j, 0)
		require.True(t, ok)
		require.Equal(t, want, next.attempt)
		require.Equal(t, 1, next.ordinal, "every attempt belongs to the same chain")
		require.Equal(t, retryDelay(want, false, unit), wait)

		select {
		case fired := <-out:
			assert.Equal(t, want, fired.attempt)
			assert.Equal(t, "evt_1", fired.frame.ID)
			assert.GreaterOrEqual(t, time.Since(armedAt), wait,
				"an attempt must not fire before its fibonacci step")
			j = fired
		case <-time.After(3 * time.Second):
			t.Fatalf("retry %d never fired", want)
		}
	}
	assert.Equal(t, 1, s.stop(), "one delivery, charged once across all seven attempts")
}

func TestRetryScheduler_RefusesPastTheByteCap(t *testing.T) {
	s, _, errOut := testScheduler(t, time.Hour) // long enough that nothing fires
	s.maxBytes = 100
	defer s.stop()

	big := job{frame: Frame{Raw: bytes.Repeat([]byte("x"), 60)}}
	_, _, ok := s.arm(context.Background(), big, 0)
	require.True(t, ok, "the first delivery fits")

	_, _, ok = s.arm(context.Background(), big, 0)
	assert.False(t, ok, "the second would exceed the cap")
	_, _, ok = s.arm(context.Background(), big, 0)
	assert.False(t, ok)

	assert.Equal(t, 1, strings.Count(errOut.String(), "retry buffer full"),
		"the cap says so once, not once per refused event")
}

func TestFrameSize_CountsEveryCopy(t *testing.T) {
	fr := Frame{Raw: []byte("aaaa"), RawBody: "bb", Payload: []byte("ccc")}
	assert.Equal(t, 9, frameSize(fr))
}

func TestRetryScheduler_StopCancelsAndReportsPending(t *testing.T) {
	s, out, _ := testScheduler(t, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 3; i++ {
		_, _, ok := s.arm(ctx, job{frame: Frame{ID: fmt.Sprintf("evt_%d", i), Raw: []byte(`{}`)}}, 0)
		require.True(t, ok)
	}

	assert.Equal(t, 3, s.stop())

	select {
	case fired := <-out:
		t.Fatalf("a stopped scheduler still fired %s", fired.frame.ID)
	case <-time.After(100 * time.Millisecond):
	}
	assert.Zero(t, s.stop(), "stop is idempotent")
}

func TestRetryScheduler_ArmAfterStopIsRefused(t *testing.T) {
	s, _, _ := testScheduler(t, time.Millisecond)
	s.stop()
	_, _, ok := s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	assert.False(t, ok)
}

func TestRetryScheduler_CancelReleasesExactlyOnce(t *testing.T) {
	s, out, _ := testScheduler(t, time.Hour) // long enough that nothing fires
	defer s.stop()

	frame := Frame{ID: "evt_1", Raw: []byte(`{"a":1}`)}
	queued, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)

	bytesHeld, chains := s.held()
	require.Equal(t, frameSize(frame), bytesHeld)
	require.Equal(t, 1, chains)

	res := s.cancel([]int{queued.ordinal})
	dropped, sending := res.dropped, res.sending
	assert.Equal(t, []int{queued.ordinal}, dropped)
	assert.Zero(t, sending, "a chain still on its timer has nothing on the wire")

	bytesHeld, chains = s.held()
	assert.Zero(t, bytesHeld, "cancelling returns the budget at once, not when the timer would have fired")
	assert.Zero(t, chains)

	s.release(queued)
	bytesHeld, chains = s.held()
	assert.Zero(t, bytesHeld, "a release after the cancel must not refund a second time")
	assert.Zero(t, chains)

	assert.Zero(t, s.stop(), "a cancelled chain is not one the session abandoned")
	select {
	case fired := <-out:
		t.Fatalf("a cancelled chain still queued %s", fired.frame.ID)
	default:
	}
}

// Once fired the attempt is a value in a channel no timer surgery can reach; the books must still balance once.
func TestRetryScheduler_CancelAfterTimerFired(t *testing.T) {
	s, out, _ := testScheduler(t, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.stop()

	frame := Frame{ID: "evt_1", Raw: []byte(`{"a":1}`)}
	_, _, ok := s.arm(ctx, job{frame: frame}, 0)
	require.True(t, ok)

	fired := <-out

	res := s.cancel([]int{fired.ordinal})
	dropped, sending := res.dropped, res.sending
	require.Equal(t, []int{fired.ordinal}, dropped)
	assert.Zero(t, sending, "nothing had claimed it, so no request is on the wire")
	assert.False(t, s.alive(fired), "the forwarder must drop it rather than send it")

	bytesHeld, chains := s.held()
	require.Zero(t, bytesHeld)
	require.Zero(t, chains)

	s.release(fired)
	_, _, ok = s.arm(ctx, fired, 0)
	assert.False(t, ok, "a cancelled chain must not arm itself back to life")

	bytesHeld, chains = s.held()
	assert.Zero(t, bytesHeld, "the budget came back once, not twice")
	assert.Zero(t, chains)
	assert.Zero(t, s.stop())
}

func TestRetryScheduler_CancelAfterTheForwarderClaimedIt(t *testing.T) {
	s, out, _ := testScheduler(t, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.stop()

	frame := Frame{ID: "evt_1", Raw: []byte(`{"a":1}`)}
	_, _, ok := s.arm(ctx, job{frame: frame}, 0)
	require.True(t, ok)

	fired := <-out
	s.markInFlight(fired) // the forwarder is now sending it

	res := s.cancel([]int{fired.ordinal})
	dropped, sending := res.dropped, res.sending
	require.Equal(t, []int{fired.ordinal}, dropped)
	assert.Equal(t, 1, sending, "the request was already on the wire")

	s.release(fired)
	_, _, ok = s.arm(ctx, fired, 0)
	assert.False(t, ok, "cancel promises no further attempt")

	bytesHeld, chains := s.held()
	assert.Zero(t, bytesHeld, "the budget came back once, not twice")
	assert.Zero(t, chains)
	assert.Zero(t, s.stop())
}

func TestRetryScheduler_ArmingClearsTheClaim(t *testing.T) {
	s, out, _ := testScheduler(t, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.stop()

	frame := Frame{ID: "evt_1", Raw: []byte(`{"a":1}`)}
	_, _, ok := s.arm(ctx, job{frame: frame}, 0)
	require.True(t, ok)

	fired := <-out
	s.markInFlight(fired)
	next, _, ok := s.arm(ctx, fired, 0)
	require.True(t, ok)

	sending := s.cancel([]int{next.ordinal}).sending
	assert.Zero(t, sending, "the next attempt is back on the timer, not on the wire")
}

func TestRetryScheduler_CancelOneLeavesSiblings(t *testing.T) {
	s, _, _ := testScheduler(t, time.Hour)
	defer s.stop()

	frame := Frame{ID: "evt", Raw: []byte(`{"a":1}`)}
	first, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)
	second, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)
	require.NotEqual(t, first.ordinal, second.ordinal, "each chain gets its own number")

	dropped := s.cancel([]int{first.ordinal}).dropped
	require.Equal(t, []int{first.ordinal}, dropped)

	assert.False(t, s.has(first.ordinal))
	assert.True(t, s.has(second.ordinal), "cancelling one chain must not disturb another")

	bytesHeld, chains := s.held()
	assert.Equal(t, frameSize(frame), bytesHeld, "only the cancelled chain's bytes came back")
	assert.Equal(t, 1, chains)

	dropped = s.cancel([]int{first.ordinal, 99}).dropped
	assert.Empty(t, dropped)
	assert.Equal(t, 1, s.stop())
}

func TestRetryScheduler_OrdinalsAreNeverReused(t *testing.T) {
	s, _, _ := testScheduler(t, time.Hour)
	defer s.stop()

	frame := Frame{ID: "evt", Raw: []byte(`{"a":1}`)}
	first, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)
	second, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)
	require.Equal(t, []int{1, 2}, []int{first.ordinal, second.ordinal})

	s.release(first)
	s.cancel([]int{second.ordinal})
	held, chains := s.held()
	require.Zero(t, held)
	require.Zero(t, chains, "nothing is armed, so #1 and #2 are free numbers")

	third, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)
	assert.Equal(t, 3, third.ordinal, "an empty scheduler still counts on, it does not restart at 1")

	dropped := s.cancel([]int{first.ordinal, second.ordinal}).dropped
	assert.Empty(t, dropped)
	assert.True(t, s.has(third.ordinal), "the live chain kept its own number")
}

func TestRetryScheduler_ArmingOffStopsNewChains(t *testing.T) {
	s, _, _ := testScheduler(t, time.Hour)
	defer s.stop()

	existing, _, ok := s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)
	require.True(t, s.arming())

	s.setArming(false)
	assert.False(t, s.arming())

	_, _, ok = s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	assert.False(t, ok, "a new failure must not arm while arming is off")

	_, _, ok = s.arm(context.Background(), existing, 0)
	assert.True(t, ok, "a chain already running keeps its schedule")

	s.setArming(true)
	_, _, ok = s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	assert.True(t, ok, "retries on resumes arming")
}

func TestRetryScheduler_CapNotWidened(t *testing.T) {
	s, _, _ := testScheduler(t, time.Hour)
	s.maxEntries = 2
	defer s.stop()

	for round := 0; round < 5; round++ {
		armed := make([]int, 0, s.maxEntries)
		for i := 0; i < s.maxEntries; i++ {
			queued, _, ok := s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
			require.True(t, ok, "round %d, delivery %d", round, i)
			armed = append(armed, queued.ordinal)
		}
		_, _, ok := s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
		require.False(t, ok, "round %d: the entry cap must still bind", round)

		dropped := s.cancel(armed).dropped
		require.Len(t, dropped, s.maxEntries)

		bytesHeld, chains := s.held()
		require.Zero(t, bytesHeld, "round %d left the byte count at %d", round, bytesHeld)
		require.Zero(t, chains)
	}
	assert.Zero(t, s.stop())
}

func TestRetryDisabledReason(t *testing.T) {
	loopback, err := forward.NewForwarder("localhost:3000", false)
	require.NoError(t, err)
	remote, err := forward.NewForwarder("https://handler.example.com", false)
	require.NoError(t, err)

	factory := func(env string) *cmdutil.Factory {
		ios, _, _, _ := iostreams.Test()
		return &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Env: env, Output: "table"}}
	}

	assert.Empty(t, retryDisabledReason(factory("sandbox"), loopback))
	assert.Equal(t, "retry is sandbox-only", retryDisabledReason(factory("production"), loopback),
		"on production SePay's own worker is already re-sending the same delivery")
	assert.Equal(t, "retry needs a loopback handler", retryDisabledReason(factory("sandbox"), remote),
		"retrying off-machine turns one failed delivery into eight requests elsewhere")
	assert.Equal(t, "retry is sandbox-only", retryDisabledReason(factory("production"), remote),
		"the env guard is checked first, so it stays true if the loopback rule is relaxed")
}

func TestWaitLabel(t *testing.T) {
	cases := map[time.Duration]string{
		time.Minute:          "1m",
		13 * time.Minute:     "13m",
		90 * time.Second:     "1m30s",
		2 * time.Millisecond: "2ms",
	}
	for d, want := range cases {
		assert.Equal(t, want, waitLabel(d), d.String())
	}
}

func TestPrintRetryNote_FollowsItsDeliveryStream(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	printRetryNote(f, nil, "retry 1/7 in ~1m")
	assert.Contains(t, out.String(), "retry 1/7 in ~1m")
	assert.Empty(t, errOut.String())

	printRetryNote(f, context.DeadlineExceeded, "gave up after 7 retries")
	assert.Contains(t, errOut.String(), "gave up after 7 retries")
}

func TestPrintRetryNote_QuietDropsIt(t *testing.T) {
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table", Quiet: true}}

	printRetryNote(f, nil, "retry 1/7 in ~1m")
	assert.Empty(t, out.String())
	assert.Empty(t, errOut.String())
}

// Goes through the real forwardEvent, shouldRetryForward, arm loop.
func TestRetryChain_ReplaysVerbatimAndStopsOnRecovery(t *testing.T) {
	const failures = 3
	type attemptRecord struct {
		body   string
		header http.Header
	}
	got := make(chan attemptRecord, 16)
	var seen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- attemptRecord{body: string(body), header: r.Header.Clone()}
		seen++
		if seen <= failures {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fwd, err := forward.NewForwarder(strings.TrimPrefix(srv.URL, "http://"), false)
	require.NoError(t, err)
	ios, _, _, _ := iostreams.Test()
	ios.Out = io.Discard
	ios.ErrOut = io.Discard
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	src := Source{RetryNon2xx: func(Frame) bool { return true }}
	frame, err := Decode([]byte(`{"id":"evt_1","type":"payment_in",` +
		`"headers":{"X-SePay-Signature":"sha256=abc","X-SePay-Timestamp":"1700000000","X-Secret-Key":"s3cr3t"},` +
		`"payload":{"transferAmount":100000},"raw_body":"{\"transferAmount\":100000}"}`))
	require.NoError(t, err)

	s, retries, _ := testScheduler(t, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.stop()

	j := job{frame: frame}
	for attempts := 0; attempts < failures+1; attempts++ {
		res, sendErr := forwardEvent(ctx, j, fwd, src, Options{ForwardRetries: DefaultForwardRetries}, f)
		if !shouldRetryForward(src, j.frame, res, sendErr) {
			s.release(j)
			break
		}
		_, _, armed := s.arm(ctx, j, 0)
		require.True(t, armed)
		select {
		case j = <-retries:
		case <-time.After(3 * time.Second):
			t.Fatal("armed retry never fired")
		}
	}

	close(got)
	var records []attemptRecord
	for r := range got {
		records = append(records, r)
	}
	require.Len(t, records, failures+1,
		"the chain must stop at the first 2xx, not keep re-arming")

	first := records[0]
	assert.Equal(t, `{"transferAmount":100000}`, first.body, "the signed raw_body is what goes out")
	for i, r := range records[1:] {
		assert.Equal(t, first.body, r.body, "retry %d changed the body", i+1)
		for _, h := range []string{"X-Sepay-Signature", "X-Sepay-Timestamp", "X-Secret-Key", "Content-Type"} {
			assert.Equal(t, first.header.Get(h), r.header.Get(h),
				"retry %d changed %s — SePay re-signs, the CLI must replay", i+1, h)
		}
	}
	assert.Equal(t, "1700000000", first.header.Get("X-Sepay-Timestamp"),
		"the original timestamp is replayed, which is why a freshness window rejects a late attempt")
	assert.Zero(t, s.stop(), "a resolved chain leaves nothing pending")
}

func TestRetryScheduler_HoldsTheBudgetUntilTheChainResolves(t *testing.T) {
	s, retries, _ := testScheduler(t, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frame := Frame{ID: "evt_1", Raw: []byte(`{"a":1}`)}
	_, _, ok := s.arm(ctx, job{frame: frame}, 0)
	require.True(t, ok)

	fired := <-retries
	require.Equal(t, 1, fired.attempt)

	bytesHeld, pending := s.held()
	assert.Equal(t, frameSize(frame), bytesHeld, "a fired-but-unresolved retry still holds its bytes")
	assert.Equal(t, 1, pending, "and still counts as abandonable")

	_, _, ok = s.arm(ctx, fired, 0)
	require.True(t, ok)
	bytesHeld, pending = s.held()
	assert.Equal(t, frameSize(frame), bytesHeld, "a later attempt of the same delivery is already charged")
	assert.Equal(t, 1, pending)

	next := <-retries
	s.release(next)
	bytesHeld, pending = s.held()
	assert.Zero(t, bytesHeld, "release returns the budget")
	assert.Zero(t, pending)
	assert.Zero(t, s.stop())
}

func TestRetryScheduler_RefusesPastTheEntryCap(t *testing.T) {
	s, _, errOut := testScheduler(t, time.Hour) // long enough that nothing fires
	s.maxEntries = 2
	defer s.stop()

	var first job
	for i := 0; i < 2; i++ {
		queued, _, ok := s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
		require.True(t, ok, "delivery %d fits", i)
		if i == 0 {
			first = queued
		}
	}
	_, _, ok := s.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	assert.False(t, ok, "the third exceeds the entry cap")
	assert.Contains(t, errOut.String(), "2 events")

	_, _, ok = s.arm(context.Background(), first, 0)
	assert.True(t, ok, "the cap counts deliveries, not attempts")
}

func TestRetryScheduler_AnAssignedNumberNeverClobbersALiveChain(t *testing.T) {
	s, out, _ := testScheduler(t, time.Hour)
	defer s.stop()

	frame := Frame{ID: "evt", Raw: []byte(`{"a":1}`)}
	first, _, ok := s.arm(context.Background(), job{frame: frame}, 0)
	require.True(t, ok)
	require.Equal(t, 1, first.ordinal)

	second, _, ok := s.arm(context.Background(), job{frame: frame}, 1)
	require.True(t, ok)
	assert.NotEqual(t, first.ordinal, second.ordinal,
		"a taken number must give way rather than overwrite the chain holding it")

	held, chains := s.held()
	assert.Equal(t, 2*frameSize(frame), held, "both chains are charged")
	assert.Equal(t, 2, chains, "and both are reachable")

	res := s.cancel([]int{first.ordinal, second.ordinal})
	assert.Len(t, res.dropped, 2, "neither chain was lost")
	held, chains = s.held()
	assert.Zero(t, held, "no bytes leaked")
	assert.Zero(t, chains)
	assert.Zero(t, s.stop())

	select {
	case fired := <-out:
		t.Fatalf("an orphaned timer queued %s after both chains were killed", fired.frame.ID)
	default:
	}
}

func TestRetryScheduler_InFlightMeansSendingNotDequeued(t *testing.T) {
	s, ch, _ := testScheduler(t, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer s.stop()

	frame := Frame{ID: "evt", Raw: []byte(`{"a":1}`)}
	_, _, ok := s.arm(ctx, job{frame: frame}, 0)
	require.True(t, ok)
	fired := <-ch

	require.True(t, s.alive(fired), "the chain is live, so the job is not dropped")
	_, sending := func() ([]int, int) { r := s.cancel([]int{fired.ordinal}); return r.dropped, r.sending }()
	assert.Zero(t, sending, "picked off the queue is not yet sent")
}
