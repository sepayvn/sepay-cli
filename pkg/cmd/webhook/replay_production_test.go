package webhook

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const replayTx = `{"status":"success","data":{"id":"tx-1","transaction_date":"2026-11-01 12:00:00","account_number":"01","va":null,"transfer_type":"in","amount_in":2500000,"amount_out":0,"accumulated":null,"transaction_content":"DH123","reference_number":null,"code":"DH123","bank_brand_name":"ACB","bank_account_id":"ba_1","va_id":null,"webhook_success":0}}`

// replayServer answers the production preview reads and records each replay POST body.
type replayServer struct {
	reads []string
	posts []map[string]any
}

func (s *replayServer) handler(postStatus int, postBody string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/transactions/"):
			s.reads = append(s.reads, r.URL.Path)
			_, _ = w.Write([]byte(replayTx))
		case r.Method == http.MethodGet && r.URL.Path == "/webhook-logs":
			s.reads = append(s.reads, r.URL.Path+"?"+r.URL.RawQuery)
			_, _ = w.Write([]byte(logsPage(logRow)))
		case r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.posts = append(s.posts, body)
			w.WriteHeader(postStatus)
			_, _ = w.Write([]byte(postBody))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestReplay_ProductionShowsWhatItResendsBeforeTheConfirm(t *testing.T) {
	srv := &replayServer{}
	f, _, errOut := testFactory(t, srv.handler(http.StatusOK, replayOK))
	f.Global.Env = config.EnvProduction
	f.Global.Yes = true

	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1"))

	s := errOut.String()
	assert.Contains(t, s, "Transaction  tx-1")
	assert.Contains(t, s, "Amount       +2.500.000")
	assert.Contains(t, s, "Sent to      https://x.test/hook")
	assert.Contains(t, s, "Last result  failed · HTTP 500 · 2026-07-15 10:00:00")
	assert.Contains(t, s, "! a handler that does not dedupe may credit this order twice")
	assert.Contains(t, s, "! live: replay transaction tx-1 to webhook wh-1 targets PRODUCTION")
	assert.Less(t, strings.Index(s, "Transaction  tx-1"), strings.Index(s, "! live:"), "the preview comes before the confirm")
	assert.Equal(t, []string{"/transactions/tx-1", "/webhook-logs?per_page=1&transaction_id=tx-1&webhook_xid=wh-1"}, srv.reads)
	require.Len(t, srv.posts, 1)
	assert.Equal(t, map[string]any{"transaction_id": "tx-1", "webhook_xid": "wh-1"}, srv.posts[0], "no force unless asked")
}

// With stderr redirected on a terminal the question draws on stdout; the preview must be read
// where the question is answered, not in the log file.
func TestReplay_ProductionPreviewSharesTheConfirmStream(t *testing.T) {
	srv := &replayServer{}
	f, out, errOut := testFactory(t, srv.handler(http.StatusOK, replayOK))
	f.Global.Env = config.EnvProduction
	prompttest.Use(t, f, prompttest.Step{Label: "replay transaction tx-1 to webhook wh-1 on production?", Answer: true})
	f.IOStreams.SetStderrTTY(false)

	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1"))

	s := out.String()
	assert.Contains(t, s, "Last result  failed · HTTP 500")
	assert.Contains(t, s, "! a handler that does not dedupe may credit this order twice")
	assert.Less(t, strings.Index(s, "Last result"), strings.Index(s, "! live:"), "the preview comes before the confirm")
	assert.NotContains(t, errOut.String(), "Last result")
}

func TestReplay_ProductionWithoutYesOrTerminalIsUsageError(t *testing.T) {
	srv := &replayServer{}
	f, _, _ := testFactory(t, srv.handler(http.StatusOK, replayOK))
	f.Global.Env = config.EnvProduction

	err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Empty(t, srv.reads, "a confirm that cannot be answered needs no preview")
	assert.Empty(t, srv.posts)
}

// Without the preview reads, a token lacking "read" still gets the usage error, not exit 5.
func TestReplay_ProductionUnattendedSkipsTheReadsItCannotUse(t *testing.T) {
	var reads int
	f, _, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		reads++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"insufficient_scope","message":"missing scope: read"}`))
	})
	f.Global.Env = config.EnvProduction

	err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.Contains(t, err.Error(), "ask the user to run this command in their own terminal")
	assert.NotContains(t, err.Error(), "--yes")
	assert.Zero(t, reads)
}

// Machine output carries no preview; --yes is the confirm and the replay still goes.
func TestReplay_ProductionJSONSkipsThePreview(t *testing.T) {
	srv := &replayServer{}
	f, _, errOut := testFactory(t, srv.handler(http.StatusOK, replayOK))
	f.Global.Env = config.EnvProduction
	f.Global.Yes, f.Global.Output = true, "json"

	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1"))

	assert.Empty(t, srv.reads)
	assert.NotContains(t, errOut.String(), "Last result")
	assert.Len(t, srv.posts, 1)
}

// The preview guards against a double credit, so a read it cannot make stops the replay.
func TestReplay_ProductionPreviewReadFailureStopsTheReplay(t *testing.T) {
	var posts int
	f, _, _ := testFactory(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			_, _ = w.Write([]byte(replayOK))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"insufficient_scope","message":"missing scope: read"}`))
	})
	f.Global.Env = config.EnvProduction
	f.Global.Yes = true

	err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitScope, cmdutil.ExitCodeForError(err))
	assert.Zero(t, posts)
}

