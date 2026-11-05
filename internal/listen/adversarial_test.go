package listen

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The coordination files are input: user-editable, truncatable, written by every session.

// A negative ordinal could never be named or killed (parseControl rejects n < 1).
func TestAdversarial_NegativeSequenceYieldsNoOrdinal(t *testing.T) {
	c := adversarialCoordinator(t)
	require.NoError(t, os.WriteFile(c.seqPath(), []byte("-9000"), 0o600))

	n := c.ordinalFor(deliveryFrame)
	assert.GreaterOrEqual(t, n, 0, "a number the control layer cannot address is worse than none")
	if n > 0 {
		action, ordinals := parseControl(fmt.Sprintf("kill %d", n))
		assert.Equal(t, actionCancel, action, "every number handed out must be killable")
		assert.Equal(t, []int{n}, ordinals)
	}
}

func TestAdversarial_SequenceAtMaxIntDoesNotWrapNegative(t *testing.T) {
	c := adversarialCoordinator(t)
	require.NoError(t, os.WriteFile(c.seqPath(), []byte("9223372036854775807"), 0o600))

	assert.GreaterOrEqual(t, c.ordinalFor(deliveryFrame), 0, "wrapping must not produce a negative ordinal")
}

func TestAdversarial_HostileSequenceContent(t *testing.T) {
	for _, content := range []string{
		"", "   ", "abc", "1e9", "0x10", "-1", "0",
		"99999999999999999999999", "1\n2\n3", "\x00\x01", strings.Repeat("9", 500),
	} {
		t.Run(fmt.Sprintf("%q", content), func(t *testing.T) {
			c := adversarialCoordinator(t)
			require.NoError(t, os.WriteFile(c.seqPath(), []byte(content), 0o600))

			n := c.ordinalFor(deliveryFrame)
			assert.GreaterOrEqual(t, n, 0)
			if n > 0 {
				action, _ := parseControl(fmt.Sprintf("kill %d", n))
				assert.Equal(t, actionCancel, action, "handed out %d, which kill rejects", n)
			}
		})
	}
}

func TestAdversarial_HostileStoredOrdinal(t *testing.T) {
	for _, ordinal := range []int64{-1, -9223372036854775808, 0} {
		t.Run(fmt.Sprintf("%d", ordinal), func(t *testing.T) {
			c := adversarialCoordinator(t)
			require.NoError(t, os.MkdirAll(c.dir, 0o700))
			require.NoError(t, os.WriteFile(c.path(deliveryFrame),
				[]byte(fmt.Sprintf(`{"ordinal":%d}`, ordinal)), 0o600))

			n := c.ordinalFor(deliveryFrame)
			assert.Positive(t, n, "a stored ordinal below 1 must be replaced, not adopted")
		})
	}
}

// A colossal attempt would make every session stand down forever.
func TestAdversarial_AbsurdAttemptCannotDisableRetryForever(t *testing.T) {
	c := adversarialCoordinator(t)
	require.NoError(t, os.MkdirAll(c.dir, 0o700))
	require.NoError(t, os.WriteFile(c.path(deliveryFrame),
		[]byte(`{"ordinal":1,"attempt":9223372036854775807}`), 0o600))

	assert.NotEqual(t, standDown, c.beforeAttempt(deliveryFrame, 1),
		"a file may not park a delivery past every real attempt and stop it being retried")
}

func TestAdversarial_ClaimFileIsBounded(t *testing.T) {
	c := adversarialCoordinator(t)
	require.NoError(t, os.MkdirAll(c.dir, 0o700))
	huge := `{"ordinal":1,"attempt":1,"settled":true,"body":"` + strings.Repeat("A", 8<<20) + `"}`
	require.NoError(t, os.WriteFile(c.path(deliveryFrame), []byte(huge), 0o600))

	out, ok := c.awaitOutcome(context.Background(), deliveryFrame, 1, 200*time.Millisecond)
	if ok && out.res != nil {
		assert.Less(t, len(out.res.Body), 1<<20, "a mirrored body must stay bounded")
	}
}

func TestAdversarial_ControlInputIsNeverFatal(t *testing.T) {
	inputs := []string{
		"kill 99999999999999999999999", "kill -0", "kill +1", "kill 1e3", "kill 0x1",
		"kill  1  ", "kill 1,2", "kill 1;kill 2", "kill $(whoami)", "kill `id`",
		"kill ../../etc/passwd", "kill \x00 1", "kill 1\x1b[31m", "\x1b]0;pwned\x07",
		strings.Repeat("kill 1 ", 5000), strings.Repeat("đ", 100000),
		"retries\x00off", "RETRIES", "kill\tall", "kill all all", "?\x1b",
		"\r\n", "\x1b[A\x1b[B", "kill -9223372036854775808",
	}
	for _, in := range inputs {
		t.Run(fmt.Sprintf("%.24q", in), func(t *testing.T) {
			action, ordinals := parseControl(in) // must not panic
			for _, n := range ordinals {
				assert.Positive(t, n, "only positive ordinals may reach the scheduler")
			}
			if action == actionUnknown {
				hint := hintFor(in)
				assert.NotContains(t, hint, "\x1b", "the echo must not carry an escape to the terminal")
				assert.NotContains(t, hint, "\x00")
				assert.Less(t, len(hint), 400, "the echo is bounded")
			}
		})
	}
}

