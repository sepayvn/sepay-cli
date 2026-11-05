package listen

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
)

// registryPoll: a retry step is at least a minute, so seconds of lag cost nothing.
const registryPoll = 2 * time.Second

// orphanConfirmations: a fresh holder can be turned away for at most a few hundred ms, so two
// ticks apart cannot both misread a live chain as abandoned.
const orphanConfirmations = 2

// maxScanEntries bounds one scan; states are swept by age, not on exit.
const maxScanEntries = 4 * retryMaxEntries

// claimSweepEvery keeps a long-lived session from growing the directory every scan reads.
const claimSweepEvery = 3 * time.Minute

// peerChain is a chain another session holds; no frame, since a follower never sends.
type peerChain struct {
	ordinal int
	path    string
	event   string
	tag     string
	cap     int
	attempt int
	next    int
	due     time.Time
	// highest attempt whose outcome was shown
	printed int
	// highest armed attempt announced
	notedNext int
	// consecutive ticks found unheld
	unheld int
}

// registry follows chains other sessions on this channel and handler hold, so this session
// can list, kill and report them. It reads only ids, counts and outcomes, and never sends:
// a chain whose holder is gone is reported as abandoned.
type registry struct {
	f       *cmdutil.Factory
	coord   *coordinator
	sched   *retryScheduler
	target  string
	channel string

	// nil until the control layer is built
	control *controller

	mu     sync.Mutex
	chains map[int]*peerChain
	noted  bool
}

// newRegistry returns nil without coordination or a scheduler. It runs with or without a TTY:
// an unattended chain stops retrying unnoticed in a log-only session.
func newRegistry(f *cmdutil.Factory, coord *coordinator, sched *retryScheduler, target string) *registry {
	if coord == nil || sched == nil {
		return nil
	}
	return &registry{
		f: f, coord: coord, sched: sched, target: target,
		channel: coord.channelID(),
		chains:  map[int]*peerChain{},
	}
}

// mirrors gates reprinting a peer's delivery: stdout of a piped or --json run carries only
// deliveries this session forwarded.
func (r *registry) mirrors() bool { return r.f.CanPrompt() }

func (r *registry) run(ctx context.Context) {
	if r == nil {
		return
	}
	t := time.NewTicker(registryPoll)
	defer t.Stop()
	lastSweep := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.scan(ctx)
			if time.Since(lastSweep) >= claimSweepEvery {
				r.coord.sweep()
				lastSweep = time.Now()
			}
		}
	}
}

// scan prints only after gathering state, never under r.mu.
func (r *registry) scan(ctx context.Context) {
	if r == nil {
		return
	}
	// Keyed on the file: a read that missed the lock still names a live delivery.
	seen := map[string]bool{}
	for _, path := range r.coord.claimPaths(maxScanEntries) {
		if ctx.Err() != nil {
			return
		}
		seen[path] = true
		if r.coord.holdsClaim(path) {
			continue
		}
		st, ok := r.coord.stateAt(path)
		switch {
		case !ok, st.Ordinal == 0:
			continue
		case st.Channel != r.channel:
			// Another channel or handler, or a state that predates the field.
			continue
		case st.Next == 0:
			// No armed attempt yet: nothing to report.
			continue
		case r.sched.has(st.Ordinal):
			// Held here by number: the marker can fail while a peer armed the state. Forget any
			// followed chain under this number so kill acts only on the listed chain.
			r.forget(st.Ordinal)
			continue
		case st.Done:
			r.closeChain(st)
			continue
		}
		r.followChain(ctx, path, st)
	}
	r.forgetAllBut(seen)
}

func (r *registry) followChain(ctx context.Context, path string, st deliveryState) {
	p, fresh := r.upsert(path, st)
	if fresh {
		r.noteFollowing()
	}
	if st.Settled && st.Attempt > p.printed && r.mirrors() {
		r.markPrinted(st.Ordinal, st.Attempt)
		renderPeerDelivery(r.f, r.target, p, st)
	}
	if st.Next > st.Attempt && st.Next > p.notedNext && !r.f.Global.Quiet {
		r.markNoted(st.Ordinal, st.Next)
		// stderr: mirrors() may have withheld the delivery line, and the note alone would be an orphan on stdout.
		noteLine(r.f, "    %s\n", r.f.IOStreams.Dim(fmt.Sprintf("%sretry %d/%d %s",
			r.label(st.Ordinal), st.Next, p.cap, retryWhen(time.Until(p.due).Round(time.Second)))))
	}
	if ctx.Err() != nil {
		return
	}
	// Ending it stops later sessions from rediscovering it.
	if r.unheldStreak(st.Ordinal, r.coord.unheld(path)) < orphanConfirmations {
		return
	}
	r.coord.endAt(path, endedUnheld)
	r.forget(st.Ordinal)
	noteLine(r.f, "    %s\n", r.f.IOStreams.Dim(
		r.label(st.Ordinal)+"abandoned — the session holding it exited"))
}

