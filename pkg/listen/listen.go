// Package listen streams one SePay delivery channel over SSE and replays each delivery
// to a local HTTP handler.
package listen

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/forward"
	"github.com/sepayvn/sepay-cli/internal/sse"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
)

// ForwardQueueSize matches the hub's per-subscriber depth; a full queue drops the newest event.
const ForwardQueueSize = 64

const dropWarnEvery = 50

// reconnectHealthyAfter is just long enough to exceed an accept-then-drop flap.
const reconnectHealthyAfter = 5 * time.Second

// maxKeyRefusals bounds consecutive refusals of a fresh key treated as the mint-to-open race.
const maxKeyRefusals = 3

// FrameEventName is the SSE `event:` label on every data frame; product identity is in Frame.Type.
const FrameEventName = "webhook"

// Options are the flags every listen command shares, after resolution.
type Options struct {
	Forward        string
	Events         []string
	Print          bool
	Headers        bool
	SkipTLS        bool
	ReconnectMax   int
	ForwardRetries int
	// replay credential headers to a non-loopback handler
	ForwardCredentials bool
}

// job is one queued delivery: attempt 0 is the first send, ordinal 0 means no chain yet.
type job struct {
	frame   Frame
	attempt int
	ordinal int
}

// Session is the mint response. Heartbeat and ExpiresIn are advisory only.
type Session struct {
	HubURL      string `json:"hub_url"`
	ListenKey   string `json:"listen_key"`
	Environment string `json:"environment"`
	Heartbeat   int    `json:"heartbeat"`
	ExpiresIn   int    `json:"expires_in"`
}

// Frame is one delivery. RawBody is POSTed verbatim so a signature still verifies.
type Frame struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	CreatedAt string            `json:"created_at"`
	Headers   map[string]string `json:"headers"`
	Payload   json.RawMessage   `json:"payload"`
	RawBody   string            `json:"raw_body"`
	Raw       json.RawMessage   `json:"-"`
}

// Body is the exact bytes to replay: RawBody, else the payload.
func (fr Frame) Body() []byte {
	if fr.RawBody != "" {
		return []byte(fr.RawBody)
	}
	return fr.Payload
}

// Source is one product's listen channel: where to mint, what to accept, how to render.
type Source struct {
	MintPath string
	// empty skips revoking an unused listen_key
	RevokePath string
	Noun       string
	// hub channel; the boundary sessions share to coordinate
	Scope string
	// best-effort; nil to skip
	Describe func(context.Context, *cmdutil.Factory)
	// product filters; nil accepts all
	Accept func(Frame) bool
	Tag    func(*iostreams.IOStreams, Frame) string
	// nil retries transport failures only
	RetryNon2xx func(Frame) bool
	// first retry due at once, not a minute out
	FirstRetryImmediate bool
}

// AddFlags registers the flags every listen command shares.
func AddFlags(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.String("forward", "", "Local handler address, e.g. localhost:3000 (prompted when omitted)")
	fl.StringSlice("events", nil, "Only forward these event types (comma-separated)")
	fl.Bool("print", false, "Also print the full event payload")
	fl.Bool("headers", false, "Show forwarded request headers")
	fl.Bool("skip-tls-verify", false, "Skip TLS verification for a loopback https handler (credential headers are then withheld)")
	fl.Int("reconnect-max", 0, "Cap the reconnect backoff attempts (0 = unlimited)")
	fl.Int("forward-retries", DefaultForwardRetries,
		"Re-send a failed forward this many times on SePay's schedule (0 = never)")
	fl.Bool("forward-credentials", false,
		"Replay credential headers (Authorization, X-Secret-Key) to a non-loopback handler; they are withheld by default")
}

// CheckEvents rejects an --events value outside allowed: a typo would silently filter everything.
func CheckEvents(got []string, allowed []string) error {
	for _, e := range got {
		if !slices.Contains(allowed, e) {
			return &cmdutil.FlagError{Err: fmt.Errorf("--events must be one of: %s", strings.Join(allowed, ", "))}
		}
	}
	return nil
}

// DescribeEvents sets the --events usage text to the product's accepted values.
func DescribeEvents(cmd *cobra.Command, allowed []string) {
	if fl := cmd.Flags().Lookup("events"); fl != nil {
		fl.Usage = "Only forward these event types (comma-separated): " + strings.Join(allowed, ", ")
	}
}

