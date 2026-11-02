package listen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/forward"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
)

// DefaultForwardRetries matches the number of retries SePay's own workers make.
const DefaultForwardRetries = 7

// MaxForwardRetries caps --forward-retries: ten retries already span close to four hours.
const MaxForwardRetries = 10

// retryUnit is the fibonacci step; tests shrink it.
const retryUnit = time.Minute

// retryMaxBytes caps payload bytes held by armed retries; an entry count alone would not
// bound memory with frames up to 1 MiB held three ways.
const retryMaxBytes = 24 << 20

// retryMaxEntries bounds live chains, and so the goroutines their timer callbacks park.
const retryMaxEntries = 500

// retryDelay returns fib(n) units before retry n (1-based); firstImmediate makes retry 1 due at once.
func retryDelay(n int, firstImmediate bool, unit time.Duration) time.Duration {
	if n == 1 && firstImmediate {
		return 0
	}
	return time.Duration(fib(n)) * unit
}

func fib(n int) int {
	a, b := 0, 1
	for i := 0; i < n; i++ {
		a, b = b, a+b
	}
	return a
}

// shouldRetryForward always retries a transport failure; a non-2xx only when the channel opts in.
func shouldRetryForward(src Source, fr Frame, res *forward.Result, err error) bool {
	if err != nil {
		// Ctrl+C is not the handler's fault.
		return !isShutdown(err)
	}
	if res.OK() || src.RetryNon2xx == nil {
		return false
	}
	return src.RetryNon2xx(fr)
}

// isShutdown reports this session ending; it is never retried nor reported to peers, or it would end their chains.
func isShutdown(err error) bool { return errors.Is(err, context.Canceled) }

// retryDisabledReason returns "" when retry is on. Production is excluded because SePay already
// re-sends; a non-loopback target because the CLI cannot vouch for amplifying at it.
func retryDisabledReason(f *cmdutil.Factory, fwd *forward.Forwarder) string {
	if f.ActiveEnv() == config.EnvProduction {
		return "retry is sandbox-only"
	}
	if !fwd.IsLoopback() {
		return "retry needs a loopback handler"
	}
	return ""
}

// RetryEnabled reports whether the flag, environment and target all allow local retry.
func RetryEnabled(f *cmdutil.Factory, opts Options) bool {
	if opts.ForwardRetries <= 0 {
		return false
	}
	fwd, err := forward.NewForwarder(opts.Forward, opts.SkipTLS)
	if err != nil {
		return false
	}
	return retryDisabledReason(f, fwd) == ""
}

// chain is one delivery's live retry chain; its presence in chains holds the budget, so a double release is a no-op.
type chain struct {
	ordinal int
	size    int
	attempt int
	due     time.Time
	frame   Frame
	// nil once the attempt is queued
	timer *time.Timer
	// past this a kill cannot stop the attempt
	inFlight bool
}

// retryScheduler re-sends a failed forward on the platform's curve, in memory for the session only.
type retryScheduler struct {
	unit           time.Duration
	firstImmediate bool
	maxBytes       int
	maxEntries     int
	out            chan<- job
	errOut         io.Writer

	mu          sync.Mutex
	chains      map[int]*chain
	lastOrdinal int
	bytes       int
	refused     int
	armingOff   bool
	stopped     bool
}

func newRetryScheduler(out chan<- job, errOut io.Writer, firstImmediate bool) *retryScheduler {
	return &retryScheduler{
		unit:           retryUnit,
		firstImmediate: firstImmediate,
		maxBytes:       retryMaxBytes,
		maxEntries:     retryMaxEntries,
		out:            out,
		errOut:         errOut,
		chains:         map[int]*chain{},
	}
}

// arm schedules the attempt after j; ok is false when the cap refuses it, the session stopped,
// or the chain was cancelled meanwhile. Arming only after the previous attempt resolved means a
// delivery is never queued twice. assigned is the shared ordinal, or 0 for the local count.
func (s *retryScheduler) arm(ctx context.Context, j job, assigned int) (next job, wait time.Duration, ok bool) {
	size := frameSize(j.frame)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return job{}, 0, false
	}

	c := s.chains[j.ordinal]
	if j.ordinal == 0 {
		if s.armingOff {
			return job{}, 0, false
		}
		if s.bytes+size > s.maxBytes || len(s.chains) >= s.maxEntries {
			s.refused++
			if s.refused == 1 {
				// Not an iostreams Block: waiting for the screen under s.mu would invert the lock order.
				fmt.Fprintf(s.errOut,
					"warning: retry buffer full (%d MiB or %d events); further failed events are not retried\n",
					s.maxBytes>>20, s.maxEntries)
			}
			return job{}, 0, false
		}
		// A shared number can collide with a private one; overwriting a live chain would orphan its timer.
		ordinal := assigned
		if ordinal == 0 || s.chains[ordinal] != nil {
			ordinal = s.lastOrdinal + 1
		}
		if ordinal > s.lastOrdinal {
			s.lastOrdinal = ordinal
		}
		c = &chain{ordinal: ordinal, size: size, frame: j.frame}
		s.chains[c.ordinal] = c
		s.bytes += size
	} else if c == nil {
		// Cancelled in flight: its budget already went back.
		return job{}, 0, false
	}

	next = job{frame: j.frame, attempt: j.attempt + 1, ordinal: c.ordinal}
	wait = retryDelay(next.attempt, s.firstImmediate, s.unit)
	c.attempt, c.due, c.inFlight = next.attempt, time.Now().Add(wait), false
	c.timer = time.AfterFunc(wait, func() { s.fire(ctx, next) })
	return next, wait, true
}

