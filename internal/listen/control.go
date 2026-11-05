package listen

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
)

type controlAction int

const (
	actionNone controlAction = iota
	actionHelp
	actionList
	actionCancel
	actionCancelAll
	actionCancelNoTarget
	actionArmingOff
	actionArmingOn
	actionUnknown
)

const maxHintEcho = 40

// hintFor echoes the rejected line, sanitized and bounded: cooked mode turns an arrow key
// into literal escape bytes, and the echo shows that.
func hintFor(line string) string {
	got := []rune(jsonfmt.Clean(strings.TrimSpace(line)))
	if len(got) == 0 {
		return "unknown input — ? for help"
	}
	if len(got) > maxHintEcho {
		return fmt.Sprintf("unknown input %q… — ? for help", string(got[:maxHintEcho]))
	}
	return fmt.Sprintf("unknown input %q — ? for help", string(got))
}

const controlHelp = "retries list · kill <n>… · kill all · retries off/on (stop/resume arming new)"

// parseControl reads one control line. The verb is `kill`, never a letter: `r 3` reads as
// "retry 3 now". The line is sanitized first, so a stray Backspace is nothing.
func parseControl(line string) (controlAction, []int) {
	fields := strings.Fields(jsonfmt.Clean(line))
	if len(fields) == 0 {
		return actionNone, nil
	}

	switch fields[0] {
	case "?", "help":
		if len(fields) == 1 {
			return actionHelp, nil
		}
	case "retries":
		switch {
		case len(fields) == 1, len(fields) == 2 && fields[1] == "list":
			return actionList, nil
		case len(fields) == 2 && fields[1] == "off":
			return actionArmingOff, nil
		case len(fields) == 2 && fields[1] == "on":
			return actionArmingOn, nil
		}
	case "kill":
		args := fields[1:]
		if len(args) == 0 {
			return actionCancelNoTarget, nil
		}
		if len(args) == 1 && args[0] == "all" {
			return actionCancelAll, nil
		}
		ordinals := make([]int, 0, len(args))
		for _, a := range args {
			n, err := strconv.Atoi(a)
			if err != nil || n < 1 {
				return actionUnknown, nil
			}
			ordinals = append(ordinals, n)
		}
		return actionCancel, ordinals
	}
	return actionUnknown, nil
}

// controller reads retry-control commands from stdin while streaming. It only ever stops work.
type controller struct {
	f        *cmdutil.Factory
	sched    *retryScheduler
	coord    *coordinator
	reg      *registry
	src      Source
	retryMax int
	live     atomic.Bool
}

// active is false without a control layer or once the reader died (a backgrounded `listen &`).
func (c *controller) active() bool { return c != nil && c.live.Load() }

// newController returns nil, reading no stdin, for machine output, --quiet, --no-input, pipes or CI.
func newController(f *cmdutil.Factory, sched *retryScheduler, coord *coordinator, reg *registry, src Source, retryMax int) *controller {
	if sched == nil || !f.CanPrompt() {
		return nil
	}
	c := &controller{f: f, sched: sched, coord: coord, reg: reg, src: src, retryMax: retryMax}
	c.live.Store(true)
	return c
}

// run is outside the session's WaitGroup: a terminal read cannot be cancelled, so shutdown
// would wait for Enter. A failed read ends control input but leaves the stream running.
func (c *controller) run(ctx context.Context) {
	defer c.live.Store(false)
	ignoreBackgroundReadStop()

	sc := bufio.NewScanner(c.f.IOStreams.In)
	// A long paste would otherwise end the reader for good.
	sc.Buffer(make([]byte, 0, 4096), maxControlLine)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		c.handle(sc.Text())
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		c.emit(c.f.IOStreams.Dim("retry control is no longer reading input; kill and retries will not answer"))
	}
}

const maxControlLine = 64 << 10

// handle reads the scheduler before emit holds the screen, keeping the forwarder's lock order.
func (c *controller) handle(line string) {
	io := c.f.IOStreams
	action, ordinals := parseControl(line)
	switch action {
	case actionNone:
	case actionHelp:
		c.emit(io.Dim(controlHelp))
	case actionList:
		c.renderList(mergeViews(c.sched.list(), c.reg.views()), c.sched.arming())
	case actionCancel:
		res := c.sched.cancel(ordinals)
		c.coord.finishAll(res.frames, true)
		c.coord.releaseChains(res.dropped)
		res.dropped = append(res.dropped, c.reg.kill(ordinals)...)
		c.renderCancelled(ordinals, res.dropped, res.sending)
	case actionCancelAll:
		res := c.sched.cancelAll()
		c.coord.finishAll(res.frames, true)
		c.coord.releaseChains(res.dropped)
		res.dropped = append(res.dropped, c.reg.killAll()...)
		if len(res.dropped) == 0 {
			c.emit(io.Dim("no armed retry chains"))
		} else {
			c.emit(c.withSentCaveat([]string{
				fmt.Sprintf("%s killed %d chain(s)", io.Green("✓"), len(res.dropped)),
			}, res.sending)...)
		}
	case actionCancelNoTarget:
		c.emit(io.Dim("kill needs a chain number or all — retries lists them"))
	case actionArmingOff:
		if c.sched.setArming(false) {
			c.emit(fmt.Sprintf("%s new failures are no longer retried — retries on to resume", io.Green("✓")))
		} else {
			c.emit(io.Dim("new failures are already not retried"))
		}
	case actionArmingOn:
		if c.sched.setArming(true) {
			c.emit(fmt.Sprintf("%s new failures are retried again", io.Green("✓")))
		} else {
			c.emit(io.Dim("new failures are already retried; a killed chain does not come back"))
		}
	case actionUnknown:
		c.emit(io.Dim(hintFor(line)))
	}
}