// ReadOptions reads the shared flags off cmd and rejects out-of-range values before any network call.
func ReadOptions(cmd *cobra.Command) (Options, error) {
	fl := cmd.Flags()
	o := Options{}
	o.Forward, _ = fl.GetString("forward")
	o.Events, _ = fl.GetStringSlice("events")
	o.Print, _ = fl.GetBool("print")
	o.Headers, _ = fl.GetBool("headers")
	o.SkipTLS, _ = fl.GetBool("skip-tls-verify")
	o.ReconnectMax, _ = fl.GetInt("reconnect-max")
	o.ForwardRetries, _ = fl.GetInt("forward-retries")
	o.ForwardCredentials, _ = fl.GetBool("forward-credentials")

	if o.ForwardRetries < 0 || o.ForwardRetries > MaxForwardRetries {
		return o, &cmdutil.FlagError{
			Err: fmt.Errorf("--forward-retries must be between 0 and %d", MaxForwardRetries),
		}
	}
	// A negative cap would read as "0 = unlimited".
	if o.ReconnectMax < 0 {
		return o, &cmdutil.FlagError{
			Err: fmt.Errorf("--reconnect-max must be 0 or more (0 = unlimited)"),
		}
	}
	if o.Forward != "" {
		if _, err := forward.NewForwarder(o.Forward, o.SkipTLS); err != nil {
			return o, &cmdutil.FlagError{Err: err}
		}
	}
	return o, nil
}

// ResolveForward returns val, or prompts on a TTY; non-TTY without the flag is a usage error.
func ResolveForward(f *cmdutil.Factory, val string) (string, error) {
	return f.InputOrFlag(val, "forward", "Local handler address")
}

const mintRateLimitRetries = 3