func (r *registry) closeChain(st deliveryState) {
	p, ok := r.take(st.Ordinal)
	if !ok {
		return // never followed; its end is not this session's news
	}
	if st.Settled && st.Attempt > p.printed && r.mirrors() {
		renderPeerDelivery(r.f, r.target, p, st)
	}
	var note string
	switch {
	case st.Killed:
		note = r.label(st.Ordinal) + "killed in another session"
	case st.Abandoned:
		note = r.label(st.Ordinal) + "abandoned — the session holding it exited"
	case st.Attempt >= p.cap:
		note = fmt.Sprintf("%sgave up after %d retries", r.label(st.Ordinal), st.Attempt)
	default:
		note = chainOverNote(r.control.active(), st.Ordinal)
	}
	noteLine(r.f, "    %s\n", r.f.IOStreams.Dim(note))
}

func (r *registry) views() []chainView {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	views := make([]chainView, 0, len(r.chains))
	for _, p := range r.chains {
		views = append(views, chainView{
			ordinal: p.ordinal,
			attempt: p.next,
			next:    max(0, time.Until(p.due).Round(time.Second)),
			event:   p.event,
			tag:     p.tag,
			cap:     p.cap,
			peer:    true,
		})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ordinal < views[j].ordinal })
	return views
}

// kill ends the named peer chains; the holder stops at its next attempt.
func (r *registry) kill(ordinals []int) []int {
	if r == nil {
		return nil
	}
	var killed []int
	for _, n := range ordinals {
		p, ok := r.peek(n)
		if !ok || !r.coord.endAt(p.path, endedByKill) {
			// Still followed, so the next tick lists it again.
			continue
		}
		r.forget(n)
		killed = append(killed, n)
	}
	return killed
}

func (r *registry) killAll() []int {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	ordinals := make([]int, 0, len(r.chains))
	for n := range r.chains {
		ordinals = append(ordinals, n)
	}
	r.mu.Unlock()
	return r.kill(ordinals)
}

func (r *registry) upsert(path string, st deliveryState) (peerChain, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.chains[st.Ordinal]
	if !ok {
		// Start from the current attempt: an earlier one would be stamped with this moment's time.
		p = &peerChain{ordinal: st.Ordinal, path: path, printed: st.Attempt, notedNext: st.Next}
		r.chains[st.Ordinal] = p
	}
	p.event, p.tag, p.attempt, p.next = st.Event, st.Tag, st.Attempt, st.Next
	p.cap = st.Cap
	if p.cap == 0 {
		p.cap = DefaultForwardRetries
	}
	p.due = time.UnixMilli(st.DueMS)
	return *p, !ok
}

func (r *registry) markPrinted(ordinal, attempt int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.chains[ordinal]; p != nil {
		p.printed = attempt
	}
}

func (r *registry) markNoted(ordinal, next int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.chains[ordinal]; p != nil {
		p.notedNext = next
	}
}

func (r *registry) unheldStreak(ordinal int, unheld bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.chains[ordinal]
	switch {
	case p == nil:
		return 0
	case !unheld:
		p.unheld = 0
	default:
		p.unheld++
	}
	return p.unheld
}

func (r *registry) peek(ordinal int) (peerChain, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.chains[ordinal]
	if !ok {
		return peerChain{}, false
	}
	return *p, true
}

// take removes a chain so its ending is reported exactly once.
func (r *registry) take(ordinal int) (peerChain, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.chains[ordinal]
	if !ok {
		return peerChain{}, false
	}
	delete(r.chains, ordinal)
	return *p, true
}

func (r *registry) forget(ordinal int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.chains, ordinal)
}

// forgetAllBut drops chains whose state file is gone.
func (r *registry) forgetAllBut(paths map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for n, p := range r.chains {
		if !paths[p.path] {
			delete(r.chains, n)
		}
	}
}

func (r *registry) noteFollowing() {
	r.mu.Lock()
	first := !r.noted
	r.noted = true
	r.mu.Unlock()
	if !first {
		return
	}
	noteLine(r.f, "note: following retry chains held by another listen session; this one reports them without sending\n")
}

// label names the ordinal only while control input is read.
func (r *registry) label(ordinal int) string {
	if !r.control.active() {
		return ""
	}
	return fmt.Sprintf("#%d ", ordinal)
}

// renderPeerDelivery prints a peer's attempt through the shared renderer, without headers or payload.
func renderPeerDelivery(f *cmdutil.Factory, target string, p peerChain, st deliveryState) {
	io := f.IOStreams
	line := deliveryLine{event: p.event, target: target}
	if st.Attempt > 0 {
		line.attempt = "   " + io.Dim(fmt.Sprintf("(retry %d/%d)", st.Attempt, p.cap))
	}
	if p.tag != "" {
		line.tag = io.Dim(p.tag)
	}
	res, err := st.outcome()
	renderDelivery(f, line, res, err, nil)
}

// plainTag renders the tag without color: another session's terminal reads it back.
func plainTag(src Source, fr Frame) string {
	if src.Tag == nil {
		return ""
	}
	return src.Tag(iostreams.Plain(), fr)
}