func TestReplay_ForceOnProductionNeedsATerminalEvenWithYes(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*cmdutil.GlobalOptions)
	}{
		{"yes", func(g *cmdutil.GlobalOptions) { g.Yes = true }},
		{"no-input", func(g *cmdutil.GlobalOptions) { g.Yes, g.NoInput = true, true }},
		{"json", func(g *cmdutil.GlobalOptions) { g.Yes, g.Output = true, "json" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &replayServer{}
			f, _, _ := testFactory(t, srv.handler(http.StatusOK, replayOK))
			f.Global.Env = config.EnvProduction
			tc.set(f.Global)

			err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1", "--force")

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "--yes does not confirm it")
			assert.Contains(t, err.Error(), "ask the user to run this command in their own terminal")
			assert.Empty(t, srv.reads, "refused before any request")
			assert.Empty(t, srv.posts)
		})
	}
}

// At a terminal the user is already there, so the refusal names the flags that keep the prompt away.
func TestReplay_ForceOnProductionAtATerminalNamesTheFlagsToDrop(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*cmdutil.GlobalOptions)
	}{
		{"json", func(g *cmdutil.GlobalOptions) { g.Yes, g.Output = true, "json" }},
		{"quiet", func(g *cmdutil.GlobalOptions) { g.Yes, g.Quiet = true, true }},
		{"no-input", func(g *cmdutil.GlobalOptions) { g.Yes, g.NoInput = true, true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &replayServer{}
			f, _, _ := testFactory(t, srv.handler(http.StatusOK, replayOK))
			f.IOStreams.SetStdinTTY(true)
			f.IOStreams.SetStdoutTTY(true)
			f.Global.Env = config.EnvProduction
			tc.set(f.Global)

			err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1", "--force")

			require.Error(t, err)
			assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
			assert.Contains(t, err.Error(), "--yes does not confirm it; run it without --yes, --json, -o yaml, --quiet or --no-input")
			assert.NotContains(t, err.Error(), "ask the user")
			assert.Empty(t, srv.reads, "refused before any request")
			assert.Empty(t, srv.posts)
		})
	}
}

func TestReplay_ForceOnProductionAsksForTheTransactionID(t *testing.T) {
	for _, tc := range []struct {
		typed    string
		wantPost bool
	}{
		{"tx-1", true},
		{"tx-2", false},
		{"", false},
	} {
		t.Run("typed "+tc.typed, func(t *testing.T) {
			srv := &replayServer{}
			f, _, _ := testFactory(t, srv.handler(http.StatusOK, replayOK))
			f.Global.Env = config.EnvProduction
			prompttest.Use(t, f,
				prompttest.Step{Label: "replay transaction tx-1 to webhook wh-1 on production?", Answer: true},
				prompttest.Step{Label: "Type tx-1 to confirm", Answer: tc.typed},
			)

			err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1", "--force")

			if !tc.wantPost {
				require.Error(t, err)
				assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
				assert.Contains(t, err.Error(), "cancelled")
				assert.Empty(t, srv.posts)
				return
			}
			require.NoError(t, err)
			require.Len(t, srv.posts, 1)
			assert.Equal(t, true, srv.posts[0]["force"])
		})
	}
}

// The server, not the CLI, decides whether the last delivery was acknowledged: a 2xx in the
// log does not say whether the body carried success:true.
func TestReplay_AlreadyDeliveredPointsAtForce(t *testing.T) {
	srv := &replayServer{}
	f, _, _ := testFactory(t, srv.handler(http.StatusConflict,
		`{"status":"error","error_code":"replay_already_delivered","message":"The last delivery succeeded at 2026-11-01 12:30:00; pass force to send it again"}`))
	f.Global.Env = config.EnvProduction
	f.Global.Yes = true

	err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, handled(err), "already succeeded; replay it anyway with --force (Reason: The last delivery succeeded at 2026-11-01 12:30:00")
}

func TestReplay_RateLimitNamesWhenToRetry(t *testing.T) {
	srv := &replayServer{}
	f, _, _ := testFactory(t, srv.handler(http.StatusTooManyRequests,
		`{"status":"error","error_code":"replay_rate_limited","message":"This transaction was replayed 3 times in the last 60 minutes; retry in 42 minutes"}`))
	f.Global.Yes = true

	err := run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1")

	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(err))
	assert.Contains(t, handled(err), "this transaction reached its replay limit (Reason: This transaction was replayed 3 times in the last 60 minutes; retry in 42 minutes)")
}

// A sandbox replay has no preview: no transaction read, nothing printed before the confirm.
func TestReplay_SandboxHasNoPreview(t *testing.T) {
	srv := &replayServer{}
	f, _, errOut := testFactory(t, srv.handler(http.StatusOK, replayOK))
	f.Global.Yes = true

	require.NoError(t, run(NewCmdWebhook(f), "replay", "tx-1", "--webhook", "wh-1", "--force"))

	assert.Empty(t, srv.reads)
	assert.NotContains(t, errOut.String(), "Last result")
	assert.Equal(t, true, srv.posts[0]["force"], "sandbox passes force through; the server ignores it there")
}

func handled(err error) string {
	ios, _, _, errOut := iostreams.Test()
	cmdutil.HandleError(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}, err)
	return errOut.String()
}