// Mint decodes a fresh hub session. No Idempotency-Key: each call must issue a new listen_key;
// a 429 is safe to repeat since it rejects before a key is issued.
func Mint(ctx context.Context, client *api.Client, path string) (*Session, error) {
	for attempt := 0; ; attempt++ {
		env, err := client.Post(ctx, path, api.WriteOptions{})
		if err == nil {
			s, decodeErr := api.DataInto[Session](env)
			if decodeErr != nil {
				return nil, decodeErr
			}
			s.HubURL = normalizeHubURL(s.HubURL)
			return &s, nil
		}

		var he *api.HTTPError
		if attempt >= mintRateLimitRetries || !errors.As(err, &he) || he.StatusCode != http.StatusTooManyRequests {
			return nil, err
		}
		select {
		case <-time.After(jitter(api.Backoff("", attempt))):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// normalizeHubURL adds https to a scheme-less hub_url; the result is still origin- and host-checked.
func normalizeHubURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + strings.TrimPrefix(raw, "//")
}

const revokeTimeout = 5 * time.Second

// Revoke best-effort deletes a listen_key that never subscribed (the hub's auto-revoke misses it).
// The context is detached: callers are on cancelled paths where the DELETE would be a no-op.
func Revoke(ctx context.Context, client *api.Client, path, listenKey string) {
	if path == "" || listenKey == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revokeTimeout)
	defer cancel()
	// Header, not path: a request line reaches access logs and --verbose output.
	if _, err := client.Delete(ctx, path, api.WriteOptions{
		Header: http.Header{"X-Listen-Key": []string{listenKey}},
	}); err != nil {
		// Never log the key itself.
		slog.Warn("could not revoke the listen session; it expires on its own via the key TTL",
			"path", path, "err", err.Error())
	}
}

// Decode parses an SSE event's data into a Frame, keeping the raw bytes.
func Decode(data []byte) (Frame, error) {
	var fr Frame
	if err := json.Unmarshal(data, &fr); err != nil {
		return Frame{}, err
	}
	fr.Raw = data
	return fr, nil
}

// Run mints a session, holds the stream and forwards accepted deliveries until ctx is cancelled.
func Run(cmd *cobra.Command, f *cmdutil.Factory, src Source, opts Options) error {
	fwd, err := forward.NewForwarder(opts.Forward, opts.SkipTLS)
	if err != nil {
		return &cmdutil.FlagError{Err: err}
	}
	if !fwd.IsLoopback() {
		fmt.Fprintf(f.IOStreams.ErrOut, "warning: forwarding to a non-loopback address (%s)\n", fwd.Target())
	}
	if WithholdCredentialsFor(fwd, opts.ForwardCredentials) {
		fmt.Fprintf(f.IOStreams.ErrOut, "warning: %s\n", WithholdReason(fwd))
	}

	retryMax := opts.ForwardRetries
	if retryMax > 0 {
		if reason := retryDisabledReason(f, fwd); reason != "" {
			fmt.Fprintf(f.IOStreams.ErrOut, "note: %s; a failed forward is delivered once\n", reason)
			retryMax = 0
		}
	}

	if src.Describe != nil {
		src.Describe(cmd.Context(), f)
	}

	ctx, cancel := context.WithCancel(cmd.Context())

	filter := toSet(opts.Events)
	sseClient := sse.NewClient()

	// One forwarder drains across reconnects so a slow handler never stalls the read loop.
	jobs := make(chan job, ForwardQueueSize)

	// Retries get their own queue so a backlog cannot evict live events.
	var (
		retries chan job
		sched   *retryScheduler
	)
	if retryMax > 0 {
		retries = make(chan job, ForwardQueueSize)
		sched = newRetryScheduler(retries, f.IOStreams.ErrOut, src.FirstRetryImmediate)
	}

	// Retry runs on loopback only, so every duplicating session is on this machine: a local file suffices.
	coord := newCoordinator(channelScope(f, src.Scope), fwd.Origin(), retryMax)

	// Every session follows peer chains, TTY or not; only reprinting to stdout is TTY-gated.
	reg := newRegistry(f, coord, sched, fwd.Target())

	// Both are wired before either goroutine starts.
	control := newController(f, sched, coord, reg, src, retryMax)
	if reg != nil {
		reg.control = control
		go reg.run(ctx)
	}
	if control != nil {
		go control.run(ctx)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var notedSignature atomic.Bool
		notedPeer := false

		retryNote := func(next job, wait time.Duration) string {
			note := fmt.Sprintf("retry %d/%d %s", next.attempt, retryMax, retryWhen(wait))
			if control.active() {
				note = fmt.Sprintf("#%d %s", next.ordinal, note)
			}
			if notedSignature.CompareAndSwap(false, true) {
				// The replay carries the original signature, so a freshness window may reject it.
				note += " · replays the original signature"
			}
			return note
		}

		// mirrorFinal shows the last rung, which arms nothing and so never reaches mirror.
		mirrorFinal := func(j job) {
			defer wg.Done()
			out, ok := coord.awaitOutcome(ctx, j.frame, j.attempt, mirrorTimeout)
			if !ok {
				return
			}
			renderForward(f, src, j, fwd.Target(),
				OutboundHeaders(j.frame, WithholdCredentialsFor(fwd, opts.ForwardCredentials)), opts, out.res, out.sendErr)
			if shouldRetryForward(src, j.frame, out.res, out.sendErr) {
				printRetryNote(f, out.sendErr, fmt.Sprintf("gave up after %d retries", j.attempt))
			}
		}

		// mirror shows an attempt a peer sent, from the outcome it recorded.
		mirror := func(j, next job, wait time.Duration) {
			defer wg.Done()
			out, ok := coord.awaitOutcome(ctx, j.frame, j.attempt, mirrorTimeout)
			if !ok {
				return
			}
			renderForward(f, src, j, fwd.Target(),
				OutboundHeaders(j.frame, WithholdCredentialsFor(fwd, opts.ForwardCredentials)), opts, out.res, out.sendErr)
			if !shouldRetryForward(src, j.frame, out.res, out.sendErr) {
				// Release the marker too, or a follower would keep calling it held.
				coord.releaseChains(sched.cancel([]int{next.ordinal}).dropped)
				return
			}
			printRetryNote(f, out.sendErr, retryNote(next, wait))
		}
		for {
			j, ok := nextJob(ctx, jobs, retries)
			if !ok {
				return
			}
			// The budget already went back when the chain ended.
			if sched != nil && !sched.alive(j) {
				continue
			}

			// Standing down skips only the request, so the handler sees what production would send.
			send := true
			if j.ordinal != 0 {
				switch coord.beforeAttempt(j.frame, j.attempt) {
				case chainOver:
					// Silent if this session already killed the chain itself.
					if sched.has(j.ordinal) {
						noteLine(f, "    %s\n", f.IOStreams.Dim(
							chainOverNote(control.active(), j.ordinal)))
					}
					endChain(sched, coord, j)
					continue
				case standDown:
					send = false
					if !notedPeer {
						noteLine(f, "note: another listen session is retrying these deliveries to %s; this one tracks them without sending\n",
							fwd.Target())
						notedPeer = true
					}
				case sendIt:
				}
			}

			var res *forward.Result
			var err error
			if send {
				// From here a kill can no longer stop the request.
				sched.markInFlight(j)
				res, err = forwardEvent(ctx, j, fwd, src, opts, f)
				if j.ordinal != 0 {
					coord.settle(j.frame, j.attempt, res, err)
				}
			}
			if sched == nil {
				continue
			}
			if send && !shouldRetryForward(src, j.frame, res, err) {
				if !isShutdown(err) {
					// Ctrl+C lands here too but must not end every peer's chain.
					coord.finish(j, false)
				}
				endChain(sched, coord, j)
				continue
			}
			if j.attempt >= retryMax {
				if send {
					printRetryNote(f, err, fmt.Sprintf("gave up after %d retries", j.attempt))
					coord.exhausted(j)
				} else {
					// A peer with a larger --forward-retries is still owed its rungs.
					wg.Add(1)
					go mirrorFinal(j)
				}
				endChain(sched, coord, j)
				continue
			}
			// File I/O: done before the scheduler's mutex, never under it.
			assigned := 0
			if j.ordinal == 0 {
				assigned = coord.ordinalFor(j.frame)
			}
			next, wait, armed := sched.arm(ctx, j, assigned)
			// Marker first: a follower ends a followable chain it finds unheld.
			if armed && coord.holdChain(j.frame, next.ordinal) {
				coord.armed(j.frame, chainMeta{
					next:  next.attempt,
					cap:   retryMax,
					due:   time.Now().Add(wait),
					event: j.frame.Type,
					tag:   plainTag(src, j.frame),
				})
			}
			if !armed {
				if !sched.arming() {
					printRetryNote(f, err, "not retried — retries off")
				}
				endChain(sched, coord, j)
				continue
			}
			if !send {
				wg.Add(1)
				go mirror(j, next, wait)
				continue
			}
			printRetryNote(f, err, retryNote(next, wait))
		}
	}()

	var dropped int

	var warnedUnknownFrame bool

	// ctx is the only shutdown signal; anything left on the queue is abandoned.
	defer func() {
		cancel()
		wg.Wait()
		if dropped > 0 {
			noteLine(f, "warning: dropped %d matching event(s) — the local handler could not keep up\n", dropped)
		}
		if sched != nil {
			if pending := sched.stop(); pending > 0 {
				noteLine(f, "warning: %d event(s) still pending retry — dropped (retries are not persisted)\n", pending)
			}
		}
	}()

	// Revocation is server-authoritative: the hub session is cut and the re-mint fails 401/403.
	attempt := 0
	keyRefusals := 0
	for {
		client, err := f.NewAPIClient()
		if err != nil {
			return err
		}

		session, err := Mint(ctx, client, src.MintPath)
		if err != nil {
			// A 401 proves the grant is gone only if the refresh reached the OAuth host.
			var he *api.HTTPError
			if errors.As(err, &he) && he.StatusCode == http.StatusUnauthorized {
				if stale := f.StaleTokenRefresh(); stale != nil {
					// %v, not %w: an HTTPError in the chain would map this to ExitAuth.
					err = fmt.Errorf("%w (%v)", stale, err)
				}
			}
			if next := reconnectOrFail(ctx, f, &attempt, opts.ReconnectMax, phaseMint, err); next != nil {
				return unwrapClean(next)
			}
			continue
		}
		// Validate the minted hub URL before the listen_key is sent to it.
		if err := cmdutil.ValidateCredentialOrigin(session.HubURL); err != nil {
			Revoke(ctx, client, src.RevokePath, session.ListenKey)
			return cmdutil.ServerValueError("hub url", err)
		}
		if err := cmdutil.AssertHubHost(f.ActiveEnv(), session.HubURL, "hub_url in the mint response"); err != nil {
			Revoke(ctx, client, src.RevokePath, session.ListenKey)
			return cmdutil.ServerValueError("hub url", err)
		}

		stream, err := sseClient.Open(ctx, session.HubURL, session.ListenKey, time.Duration(session.Heartbeat)*time.Second)
		if err != nil {
			Revoke(ctx, client, src.RevokePath, session.ListenKey)
			// A refused key died between mint and open; past maxKeyRefusals it becomes the fatal 401.
			var kr *sse.KeyRefusedError
			if errors.As(err, &kr) {
				keyRefusals++
				if keyRefusals > maxKeyRefusals {
					err = &api.HTTPError{StatusCode: kr.StatusCode, Code: kr.Code, Message: kr.Message}
				}
			}
			if next := reconnectOrFail(ctx, f, &attempt, opts.ReconnectMax, phaseOpen, err); next != nil {
				return unwrapClean(next)
			}
			continue
		}

		printListening(f, src, session, fwd, control.active())
		connectedAt := time.Now()
		err = readLoop(ctx, stream, jobs, filter, src, &dropped, &warnedUnknownFrame, f)
		_ = stream.Close()

		if errors.Is(err, context.Canceled) {
			return nil
		}
		// Reset only after a healthy stream, or a flap would defeat --reconnect-max.
		if time.Since(connectedAt) >= reconnectHealthyAfter {
			attempt = 0
			keyRefusals = 0
		}
		if next := reconnectOrFail(ctx, f, &attempt, opts.ReconnectMax, phaseStream, err); next != nil {
			return unwrapClean(next)
		}
	}
}

// Hub frames not forwarded: `ready` greets every connection; `gap` reports events the hub
// discarded for this subscriber.
const (
	frameReady = "ready"
	frameGap   = "gap"
)

type gapFrame struct {
	Dropped int64 `json:"dropped"`
}

// noteUnforwardedFrame reports `gap` and warns once on an unknown frame name, which means
// a protocol mismatch where nothing is forwarded.
func noteUnforwardedFrame(f *cmdutil.Factory, ev sse.Event, warnedUnknown *bool) {
	switch ev.Name {
	case frameReady:
	case frameGap:
		var g gapFrame
		if err := json.Unmarshal(ev.Data, &g); err != nil || g.Dropped <= 0 {
			noteLine(f, "the hub dropped events it could not deliver in time\n")
			break
		}
		noteLine(f, "the hub dropped %d event(s) it could not deliver in time; the listener is not keeping up\n", g.Dropped)
	default:
		if !*warnedUnknown {
			*warnedUnknown = true
			noteLine(f, "the hub sent events named %q but this build only forwards %q, so nothing will be forwarded; upgrade the CLI\n", ev.Name, FrameEventName)
		}
	}

	slog.Debug("hub frame not forwarded", "event", ev.Name, "want", FrameEventName)
}

// readLoop filters before enqueueing; a full queue drops the newest frame to keep the socket draining.
func readLoop(ctx context.Context, stream *sse.Stream, jobs chan<- job, filter map[string]bool, src Source, dropped *int, warnedUnknownFrame *bool, f *cmdutil.Factory) error {
	for {
		ev, err := stream.Read(ctx)
		if err != nil {
			return err
		}
		if ev.Name != FrameEventName {
			noteUnforwardedFrame(f, ev, warnedUnknownFrame)
			continue
		}
		frame, ok := decodeFrame(ev.Data, f)
		if !ok {
			continue
		}
		if !accepts(frame, filter, src) {
			continue
		}
		select {
		case jobs <- job{frame: frame}:
		default:
			*dropped++
			if *dropped == 1 || *dropped%dropWarnEvery == 0 {
				noteLine(f, "warning: local handler is slow; dropped %d event(s)\n", *dropped)
			}
		}
	}
}

func decodeFrame(data []byte, f *cmdutil.Factory) (Frame, bool) {
	frame, err := Decode(data)
	if err != nil {
		noteLine(f, "skipped a malformed event from the hub\n")
		return Frame{}, false
	}
	return frame, true
}

func accepts(frame Frame, filter map[string]bool, src Source) bool {
	if len(filter) > 0 && !filter[frame.Type] {
		return false
	}
	if src.Accept != nil && !src.Accept(frame) {
		return false
	}
	return true
}

// nextJob prefers neither queue: favouring a busy stream would starve retries. A nil channel is never ready.
func nextJob(ctx context.Context, jobs, retries <-chan job) (job, bool) {
	select {
	case <-ctx.Done():
		return job{}, false
	case j := <-jobs:
		return j, true
	case j := <-retries:
		return j, true
	}
}

func forwardEvent(ctx context.Context, j job, fwd *forward.Forwarder, src Source, opts Options, f *cmdutil.Factory) (*forward.Result, error) {
	headers := OutboundHeaders(j.frame, WithholdCredentialsFor(fwd, opts.ForwardCredentials))
	res, err := fwd.Send(ctx, j.frame.Body(), headers)
	renderForward(f, src, j, fwd.Target(), headers, opts, res, err)
	return res, err
}

// WithholdCredentialsFor reports whether credential headers must be dropped for fwd: any
// non-loopback target (https proves secrecy, not identity) unless --forward-credentials, and
// always for an unverified certificate. Signature headers are never withheld.
func WithholdCredentialsFor(fwd *forward.Forwarder, forwardCredentials bool) bool {
	if fwd.IsUnverifiedTLS() {
		return true
	}
	if fwd.IsLoopback() {
		return false
	}
	// Plaintext off-machine stays refused even with the opt-in.
	if fwd.IsPlaintextRemote() {
		return true
	}
	return !forwardCredentials
}

// WithholdReason names why credentials are withheld, without suggesting a flag that cannot help.
func WithholdReason(fwd *forward.Forwarder) string {
	if fwd.IsUnverifiedTLS() {
		return "withholding credential headers (Authorization, X-Secret-Key) while TLS verification is off; use a trusted certificate or plain http on loopback to replay them"
	}
	if fwd.IsPlaintextRemote() {
		return "withholding credential headers (Authorization, X-Secret-Key) over plaintext http; use https or a loopback address to replay them"
	}
	return "withholding credential headers (Authorization, X-Secret-Key) from a non-loopback handler; pass --forward-credentials to replay them"
}

// OutboundHeaders returns fr's headers to replay, minus credentials when withhold is set.
func OutboundHeaders(fr Frame, withhold bool) http.Header {
	headers := DeliveryHeaders(fr)
	if withhold {
		withholdCredentials(headers)
	}
	return headers
}

// Tag renders a sanitized trailing dim label, "" when value is empty.
func Tag(io *iostreams.IOStreams, key, value string) string {
	if value == "" {
		return ""
	}
	return "   " + io.Dim(key+"="+jsonfmt.Clean(value))
}

// PayloadString returns the string or number at path in fr's payload as text, "" otherwise.
func PayloadString(fr Frame, path ...string) string {
	var node any
	if err := json.Unmarshal(fr.Payload, &node); err != nil {
		return ""
	}
	for _, key := range path {
		obj, ok := node.(map[string]any)
		if !ok {
			return ""
		}
		node = obj[key]
	}
	switch v := node.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// ShortID trims an id to its first dash-separated segment.
func ShortID(id string) string {
	if i := strings.Index(id, "-"); i > 0 {
		return id[:i]
	}
	return id
}

// DeliveryHeaders returns a frame's headers minus hop-by-hop ones, defaulting Content-Type to JSON.
func DeliveryHeaders(frame Frame) http.Header {
	headers := http.Header{}
	for k, v := range frame.Headers {
		if isHopByHopHeader(k) {
			continue
		}
		headers.Set(k, v)
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	return headers
}

func isHopByHopHeader(k string) bool {
	switch http.CanonicalHeaderKey(k) {
	case "Host", "Content-Length", "Connection", "Keep-Alive", "Proxy-Connection",
		"Transfer-Encoding", "Te", "Trailer", "Upgrade":
		return true
	}
	return false
}

// reconnectPhase lets the retry line say something true: only a dropped stream is "connection lost".
type reconnectPhase int

const (
	phaseMint reconnectPhase = iota
	phaseOpen
	phaseStream
)

// reconnectReason: a 429 on subscribe is the hub's per-channel cap; a 429 on mint with
// sse_hub_session_limit is the account's session cap, which clears only as sessions expire.
func reconnectReason(phase reconnectPhase, cause error) string {
	var re *sse.RetryableError
	if errors.As(cause, &re) && re.StatusCode == http.StatusTooManyRequests {
		return "hub connection limit reached for this channel; close another listener"
	}
	var he *api.HTTPError
	if errors.As(cause, &he) && he.StatusCode == http.StatusTooManyRequests {
		if he.Code == "sse_hub_session_limit" {
			return "listen session limit reached for this account; close another listener or wait for an idle session to expire"
		}
		return "rate limited by SePay"
	}
	var kr *sse.KeyRefusedError
	if errors.As(cause, &kr) {
		return "the hub refused this listen session; requesting a new one"
	}
	switch phase {
	case phaseMint:
		return "cannot reach SePay to open a listen session"
	case phaseOpen:
		return "cannot reach the hub"
	}
	return "connection lost"
}

// reconnectOrFail returns nil to reconnect after a backoff, or the error to stop with.
func reconnectOrFail(ctx context.Context, f *cmdutil.Factory, attempt *int, maxAttempts int, phase reconnectPhase, cause error) error {
	ok, retryAfter := retryable(cause)
	if !ok {
		return cause
	}
	*attempt++
	if maxAttempts > 0 && *attempt > maxAttempts {
		return cause
	}
	wait := jitter(api.Backoff(retryAfter, *attempt-1))
	noteLine(f, "%s, retrying (attempt %d)...\n", reconnectReason(phase, cause), *attempt)
	select {
	case <-time.After(wait):
		return nil
	case <-ctx.Done():
		return context.Canceled
	}
}

// retryable treats a portal 401/403 as fatal so its exit code surfaces; a hub key refusal is transient.
func retryable(err error) (bool, string) {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, sse.ErrHeartbeatTimeout):
		return true, ""
	case errors.Is(err, context.Canceled):
		return false, ""
	}
	var re *sse.RetryableError
	if errors.As(err, &re) {
		return true, re.RetryAfter
	}
	var kr *sse.KeyRefusedError
	if errors.As(err, &kr) {
		return true, ""
	}
	// The refresh never reached the host, so the token was stale: retry.
	if errors.Is(err, cmdutil.ErrTokenRefreshUnreachable) {
		return true, ""
	}
	var he *api.HTTPError
	if errors.As(err, &he) {
		// sse_hub_disabled is a kill switch, not a blip.
		if he.StatusCode == http.StatusServiceUnavailable && he.Code == "sse_hub_disabled" {
			return false, ""
		}
		return he.StatusCode == http.StatusTooManyRequests || he.StatusCode == http.StatusServiceUnavailable, ""
	}
	return true, ""
}

func unwrapClean(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// jitter adds up to 25% slack. crypto/rand: the repo uses no math/rand.
func jitter(d time.Duration) time.Duration {
	span := int64(d) / 4
	if span <= 0 {
		return d
	}
	n, err := rand.Int(rand.Reader, big.NewInt(span))
	if err != nil {
		return d
	}
	return d + time.Duration(n.Int64())
}

// sensitivePayloadKeys never reach stdout; matched by name at any depth.
var sensitivePayloadKeys = map[string]bool{
	"card_number":      true,
	"card_holder_name": true,
	"card_expiry":      true,
	"cvv":              true,
}

// redactPayload yields a placeholder, never raw bytes, for an undecodable payload.
func redactPayload(payload json.RawMessage) []byte {
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return []byte("<unprintable payload>")
	}
	// Escaping off: Marshal would emit the marker as <redacted>.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactValue(decoded)); err != nil {
		return []byte("<unprintable payload>")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if sensitivePayloadKeys[strings.ToLower(k)] {
				t[k] = "<redacted>"
				continue
			}
			t[k] = redactValue(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = redactValue(child)
		}
		return t
	}
	return v
}