func (s *retryScheduler) fire(ctx context.Context, queued job) {
	s.mu.Lock()
	c := s.chains[queued.ordinal]
	if s.stopped || c == nil {
		s.mu.Unlock()
		return
	}
	c.timer = nil
	s.mu.Unlock()

	select {
	case s.out <- queued:
	case <-ctx.Done():
	}
}

// release drops an ended chain's hold; a never-charged job and a second release are no-ops.
func (s *retryScheduler) release(j job) {
	if j.ordinal == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropLocked(j.ordinal)
}

// dropLocked ends one chain and returns it, or nil. Caller holds s.mu.
func (s *retryScheduler) dropLocked(ordinal int) *chain {
	c := s.chains[ordinal]
	if c == nil {
		return nil
	}
	if c.timer != nil {
		c.timer.Stop()
	}
	delete(s.chains, ordinal)
	s.bytes -= c.size
	return c
}

// alive reports whether j's chain is still armed; a job that outlived its chain is dropped.
func (s *retryScheduler) alive(j job) bool {
	if j.ordinal == 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chains[j.ordinal] != nil
}

// markInFlight is separate from alive: a session that dequeues a job may still stand down.
func (s *retryScheduler) markInFlight(j job) {
	if j.ordinal == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.chains[j.ordinal]; c != nil {
		c.inFlight = true
	}
}

// has reports whether a chain is armed here without claiming it.
func (s *retryScheduler) has(ordinal int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chains[ordinal] != nil
}

// killResult carries the frames out so peers are told outside the scheduler's mutex.
type killResult struct {
	dropped []int
	sending int
	frames  []Frame
}

// cancel ends the named chains; one already on the wire runs to completion.
func (s *retryScheduler) cancel(ordinals []int) killResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res killResult
	for _, n := range ordinals {
		s.collectLocked(&res, n)
	}
	return res
}

// cancelAll ends every live chain.
func (s *retryScheduler) cancelAll() killResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var res killResult
	for ordinal := range s.chains {
		s.collectLocked(&res, ordinal)
	}
	return res
}

// collectLocked ends one chain and records it in res. Caller holds s.mu.
func (s *retryScheduler) collectLocked(res *killResult, ordinal int) {
	c := s.dropLocked(ordinal)
	if c == nil {
		return
	}
	res.dropped = append(res.dropped, ordinal)
	res.frames = append(res.frames, c.frame)
	if c.inFlight {
		res.sending++
	}
}

// setArming toggles arming of new chains; armed chains keep their schedule.
func (s *retryScheduler) setArming(on bool) (changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed = s.armingOff == on
	s.armingOff = !on
	return changed
}

func (s *retryScheduler) arming() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.armingOff
}

// chainView is one listing row. A peer's row carries event, tag and cap instead of a frame.
type chainView struct {
	ordinal int
	attempt int
	next    time.Duration
	sending bool
	frame   Frame

	peer  bool
	event string
	tag   string
	cap   int
}

func (s *retryScheduler) list() []chainView {
	s.mu.Lock()
	defer s.mu.Unlock()
	views := make([]chainView, 0, len(s.chains))
	for _, c := range s.chains {
		views = append(views, chainView{
			ordinal: c.ordinal,
			attempt: c.attempt,
			// Clamped: an overdue chain would otherwise read "in ~-1m30s".
			next:    max(0, time.Until(c.due).Round(time.Second)),
			sending: c.timer == nil,
			frame:   c.frame,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ordinal < views[j].ordinal })
	return views
}

// stop cancels every armed retry and returns how many chains were abandoned.
func (s *retryScheduler) stop() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	for _, c := range s.chains {
		if c.timer != nil {
			c.timer.Stop()
		}
	}
	abandoned := len(s.chains)
	s.chains = nil
	s.bytes = 0
	return abandoned
}

// endChain ends j's chain here and releases this session's marker; both, or a follower
// reads a finished delivery as still held.
func endChain(s *retryScheduler, c *coordinator, j job) {
	s.release(j)
	c.releaseChain(j.ordinal)
}

func frameSize(fr Frame) int {
	return len(fr.Raw) + len(fr.RawBody) + len(fr.Payload)
}

// printRetryNote writes to the parent line's stream so a pipe never splits one event.
func printRetryNote(f *cmdutil.Factory, err error, text string) {
	if f.Global.Quiet {
		return
	}
	f.IOStreams.Block(func() {
		fmt.Fprintf(deliveryStream(f.IOStreams, err), "    %s\n", f.IOStreams.Dim(text))
	})
}

// waitLabel renders "1m", not "1m0s".
func waitLabel(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}

// retryWhen is approximate: one forwarder with a 30s timeout can push a due attempt late.
func retryWhen(d time.Duration) string {
	if d == 0 {
		return "now"
	}
	return "in ~" + waitLabel(d)
}
