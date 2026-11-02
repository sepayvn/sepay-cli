package webhook

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/sepayvn/sepay-cli/internal/forward"
	"github.com/sepayvn/sepay-cli/internal/sse"
	bankaccount "github.com/sepayvn/sepay-cli/pkg/cmd/bank-account"
	"github.com/sepayvn/sepay-cli/pkg/cmd/sandbox"
	vapkg "github.com/sepayvn/sepay-cli/pkg/cmd/va"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/listen"

	"github.com/spf13/cobra"
)

type testOptions struct {
	forward      string
	forwardCreds bool
	webhook      string
	event        string
	bankAccount  string
	va           string
	amount       int64
	timeout      time.Duration
	expectStatus int
}

type testResult struct {
	Event      string `json:"event" yaml:"event"`
	StatusCode int    `json:"status_code" yaml:"status_code"`
	LatencyMs  int64  `json:"latency_ms" yaml:"latency_ms"`
	OK         bool   `json:"ok" yaml:"ok"`
}

const (
	simulateAmountMin = 1000
	simulateAmountMax = 499_000_000
)

func checkAmount(amount int64) error {
	if amount == 0 || (amount >= simulateAmountMin && amount <= simulateAmountMax) {
		return nil
	}
	return &cmdutil.FlagError{Err: fmt.Errorf("--amount must be between %d and %d", simulateAmountMin, simulateAmountMax)}
}

// resolveTestOptions pins sandbox (simulate has no production equivalent) and fills required inputs.
func resolveTestOptions(cmd *cobra.Command, f *cmdutil.Factory, in testOptions) (testOptions, error) {
	if err := sandbox.EnsureSandboxEnv(f); err != nil {
		return testOptions{}, err
	}
	if err := checkAmount(in.amount); err != nil {
		return testOptions{}, err
	}
	// SelectOrFlag passes values through, so validate here to fail as a flag error (exit 2), not a server one.
	if err := cmdutil.ValidateChoices(cmd, map[string][]string{"event": sandbox.EventTypes()}); err != nil {
		return testOptions{}, err
	}

	ctx := cmd.Context()
	forwardAddr, err := f.InputOrFlag(in.forward, "forward", "Local handler address")
	if err != nil {
		return testOptions{}, err
	}
	event, err := f.SelectOrFlag(in.event, "event", "Event type", sandbox.EventTypes())
	if err != nil {
		return testOptions{}, err
	}
	baXID, err := resolveBankAccount(ctx, f, in.bankAccount)
	if err != nil {
		return testOptions{}, err
	}
	amount, err := sandbox.ResolveAmount(f, in.amount, cmd.Flags().Changed("amount"))
	if err != nil {
		return testOptions{}, err
	}
	if err := checkAmount(amount); err != nil {
		return testOptions{}, err
	}
	va := in.va
	if event == eventVACredit {
		if va, err = vapkg.PickNumber(ctx, f, baXID, in.va, "va", "Virtual account"); err != nil {
			return testOptions{}, err
		}
	}
	out := in
	out.forward, out.event, out.bankAccount, out.va, out.amount = forwardAddr, event, baXID, va, amount
	return out, nil
}

func resolveBankAccount(ctx context.Context, f *cmdutil.Factory, val string) (string, error) {
	if val != "" {
		return val, nil
	}
	client, err := f.NewAPIClient()
	if err != nil {
		return "", err
	}
	accts, err := bankaccount.FetchAll(ctx, client)
	if err != nil {
		return "", err
	}
	if len(accts) == 1 {
		return accts[0].ID, nil
	}
	return bankaccount.PickOne(ctx, f, "", "bank-account", "Bank account")
}

// runTest fires a sandbox event, waits for it on the stream, forwards it once and asserts the response.
// A wrong or missing handler response exits 6.
func runTest(ctx context.Context, f *cmdutil.Factory, opts testOptions) error {
	fwd, err := forward.NewForwarder(opts.forward, false)
	if err != nil {
		return &cmdutil.FlagError{Err: err}
	}
	if !fwd.IsLoopback() {
		fmt.Fprintf(f.IOStreams.ErrOut, "warning: forwarding to a non-loopback address (%s)\n", fwd.Target())
	}
	if listen.WithholdCredentialsFor(fwd, opts.forwardCreds) {
		fmt.Fprintf(f.IOStreams.ErrOut, "warning: %s\n", listen.WithholdReason(fwd))
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}

	// A filtered-out event would be reported as a handler timeout.
	if err := assertDeliverable(ctx, client, opts); err != nil {
		return err
	}

	// Subscribe before firing: the hub live-tails with no replay.
	session, err := listen.Mint(ctx, client, mintPath)
	if err != nil {
		return err
	}
	// A never-subscribed key escapes the hub's auto-revoke, so a pre-Open failure must revoke it.
	if err := cmdutil.ValidateCredentialOrigin(session.HubURL); err != nil {
		listen.Revoke(ctx, client, mintPath, session.ListenKey)
		return cmdutil.ServerValueError("hub url", err)
	}
	if err := cmdutil.AssertHubHost(f.ActiveEnv(), session.HubURL, "hub_url in the mint response"); err != nil {
		listen.Revoke(ctx, client, mintPath, session.ListenKey)
		return cmdutil.ServerValueError("hub url", err)
	}
	stream, err := sse.NewClient().Open(ctx, session.HubURL, session.ListenKey, time.Duration(session.Heartbeat)*time.Second)
	if err != nil {
		listen.Revoke(ctx, client, mintPath, session.ListenKey)
		return err
	}
	defer func() { _ = stream.Close() }()

	if _, err := sandbox.Simulate(ctx, client, sandbox.SimulateBody{
		Type:           opts.event,
		Amount:         opts.amount,
		BankAccountXID: opts.bankAccount,
		VANumber:       opts.va,
	}); err != nil {
		return err
	}
	f.ErrPrintf("Fired %s event; waiting for the handler...\n", opts.event)

	waitCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	frame, err := awaitEvent(waitCtx, stream, opts)
	if err != nil {
		return err
	}
	return forwardOnce(ctx, f, fwd, frame, opts.expectStatus, listen.WithholdCredentialsFor(fwd, opts.forwardCreds))
}