// credentialHeaders are replayed verbatim, never printed; a substring net catches new auth types.
var credentialHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"X-Secret-Key":        true,
	"X-Api-Key":           true,
	"Api-Key":             true,
	"Cookie":              true,
}

// isCredentialHeader excludes signatures: the handler needs them to verify.
func isCredentialHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	if credentialHeaders[canonical] {
		return true
	}
	lower := strings.ToLower(canonical)
	for _, needle := range []string{"secret", "token", "password", "credential"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func displayHeaderValue(name, value string) string {
	if isCredentialHeader(name) {
		return "<redacted>"
	}
	return value
}

func withholdCredentials(h http.Header) {
	for name := range h {
		if isCredentialHeader(name) {
			h.Del(name)
		}
	}
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, s := range items {
		set[s] = true
	}
	return set
}

func printListening(f *cmdutil.Factory, src Source, s *Session, fwd *forward.Forwarder, control bool) {
	envWord := s.Environment
	if envWord == "" {
		envWord = "sandbox"
	}
	noun := src.Noun
	if noun == "" {
		noun = "events"
	}
	keys := "Ctrl+C to stop"
	if control {
		keys += " · retries+enter to manage"
	}
	noteLine(f, "%s Listening for %s %s → %s   (%s)\n",
		f.IOStreams.Green("✓"), envWord, noun, fwd.Target(), keys)
}

// channelScope keys coordination by product, env and account (not profile): two names for
// one login must coordinate, two logins must not. Empty when the product named no scope.
func channelScope(f *cmdutil.Factory, scope string) string {
	if scope == "" {
		return ""
	}
	return strings.Join([]string{scope, f.ActiveEnv(), f.Account()}, "\x00")
}

// noteLine writes under the screen lock so it never lands inside a control block.
func noteLine(f *cmdutil.Factory, format string, args ...any) {
	f.IOStreams.Block(func() { fmt.Fprintf(f.IOStreams.ErrOut, format, args...) })
}

// deliveryStream: an unreachable handler goes to stderr, an answer to stdout.
func deliveryStream(s *iostreams.IOStreams, err error) io.Writer {
	if err != nil {
		return s.ErrOut
	}
	return s.Out
}

func renderForward(f *cmdutil.Factory, src Source, j job, target string, sent http.Header, opts Options, res *forward.Result, err error) {
	if err != nil && errors.Is(err, context.Canceled) {
		return // Ctrl+C mid-forward: a clean shutdown, not a handler fault
	}
	ios := f.IOStreams

	// Attempt and tag ride the status line: --quiet drops the retry note.
	line := deliveryLine{event: j.frame.Type, target: target}
	if j.attempt > 0 {
		line.attempt = "   " + ios.Dim(fmt.Sprintf("(retry %d/%d)", j.attempt, opts.ForwardRetries))
	}
	if src.Tag != nil {
		line.tag = src.Tag(ios, j.frame)
	}

	renderDelivery(f, line, res, err, func(w io.Writer) {
		if opts.Headers {
			for k, vs := range sent {
				for _, v := range vs {
					cmdutil.Fprintf(w, "    %s: %s\n", k, displayHeaderValue(k, v))
				}
			}
		}
		if opts.Print {
			cmdutil.Fprintf(w, "    %s\n", string(redactPayload(j.frame.Payload)))
		}
	})
}

type deliveryLine struct {
	event   string
	target  string
	attempt string // "   (retry 3/7)", or empty for a first delivery
	tag     string // the product's trailing tag, already colored
}

// renderDelivery is the one render path for forwarded and mirrored lines, held under the screen lock.
func renderDelivery(f *cmdutil.Factory, line deliveryLine, res *forward.Result, err error, extra func(io.Writer)) {
	ios := f.IOStreams
	ts := time.Now().Format("15:04:05")
	w := deliveryStream(ios, err)
	// Read per delivery so lines fit after a resize.
	width := ios.TerminalWidth(w)
	fitted := func(format string, args ...any) {
		var b strings.Builder
		cmdutil.Fprintf(&b, format, args...)
		fmt.Fprintln(w, jsonfmt.Truncate(b.String(), width))
	}

	ios.Block(func() {
		if err != nil {
			fitted("%s  %s   → %s   %s%s%s", ts, line.event, line.target,
				ios.Red("not responding (is your handler running?)"), line.attempt, line.tag)
			return
		}

		status := fmt.Sprintf("%d", res.StatusCode)
		if res.OK() {
			status = ios.Green(status)
		} else {
			status = ios.Red(status)
		}
		fitted("%s  %s   → %s   %s   %dms%s%s", ts, line.event, line.target,
			status, res.Latency.Milliseconds(), line.attempt, line.tag)

		if !res.OK() && len(res.Body) > 0 {
			fitted("    %s", string(res.Body))
		}
		if extra != nil {
			extra(w)
		}
	})
}