// Run under -race: this is the real concurrency check.
func TestAdversarial_ControlAndSchedulerUnderConcurrentLoad(t *testing.T) {
	c, sched, in, _ := testController(t)
	sched.unit = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // keep arming and resolving chains
		defer wg.Done()
		for i := 0; ctx.Err() == nil; i++ {
			j := job{frame: Frame{ID: fmt.Sprintf("evt_%d", i), Raw: []byte(`{"a":1}`)}}
			next, _, ok := sched.arm(ctx, j, 0)
			if !ok {
				continue
			}
			sched.markInFlight(next)
			if i%3 == 0 {
				sched.release(next)
			}
		}
	}()
	wg.Add(1)
	go func() { // and keep reading them back
		defer wg.Done()
		for ctx.Err() == nil {
			sched.list()
			sched.arming()
			sched.has(1)
		}
	}()

	for ctx.Err() == nil {
		in.WriteString("retries\nkill 1\nkill all\nretries off\nretries on\nkill 1 2 3\n?\nbogus\n")
		c.handle("retries")
		c.handle("kill 1")
		c.handle("kill all")
	}
	cancel()
	wg.Wait()

	sched.cancelAll()
	held, chains := sched.held()
	assert.Zero(t, chains, "every chain accounted for")
	assert.Zero(t, held, "and every byte returned")
}

func TestAdversarial_KillRacesTheMirror(t *testing.T) {
	a, b := twoListeners(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		fr := Frame{ID: fmt.Sprintf("evt_%02d", i), Raw: []byte(`{"a":1}`)}
		require.Equal(t, sendIt, a.beforeAttempt(fr, 1))

		wg.Add(3)
		go func() { defer wg.Done(); a.settle(fr, 1, nil, fmt.Errorf("EOF")) }()
		go func() { defer wg.Done(); b.awaitOutcome(ctx, fr, 1, 100*time.Millisecond) }()
		go func() { defer wg.Done(); a.finishFrame(fr, true) }()
	}
	wg.Wait()

	for i := 0; i < 40; i++ {
		fr := Frame{ID: fmt.Sprintf("evt_%02d", i), Raw: []byte(`{"a":1}`)}
		assert.Equal(t, chainOver, b.beforeAttempt(fr, 2),
			"delivery %d was killed and must stay killed", i)
	}
}

func adversarialCoordinator(t *testing.T) *coordinator {
	t.Helper()
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	c := testCoordinator(t, "webhook", "http://localhost:3000/", DefaultForwardRetries)
	require.NoError(t, os.MkdirAll(c.dir, 0o700))
	return c
}

func TestAdversarial_InterruptedMidTypingActionsNothing(t *testing.T) {
	c, sched, in, errOut := testController(t)
	queued, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	require.True(t, ok)

	in.WriteString("kill 1") // no newline: the developer is still typing
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() { c.run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled session hung on a half-typed line")
	}

	assert.True(t, sched.has(queued.ordinal), "an unterminated line must not kill anything")
	assert.NotContains(t, errOut.String(), "killed")
}

func TestAdversarial_ControlAfterSchedulerStopped(t *testing.T) {
	c, sched, in, _ := testController(t)
	for i := 0; i < 5; i++ {
		_, _, ok := sched.arm(context.Background(), job{frame: Frame{ID: fmt.Sprintf("e%d", i), Raw: []byte(`{}`)}}, 0)
		require.True(t, ok)
	}
	sched.stop()

	in.WriteString("retries\nkill 1\nkill all\nretries off\nretries on\nkill 1 2 3\n")
	c.run(context.Background()) // must not panic on a stopped scheduler

	_, _, ok := sched.arm(context.Background(), job{frame: Frame{Raw: []byte(`{}`)}}, 0)
	assert.False(t, ok, "a stopped scheduler arms nothing")
	held, chains := sched.held()
	assert.Zero(t, held)
	assert.Zero(t, chains)
}

// The id-less fallback hashes up to 1 MiB of Raw; the path must not be recomputed per mirror poll.
func TestAdversarial_HugeFrameWithoutAnID(t *testing.T) {
	c := adversarialCoordinator(t)
	huge := Frame{Raw: []byte(`{"payload":"` + strings.Repeat("x", 1<<20) + `"}`)}

	start := time.Now()
	first := c.path(huge)
	assert.NotEmpty(t, first)
	assert.Equal(t, first, c.path(huge), "the same frame names the same file")

	require.Equal(t, sendIt, c.beforeAttempt(huge, 1))
	assert.Less(t, time.Since(start), 5*time.Second, "a large frame must not stall coordination")

	assert.NotContains(t, first, "..")
	assert.True(t, strings.HasPrefix(first, c.dir), "the file stays inside the claim directory")
}

func TestAdversarial_ConcurrentAllocationNeverRepeatsANumber(t *testing.T) {
	a, b := twoListeners(t)

	const n = 60
	got := make(chan int, 2*n)
	var wg sync.WaitGroup
	for _, c := range []*coordinator{a, b} {
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(c *coordinator) { defer wg.Done(); got <- c.allocate() }(c)
		}
	}
	wg.Wait()
	close(got)

	seen := map[int]bool{}
	for v := range got {
		if v == 0 {
			continue // refused under contention; the caller falls back to its own count
		}
		assert.False(t, seen[v], "number %d was handed out twice", v)
		assert.Positive(t, v)
		seen[v] = true
	}
	assert.NotEmpty(t, seen, "some allocations must have succeeded")
}
