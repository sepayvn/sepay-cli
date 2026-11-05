package listen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/forward"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"
)

// claimDirName holds one small file per delivery, named by digest: no payload, no id.
const claimDirName = "listen-retries"

const seqSuffix = ".seq"

// Coordination files are user-editable and shared by every session: validated on read.
const (
	maxClaimBytes = 64 << 10
	// maxOrdinal keeps the sequence from overflowing into an ordinal kill could never address.
	maxOrdinal      = 1 << 30
	maxMirroredBody = 2 << 10
	maxMetaLen      = 256
)

// ownSuffix names the marker every holder locks shared; an exclusive lock succeeding means
// no live holder is left. The kernel drops it on process death, so no lease is needed.
const ownSuffix = ".own"

const (
	claimWait = 250 * time.Millisecond
	seqWait   = 250 * time.Millisecond
)

// claimStaleAge must cover the longest chain (143 min curve plus a forwarder timeout per attempt).
const claimStaleAge = 6 * time.Hour

// claimEndedAge outlasts a follower's next scan (registryPoll * orphanConfirmations).
const claimEndedAge = time.Minute

// deliveryState is the whole coordination protocol every listener on one delivery agrees on.
type deliveryState struct {
	// shared across sessions so `kill 5` means one delivery everywhere
	Ordinal int `json:"ordinal,omitempty"`
	// highest retry number any listener has sent
	Attempt int `json:"attempt"`
	// handler accepted it or the budget ran out
	Done bool `json:"done,omitempty"`
	// kill acts on the delivery, in every session
	Killed bool `json:"killed,omitempty"`
	// no session was left holding it
	Abandoned bool `json:"abandoned,omitempty"`

	// the outcome below belongs to Attempt
	Settled   bool   `json:"settled,omitempty"`
	Status    int    `json:"status,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Failure   string `json:"failure,omitempty"` // a forward.Cause
	Body      string `json:"body,omitempty"`

	// Channel keeps a follower off other channels' states (the filename digest is one-way).
	// The rest lets a session follow a chain it never received. The body and credential headers never reach disk.
	Channel string `json:"channel,omitempty"`
	Event   string `json:"event,omitempty"`
	Tag     string `json:"tag,omitempty"`
	Cap     int    `json:"cap,omitempty"`
	Next    int    `json:"next,omitempty"`
	DueMS   int64  `json:"due_ms,omitempty"`
}

type verdict int

const (
	sendIt verdict = iota
	// another listener has this attempt
	standDown
	// the delivery ended elsewhere
	chainOver
)

// coordinator keeps a handler's retry load equal to production's however many sessions listen.
// Only the request is coordinated; a lock covers one read and write, never the POST.
type coordinator struct {
	dir   string
	scope string
	// different handlers are not duplicates and must both retry
	origin string

	// shared markers for held chains, by ordinal
	mu   sync.Mutex
	held map[int]*heldChain
}

type heldChain struct {
	lock  *config.FileLock
	claim string
}

// newCoordinator returns nil when retry is off or there is no scope; a nil coordinator is inert.
func newCoordinator(scope, origin string, retryMax int) *coordinator {
	if retryMax <= 0 || scope == "" || origin == "" {
		return nil
	}
	dir, err := config.Dir()
	if err != nil {
		return nil
	}
	c := &coordinator{
		dir:    filepath.Join(dir, claimDirName),
		scope:  scope,
		origin: origin,
		held:   map[int]*heldChain{},
	}
	c.sweep()
	return c
}

// path digests channel, handler and delivery so no two of them share a file.
func (c *coordinator) path(fr Frame) string {
	h := sha256.New()
	for _, part := range []string{c.scope, c.origin, deliveryID(fr)} {
		_, _ = io.WriteString(h, part)
		_, _ = h.Write([]byte{0})
	}
	return filepath.Join(c.dir, hex.EncodeToString(h.Sum(nil))[:32]+".json")
}

// deliveryID falls back to hashing Raw, so a per-subscriber frame field fails open to uncoordinated retries.
func deliveryID(fr Frame) string {
	if fr.ID != "" {
		return fr.ID
	}
	sum := sha256.Sum256(fr.Raw)
	return "raw:" + hex.EncodeToString(sum[:])
}

// beforeAttempt fails open: on filesystem trouble it sends, since a duplicate retry is
// acceptable and a silent stop is not.
func (c *coordinator) beforeAttempt(fr Frame, attempt int) verdict {
	if c == nil {
		return sendIt
	}
	// Most lock holders are not deciding anything, so contention means wait, not stand down.
	lock, ok, err := config.TryLockFileFor(c.path(fr), claimWait)
	if err != nil || !ok {
		return sendIt
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil {
		return sendIt
	}
	switch {
	case st.Killed, st.Done:
		return chainOver
	case st.Attempt >= attempt:
		return standDown
	}

	st.Attempt, st.Settled = attempt, false
	st.Status, st.LatencyMS, st.Failure, st.Body = 0, 0, "", ""
	_ = writeState(lock.File(), st)
	return sendIt
}

// finish writes nothing for a first delivery: a peer whose first attempt failed is owed its full curve.
func (c *coordinator) finish(j job, killed bool) {
	if j.ordinal == 0 {
		return
	}
	c.finishFrame(j.frame, killed)
}

// exhausted ends the chain for everyone only once the largest declared budget is spent;
// short of that it clears the armed attempt so the owed peer re-arms.
func (c *coordinator) exhausted(j job) {
	if c == nil || j.ordinal == 0 {
		return
	}
	lock, ok, err := config.TryLockFileFor(c.path(j.frame), claimWait)
	if err != nil || !ok {
		return
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil {
		return
	}
	if j.attempt >= st.Cap {
		st.Done = true
	} else {
		st.Next, st.DueMS = 0, 0
	}
	_ = writeState(lock.File(), st)
}

func (c *coordinator) finishFrame(fr Frame, killed bool) {
	if c == nil {
		return
	}
	// A kill or end must not be dropped because a poll held the file.
	lock, ok, err := config.TryLockFileFor(c.path(fr), claimWait)
	if err != nil || !ok {
		return
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil {
		return
	}
	st.Done = true
	st.Killed = st.Killed || killed
	_ = writeState(lock.File(), st)
}

// sweep is best effort: a peer may hold a file open, and Windows refuses to delete open files.
func (c *coordinator) sweep() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		// A marker's mtime is never refreshed, so age cannot judge it.
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(c.dir, e.Name())
		age := time.Since(info.ModTime())
		endedEarly := age >= claimEndedAge && ended(path)
		if age < claimStaleAge && !endedEarly {
			continue
		}
		_ = os.Remove(path)
		_ = os.Remove(ownPathFor(path))
	}
}

// ended reads without the lock: the end flags are one-way latches and a torn read yields
// the zero value, so a partial read can only keep a file.
func ended(claimPath string) bool {
	f, err := os.Open(filepath.Clean(claimPath))
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	st, err := readState(f)
	if err != nil {
		return false
	}
	return st.Done || st.Killed || st.Abandoned
}

// readState treats an empty file as the zero state and range-checks the rest.
func readState(f *os.File) (deliveryState, error) {
	var st deliveryState
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return st, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxClaimBytes))
	if err != nil || len(data) == 0 {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return deliveryState{}, nil // truncated or corrupt reads as unclaimed
	}
	return sane(st), nil
}

func sane(st deliveryState) deliveryState {
	if st.Ordinal < 1 || st.Ordinal > maxOrdinal {
		st.Ordinal = 0 // unnumbered; a fresh number is allocated instead
	}
	if st.Attempt < 0 || st.Attempt > MaxForwardRetries {
		// A value past the cap would park the delivery for good.
		st.Attempt = 0
		st.Settled = false
	}
	if st.Status != 0 && (st.Status < 100 || st.Status > 599) {
		st.Status = 0
	}
	if st.LatencyMS < 0 {
		st.LatencyMS = 0
	}
	if len(st.Body) > maxMirroredBody {
		st.Body = st.Body[:maxMirroredBody]
	}
	if st.Cap < 0 || st.Cap > MaxForwardRetries {
		st.Cap = 0 // unknown budget; the reader falls back to its own
	}
	if st.Next < 0 || st.Next > MaxForwardRetries {
		st.Next = 0 // not followable: a chain is followed only once an attempt is armed
	}
	if st.DueMS < 0 {
		st.DueMS = 0
	}
	if len(st.Channel) > maxMetaLen {
		st.Channel = "" // not a channel this build writes; it matches nothing
	}
	// Printed to a terminal from a file any local process can write.
	st.Event = clampMeta(st.Event)
	st.Tag = clampMeta(st.Tag)
	return st
}

func clampMeta(s string) string {
	s = jsonfmt.Clean(s)
	if len(s) > maxMetaLen {
		s = s[:maxMetaLen]
	}
	return strings.ToValidUTF8(s, "")
}

// writeState rewrites in place: an atomic rename would leave the writer locking an unlinked inode.
func writeState(f *os.File, st deliveryState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Write before truncate: a crash leaves a stale tail, not an empty file.
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Truncate(int64(len(data)))
}

// finishAll is called outside the scheduler's mutex.
func (c *coordinator) finishAll(frames []Frame, killed bool) {
	for _, fr := range frames {
		c.finishFrame(fr, killed)
	}
}

// mirrorTimeout is the forwarder's 30s timeout plus a margin.
const mirrorTimeout = 35 * time.Second

const mirrorPoll = 25 * time.Millisecond

const mirrorPollMax = 400 * time.Millisecond

// settle records the handler's answer so stood-down sessions print the same line.
func (c *coordinator) settle(fr Frame, attempt int, res *forward.Result, sendErr error) {
	if c == nil || isShutdown(sendErr) {
		// Ctrl+C is not an outcome.
		return
	}
	// A peer polls this file while it waits, so one try often loses the race.
	lock, ok, err := config.TryLockFileFor(c.path(fr), claimWait)
	if err != nil || !ok {
		return
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil || st.Attempt != attempt {
		return // a later attempt already moved on; its outcome is the current one
	}
	st.Settled = true
	if sendErr != nil {
		st.Failure = forward.Cause(sendErr)
	} else if res != nil {
		st.Status, st.LatencyMS, st.Body = res.StatusCode, res.Latency.Milliseconds(), string(res.Body)
	}
	_ = writeState(lock.File(), st)
}

func (st deliveryState) outcome() (*forward.Result, error) {
	if st.Failure != "" {
		// The file is user-editable: only a known cause is read back.
		cause := st.Failure
		if cause != forward.CauseTLS && cause != forward.CauseTimeout {
			cause = forward.CauseDown
		}
		return nil, &forward.Unanswered{Cause: cause}
	}
	return &forward.Result{
		StatusCode: st.Status,
		Latency:    time.Duration(st.LatencyMS) * time.Millisecond,
		Body:       []byte(st.Body),
	}, nil
}

type attemptOutcome struct {
	res     *forward.Result
	sendErr error
}

// awaitOutcome returns ok=false when the wait is cut short; the caller then prints nothing.
func (c *coordinator) awaitOutcome(ctx context.Context, fr Frame, attempt int, timeout time.Duration) (attemptOutcome, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	// Hashed once: an id-less frame hashes up to 1 MiB of Raw.
	path := c.path(fr)
	wait := mirrorPoll
	for {
		if st, err := c.readSettledAt(path, attempt); err == nil && st != nil {
			res, sendErr := st.outcome()
			return attemptOutcome{res: res, sendErr: sendErr}, true
		}
		select {
		case <-ctx.Done():
			return attemptOutcome{}, false
		case <-deadline.C:
			return attemptOutcome{}, false
		case <-time.After(wait):
			if wait < mirrorPollMax {
				wait *= 2
			}
		}
	}
}

// readSettledAt returns nil, nil until the outcome for attempt is recorded.
func (c *coordinator) readSettledAt(path string, attempt int) (*deliveryState, error) {
	lock, ok, err := config.TryLockFile(path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil // the sender holds it; try again on the next tick
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil {
		return nil, err
	}
	if st.Attempt != attempt || !st.Settled {
		return nil, nil
	}
	return &st, nil
}

// ordinalFor returns 0 when the caller should use its own count. Never call it under the
// scheduler's mutex; its two locks are taken in turn, never nested.
func (c *coordinator) ordinalFor(fr Frame) int {
	if c == nil {
		return 0
	}
	if n := c.storedOrdinal(fr); n != 0 {
		return n
	}
	n := c.allocate()
	if n == 0 {
		return 0
	}
	return c.adoptOrdinal(fr, n)
}

func (c *coordinator) storedOrdinal(fr Frame) int {
	lock, ok, err := config.TryLockFileFor(c.path(fr), claimWait)
	if err != nil || !ok {
		return 0
	}
	defer lock.Release()
	st, err := readState(lock.File())
	if err != nil {
		return 0
	}
	return st.Ordinal
}

// adoptOrdinal returns n or the number another session stored first; the loser leaves a harmless gap.
func (c *coordinator) adoptOrdinal(fr Frame, n int) int {
	lock, ok, err := config.TryLockFileFor(c.path(fr), claimWait)
	if err != nil || !ok {
		return 0
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil {
		return 0
	}
	if st.Ordinal != 0 {
		return st.Ordinal
	}
	st.Ordinal, st.Channel = n, c.channelID()
	if writeState(lock.File(), st) != nil {
		return 0
	}
	return n
}

// allocate returns 0 on any trouble.
func (c *coordinator) allocate() int {
	lock, ok, err := config.TryLockFileFor(c.seqPath(), seqWait)
	if err != nil || !ok {
		return 0
	}
	defer lock.Release()

	f := lock.File()
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxClaimBytes))
	if err != nil {
		return 0
	}
	text := strings.TrimSpace(string(raw))
	n := 0
	if text != "" {
		// An unreadable sequence must not restart at 1 and reuse live numbers.
		if n, err = strconv.Atoi(text); err != nil {
			return 0
		}
	}
	if n < 0 || n >= maxOrdinal {
		return 0 // out of range: refuse rather than wrap into a number kill cannot take
	}
	n++

	data := []byte(strconv.Itoa(n))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0
	}
	// Write before truncate: a crash must not leave an empty file that reads as "start over".
	if _, err := f.Write(data); err != nil {
		return 0
	}
	if f.Truncate(int64(len(data))) != nil {
		return 0
	}
	return n
}

func (c *coordinator) channelID() string {
	h := sha256.New()
	_, _ = io.WriteString(h, c.scope+"\x00"+c.origin)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (c *coordinator) seqPath() string {
	return filepath.Join(c.dir, c.channelID()+seqSuffix)
}

func ownPathFor(claimPath string) string {
	return strings.TrimSuffix(claimPath, ".json") + ownSuffix
}

// holdChain takes fr's marker. On failure the caller must not publish the chain as
// followable: a follower ends a followable chain it finds unheld.
func (c *coordinator) holdChain(fr Frame, ordinal int) bool {
	if c == nil || ordinal == 0 {
		return false
	}
	claim := c.path(fr)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[ordinal] != nil {
		return true
	}
	// A peer's orphan check holds it exclusively briefly; failing here would look like letting go.
	lock, ok, err := config.TrySharedLockFileFor(ownPathFor(claim), claimWait)
	if err != nil || !ok {
		return false
	}
	c.held[ordinal] = &heldChain{lock: lock, claim: claim}
	return true
}

// holdsClaim matches by path, not ordinal: a chain may be numbered privately here.
func (c *coordinator) holdsClaim(claimPath string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, h := range c.held {
		if h.claim == claimPath {
			return true
		}
	}
	return false
}

// releaseChain drops this session's shared marker; the chain looks unheld only once the last holder leaves.
func (c *coordinator) releaseChain(ordinal int) {
	if c == nil || ordinal == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.held[ordinal]; h != nil {
		h.lock.Release()
		delete(c.held, ordinal)
	}
}

func (c *coordinator) releaseChains(ordinals []int) {
	for _, n := range ordinals {
		c.releaseChain(n)
	}
}

type chainMeta struct {
	next  int
	cap   int
	due   time.Time
	event string
	tag   string
}

// armed makes the chain followable: no peer reports on a state with no armed attempt.
func (c *coordinator) armed(fr Frame, m chainMeta) {
	if c == nil || m.next == 0 {
		return
	}
	lock, ok, err := config.TryLockFileFor(c.path(fr), claimWait)
	if err != nil || !ok {
		return
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil {
		return
	}
	// The largest budget wins, not the last written.
	st.Next, st.Cap, st.Event, st.Tag = m.next, max(st.Cap, m.cap), m.event, m.tag
	st.DueMS, st.Channel = m.due.UnixMilli(), c.channelID()
	_ = writeState(lock.File(), st)
}

// stateAt waits for the lock: a mirroring session polls this file every 25ms.
func (c *coordinator) stateAt(claimPath string) (deliveryState, bool) {
	lock, ok, err := config.TryLockFileFor(claimPath, claimWait)
	if err != nil || !ok {
		return deliveryState{}, false
	}
	defer lock.Release()
	st, err := readState(lock.File())
	if err != nil {
		return deliveryState{}, false
	}
	return st, true
}

// claimPaths caps the scan when a directory holds more files than any session could.
func (c *coordinator) claimPaths(limit int) []string {
	if c == nil {
		return nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue // the sequence and the markers are not deliveries
		}
		if len(paths) == limit {
			break
		}
		paths = append(paths, filepath.Join(c.dir, e.Name()))
	}
	return paths
}

type endReason int

const (
	endedByKill endReason = iota
	endedUnheld
)

func (c *coordinator) endAt(claimPath string, why endReason) bool {
	if c == nil {
		return false
	}
	lock, ok, err := config.TryLockFileFor(claimPath, claimWait)
	if err != nil || !ok {
		return false
	}
	defer lock.Release()

	st, err := readState(lock.File())
	if err != nil {
		return false
	}
	st.Done = true
	st.Killed = st.Killed || why == endedByKill
	st.Abandoned = st.Abandoned || why == endedUnheld
	return writeState(lock.File(), st) == nil
}

// unheld probes the marker exclusively and releases it at once.
func (c *coordinator) unheld(claimPath string) bool {
	if c == nil {
		return false
	}
	lock, ok, err := config.TryLockFile(ownPathFor(claimPath))
	if err != nil || !ok {
		return false
	}
	lock.Release()
	return true
}

func chainOverNote(control bool, ordinal int) string {
	if control {
		return fmt.Sprintf("#%d stopped — the delivery ended in another session", ordinal)
	}
	return "stopped — the delivery ended in another session"
}