// matches requires both the direction and the VA: the hub types a VA credit as payment_in.
func (o testOptions) matches(fr listen.Frame) bool {
	if o.event == eventVACredit {
		return fr.Type == eventPaymentIn && subAccount(fr) == o.va
	}
	return fr.Type == o.event
}

// awaitEvent reads the stream until the fired event arrives. One transaction fans out to every matching webhook,
// so opts.webhook pins which one is judged; an empty WebhookXID (older backend) still matches.
func awaitEvent(ctx context.Context, stream *sse.Stream, opts testOptions) (listen.Frame, error) {
	var skipped int
	var otherOnVA string // non-incoming frame type seen on --va
	for {
		ev, err := stream.Read(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				switch {
				case otherOnVA != "":
					return listen.Frame{}, fmt.Errorf(
						"timed out waiting for a %s on virtual account %s; a %s event on it arrived instead",
						opts.event, opts.va, otherOnVA)
				case skipped > 0:
					return listen.Frame{}, fmt.Errorf(
						"timed out waiting for a %s event on webhook %s; %d delivery(ies) for other webhooks arrived instead",
						opts.event, opts.webhook, skipped)
				}
				return listen.Frame{}, fmt.Errorf("timed out waiting for the %s event", opts.event)
			}
			return listen.Frame{}, err
		}
		if ev.Name != listen.FrameEventName {
			continue
		}
		fr, err := listen.Decode(ev.Data)
		if err != nil {
			continue
		}
		if !opts.matches(fr) {
			if opts.event == eventVACredit && subAccount(fr) == opts.va {
				otherOnVA = fr.Type
			}
			continue
		}
		if xid := webhookXID(fr); opts.webhook != "" && xid != "" && xid != opts.webhook {
			skipped++
			continue
		}
		return fr, nil
	}
}

// forwardOnce POSTs the event to the handler; a transport failure or unexpected status returns a SilentError (exit 6).
func forwardOnce(ctx context.Context, f *cmdutil.Factory, fwd *forward.Forwarder, frame listen.Frame, expectStatus int, withholdCreds bool) error {
	// The signature covers raw_body, not the re-encoded payload.
	res, err := fwd.Send(ctx, frame.Body(), listen.OutboundHeaders(frame, withholdCreds))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		_ = f.RenderOutput(testResult{Event: frame.Type, OK: false}, func() error {
			f.ErrPrintf("handler %s: %v\n", f.IOStreams.Red("did not respond"), err)
			return nil
		})
		return cmdutil.SilentError{Code: cmdutil.ExitForward}
	}

	ok := res.OK()
	if expectStatus != 0 {
		ok = res.StatusCode == expectStatus
	}
	result := testResult{Event: frame.Type, StatusCode: res.StatusCode, LatencyMs: res.Latency.Milliseconds(), OK: ok}
	if !ok {
		want := "2xx"
		if expectStatus != 0 {
			want = strconv.Itoa(expectStatus)
		}
		_ = f.RenderOutput(result, func() error {
			f.ErrPrintf("handler returned %s, want %s\n", f.IOStreams.Red(strconv.Itoa(res.StatusCode)), want)
			if len(res.Body) > 0 {
				f.ErrPrintf("    %s\n", string(res.Body))
			}
			// Several webhooks may match; name the one judged.
			if xid := webhookXID(frame); xid != "" {
				f.ErrPrintf("    judged the delivery for webhook %s; pin it with --webhook <xid>\n", xid)
			}
			return nil
		})
		return cmdutil.SilentError{Code: cmdutil.ExitForward}
	}

	return f.RenderOutput(result, func() error {
		fmt.Fprintf(f.IOStreams.ErrOut, "%s handler responded %s in %dms\n",
			f.SuccessIcon(), f.IOStreams.Green(strconv.Itoa(res.StatusCode)), res.Latency.Milliseconds())
		return nil
	})
}