// mergeViews merges local and followed chains; on a duplicate number (private fallback count) the local chain wins.
func mergeViews(local, peers []chainView) []chainView {
	mine := make(map[int]bool, len(local))
	for _, v := range local {
		mine[v.ordinal] = true
	}
	views := local
	for _, v := range peers {
		if !mine[v.ordinal] {
			views = append(views, v)
		}
	}
	return views
}

func (c *controller) capFor(v chainView) int {
	if v.peer && v.cap > 0 {
		return v.cap
	}
	return c.retryMax
}

func (c *controller) eventOf(v chainView) string {
	if v.peer {
		return v.event
	}
	return v.frame.Type
}

func (c *controller) tagOf(v chainView) string {
	if v.peer {
		return strings.TrimSpace(v.tag)
	}
	if c.src.Tag == nil {
		return ""
	}
	return strings.TrimSpace(c.src.Tag(c.f.IOStreams, v.frame))
}

func (c *controller) heldBy(v chainView) string {
	if v.peer {
		return c.f.IOStreams.Dim("another session")
	}
	return "this session"
}

const controlPrefix = "│ "

// emit writes one prefixed block to stderr while holding the screen, so no delivery lands inside it.
func (c *controller) emit(lines ...string) {
	io := c.f.IOStreams
	io.Block(func() {
		fmt.Fprintln(io.ErrOut)
		for _, l := range lines {
			fmt.Fprintf(io.ErrOut, "%s%s\n", io.Dim(controlPrefix), l)
		}
		fmt.Fprintln(io.ErrOut)
	})
}

// withSentCaveat notes that kill does not recall a request already out.
func (c *controller) withSentCaveat(lines []string, sending int) []string {
	if sending == 0 {
		return lines
	}
	return append(lines, "    "+c.f.IOStreams.Dim(
		fmt.Sprintf("%d attempt(s) already sent will still finish", sending)))
}

// renderCancelled names the numbers that matched no live chain, so a typo never reads as success.
func (c *controller) renderCancelled(requested, dropped []int, sending int) {
	io := c.f.IOStreams
	cancelled := make(map[int]bool, len(dropped))
	for _, n := range dropped {
		cancelled[n] = true
	}
	var hits, misses []string
	named := make(map[int]bool, len(requested))
	for _, n := range requested {
		if named[n] {
			continue // `kill 1 1` is one chain, said once
		}
		named[n] = true
		if cancelled[n] {
			hits = append(hits, "#"+strconv.Itoa(n))
		} else {
			misses = append(misses, "#"+strconv.Itoa(n))
		}
	}

	if len(hits) == 0 {
		c.emit(io.Dim("no armed retry chain by that number"))
		return
	}
	lines := []string{fmt.Sprintf("%s killed %s", io.Green("✓"), strings.Join(hits, " "))}
	if len(misses) > 0 {
		lines = append(lines, "    "+io.Dim(strings.Join(misses, " ")+" — no armed chain by that number"))
	}
	c.emit(c.withSentCaveat(lines, sending)...)
}

func (c *controller) renderList(views []chainView, arming bool) {
	io := c.f.IOStreams
	var lines []string
	if len(views) == 0 {
		lines = append(lines, io.Dim("no armed retry chains"))
	} else {
		peers := false
		for _, v := range views {
			peers = peers || v.peer
		}
		sort.Slice(views, func(i, j int) bool { return views[i].ordinal < views[j].ordinal })

		header := []string{"#", "NEXT", "ATTEMPT", "EVENT", ""}
		if peers {
			header = []string{"#", "NEXT", "ATTEMPT", "EVENT", "HELD BY", ""}
		}
		t := jsonfmt.NewTable(header...).Truncatable("EVENT", "")
		t.HeaderColor = io.Cyan
		if width := io.TerminalWidth(io.ErrOut); width > 0 {
			t.MaxWidth = width - utf8.RuneCountInString(controlPrefix)
		}
		for _, v := range views {
			// A fired timer waits on the forwarder, not the clock.
			next := retryWhen(v.next)
			if v.sending && !v.peer {
				next = "sending"
			}
			row := []string{
				"#" + strconv.Itoa(v.ordinal), next,
				fmt.Sprintf("%d/%d", v.attempt, c.capFor(v)), c.eventOf(v),
			}
			if peers {
				row = append(row, c.heldBy(v))
			}
			t.AddRow(append(row, c.tagOf(v))...)
		}
		// Buffered so the table goes out as one block.
		var buf bytes.Buffer
		_ = t.Render(&buf)
		lines = append(lines, strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")...)
		lines = append(lines, io.Dim(controlHelp))
	}
	if !arming {
		lines = append(lines, io.Dim("new failures are not being retried — retries on to resume"))
	}
	c.emit(lines...)
}
