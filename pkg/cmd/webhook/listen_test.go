package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"
	"github.com/sepayvn/sepay-cli/pkg/listen"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func factoryForAPI(url string) (*cmdutil.Factory, *bytes.Buffer, *bytes.Buffer) {
	ios, _, out, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	f.NewAPIClient = func() (*api.Client, error) {
		c := api.NewClient(url, "test")
		c.MaxRetries = 0
		return c, nil
	}
	return f, out, errOut
}

// sseHub streams frames, then holds the connection until the client disconnects.
func sseHub(t *testing.T, frames ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer lk_test" {
			t.Errorf("subscribe auth = %q, want Bearer lk_test", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for _, f := range frames {
			_, _ = io.WriteString(w, f)
			fl.Flush()
		}
		<-r.Context().Done()
	}
}

func mintAPI(hubURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"status":"success","data":{"hub_url":%q,"listen_key":"lk_test","environment":"sandbox","heartbeat":15}}`, hubURL)
	}
}

type lineWriter struct{ ch chan string }

func (w lineWriter) Write(p []byte) (int, error) {
	w.ch <- string(p)
	return len(p), nil
}

func TestListen_ForwardsEventThenExitsOnCancel(t *testing.T) {
	var gotBody []byte
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {\"environment\":\"sandbox\"}\n\n",
		"event: webhook\ndata: {\"id\":\"evt_1\",\"type\":\"payment_in\",\"payload\":{\"transferAmount\":100000}}\n\n",
	))
	defer hub.Close()

	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	lines := make(chan string, 16)
	f.IOStreams.Out = lineWriter{lines}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL})

	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	waitFor(t, lines, "payment_in") // event forwarded and rendered
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("command did not exit after cancel")
	}
	assert.Contains(t, string(gotBody), "transferAmount")
}

// safeBuf is safe for the read loop and forwarder writing ErrOut concurrently.
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestListen_SlowHandlerDropsNotStall(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	frames := []string{"event: ready\ndata: {}\n\n"}
	for i := 0; i < listen.ForwardQueueSize*3; i++ {
		frames = append(frames, fmt.Sprintf("event: webhook\ndata: {\"id\":\"e%d\",\"type\":\"payment_in\",\"payload\":{}}\n\n", i))
	}
	hub := httptest.NewServer(sseHub(t, frames...))
	defer hub.Close()
	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	errBuf := &safeBuf{}
	f.IOStreams.ErrOut = errBuf
	f.IOStreams.Out = io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	deadline := time.After(3 * time.Second)
	for !strings.Contains(errBuf.String(), "dropped") {
		select {
		case <-deadline:
			t.Fatal("no drop warning: read loop stalled on the slow handler")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not exit after cancel")
	}
}

func TestListen_ReplaysHeaders(t *testing.T) {
	gotHeader := make(chan http.Header, 1)
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"headers\":{\"X-SePay-Signature\":\"sha256=abc\",\"Content-Type\":\"application/json\"},\"payload\":{\"x\":1}}\n\n",
	))
	defer hub.Close()
	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	f.IOStreams.Out = io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	select {
	case h := <-gotHeader:
		assert.Equal(t, "sha256=abc", h.Get("X-SePay-Signature"))
		assert.Equal(t, "application/json", h.Get("Content-Type"))
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not receive the forwarded event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not exit after cancel")
	}
}

func waitFor(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case line := <-lines:
			if strings.Contains(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("did not see %q on stdout", want)
		}
	}
}

func TestListen_EventsFilter(t *testing.T) {
	received := make(chan []byte, 2)
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: webhook\ndata: {\"type\":\"payment_out\",\"payload\":{\"marker\":\"OUT\"}}\n\n",
		"event: webhook\ndata: {\"type\":\"payment_in\",\"payload\":{\"marker\":\"IN\"}}\n\n",
	))
	defer hub.Close()

	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL, "--events", "payment_in"})

	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	select {
	case body := <-received:
		assert.Contains(t, string(body), "IN")
		assert.NotContains(t, string(body), "OUT", "payment_out must be filtered out")
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not receive the filtered event")
	}
	cancel()
	<-done
}

func TestListen_UnauthorizedExit4(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer hub.Close()

	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"listen", "--forward", "localhost:65535"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
}

// cuttingHub hands the nth subscribe (1-based) to onConnect; returning cuts the stream.
func cuttingHub(onConnect func(n int, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	var n atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: ready\ndata: {}\n\n")
		fl.Flush()
		onConnect(int(n.Add(1)), w, r)
	}
}

func TestListen_HubCutThenRevokedMintExits4(t *testing.T) {
	hub := httptest.NewServer(cuttingHub(func(_ int, _ http.ResponseWriter, _ *http.Request) {}))
	defer hub.Close()

	var mints atomic.Int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if mints.Add(1) > 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"status":"success","data":{"hub_url":%q,"listen_key":"lk_test","environment":"sandbox","heartbeat":15}}`, hub.URL)
	}))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	f.IOStreams.Out = io.Discard
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"listen", "--forward", "localhost:65535"})

	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
	case <-time.After(10 * time.Second):
		t.Fatal("listen did not stop after the hub cut a revoked session")
	}
}

// The hub also retires keys on TTL or auto-revoke, so a successful re-mint must resume.
func TestListen_HubCutRemintsAndKeepsListening(t *testing.T) {
	received := make(chan struct{}, 1)
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case received <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(cuttingHub(func(n int, w http.ResponseWriter, r *http.Request) {
		if n == 1 {
			return // first key retired
		}
		_, _ = io.WriteString(w, "event: webhook\ndata: {\"type\":\"payment_in\",\"payload\":{}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer hub.Close()

	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	f.IOStreams.Out = io.Discard
	ctx, cancel := context.WithCancel(context.Background())
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	select {
	case <-received:
	case <-time.After(10 * time.Second):
		t.Fatal("listen did not resume after the hub cut a still-valid session")
	}
	cancel()
	<-done
}

func TestListen_ReconnectMaxExhausted(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"listen", "--forward", "localhost:65535", "--reconnect-max", "1"})
	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, errOut.String(), "retrying (attempt 1)")
}

func TestListen_FlappingHubHonoursReconnectMax(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: ready\ndata: {}\n\n")
	}))
	defer hub.Close()
	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	f.IOStreams.Out = io.Discard
	cmd := NewCmdWebhook(f)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"listen", "--forward", "localhost:65535", "--reconnect-max", "2"})

	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(context.Background()) }()
	select {
	case err := <-done:
		require.Error(t, err, "a flapping hub must exhaust --reconnect-max")
	case <-time.After(15 * time.Second):
		t.Fatal("listen ignored --reconnect-max against a flapping hub (open-then-drop reset the counter)")
	}
	assert.Contains(t, errOut.String(), "retrying (attempt 2)")
}

func TestListen_ForwardsAllDeliveryHeaders(t *testing.T) {
	gotHeader := make(chan http.Header, 1)
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"headers\":{\"X-SePay-Signature\":\"sha256=abc\",\"Authorization\":\"Apikey real\",\"X-Secret-Key\":\"s3cr3t\",\"X-Custom\":\"keep\"},\"payload\":{\"x\":1}}\n\n",
	))
	defer hub.Close()
	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	f.IOStreams.Out = io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	select {
	case h := <-gotHeader:
		assert.Equal(t, "sha256=abc", h.Get("X-SePay-Signature"))
		assert.Equal(t, "Apikey real", h.Get("Authorization"))
		assert.Equal(t, "s3cr3t", h.Get("X-Secret-Key"))
		assert.Equal(t, "keep", h.Get("X-Custom"), "every delivery header is forwarded verbatim")
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not receive the forwarded event")
	}
	cancel()
	<-done
}

func TestListen_RefusesAHubURLOutsideTheActiveEnv(t *testing.T) {
	for _, hubURL := range []string{
		"https://ssehub.sepay.vn",
		"https://sepay.vn",
		"https://x.sepay.vn",
		"https://hub.example.com",
	} {
		t.Run(hubURL, func(t *testing.T) {
			var revoked bool
			apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					revoked = true
					w.WriteHeader(http.StatusNoContent)
					return
				}
				mintAPI(hubURL)(w, r)
			}))
			defer apiSrv.Close()

			f, _, _ := factoryForAPI(apiSrv.URL)
			cmd := NewCmdWebhook(f)
			cmd.SetArgs([]string{"listen", "--forward", "localhost:3000"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)

			err := cmd.ExecuteContext(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "refusing to send credentials to host")
			assert.Contains(t, err.Error(), "ssehub-sandbox.sepay.vn (sandbox)")
			assert.True(t, revoked, "the refused listen key is revoked")
		})
	}
}

func TestListen_AccessRevokedStopsWithoutRetry(t *testing.T) {
	var mints int
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mints++
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"error","error_code":"access_revoked","message":"Your access to this stream was revoked. Run: sepay login"}`))
	}))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", "localhost:3000"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(err))
	assert.Equal(t, 1, mints, "a revoked user is not retried")
	ios, _, _, errOut := iostreams.Test()
	cmdutil.HandleError(&cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}, err)
	assert.Contains(t, errOut.String(), "run: sepay login")
}

func TestListen_SkipTLSVerifyWithholdsCredentials(t *testing.T) {
	gotHeader := make(chan http.Header, 1)
	handler := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"headers\":{\"X-SePay-Signature\":\"sha256=abc\",\"Authorization\":\"Apikey real\",\"X-Secret-Key\":\"s3cr3t\"},\"payload\":{\"x\":1}}\n\n",
	))
	defer hub.Close()
	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, errOut := factoryForAPI(apiSrv.URL)
	f.IOStreams.Out = io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL, "--skip-tls-verify", "--forward-credentials"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	select {
	case h := <-gotHeader:
		assert.Equal(t, "sha256=abc", h.Get("X-SePay-Signature"), "the signature is not a credential")
		assert.Empty(t, h.Get("Authorization"))
		assert.Empty(t, h.Get("X-Secret-Key"))
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not receive the forwarded event")
	}
	cancel()
	<-done
	assert.Contains(t, errOut.String(), "TLS verification is off")
}

func TestListen_RejectsSkipTLSVerifyForANonLoopbackTarget(t *testing.T) {
	var minted bool
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		minted = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", "https://staging.example.com/hook", "--skip-tls-verify", "--forward-credentials"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--skip-tls-verify only applies to a loopback forward address")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
	assert.False(t, minted, "no request may leave before the flag is refused")
}

func TestListen_HeadersRedactsCredentials(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {}\n\n",
		"event: webhook\ndata: {\"id\":\"e1\",\"type\":\"payment_in\",\"headers\":{\"Authorization\":\"Bearer real\",\"X-Secret-Key\":\"s3cr3t\",\"X-SePay-Signature\":\"sha256=abc\",\"X-SePay-Timestamp\":\"1700\",\"Content-Type\":\"application/json\"},\"payload\":{\"x\":1}}\n\n",
	))
	defer hub.Close()
	apiSrv := httptest.NewServer(mintAPI(hub.URL))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	out := &safeBuf{}
	f.IOStreams.Out = out

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL, "--headers"})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "Content-Type")
	}, 3*time.Second, 20*time.Millisecond, "headers block never printed")
	cancel()
	<-done

	// Authenticating header values are redacted on stdout; the signature is shown.
	s := out.String()
	assert.NotContains(t, s, "Bearer real", "an Authorization value must not reach stdout")
	assert.NotContains(t, s, "s3cr3t", "a shared secret must not reach stdout")
	assert.Contains(t, s, "Authorization: <redacted>")
	assert.Contains(t, s, "X-Secret-Key: <redacted>")
	assert.Contains(t, s, "sha256=abc")
	assert.Contains(t, s, "X-Sepay-Timestamp: 1700")
	assert.Contains(t, s, "Content-Type: application/json")
}

func TestDescribeCoverage(t *testing.T) {
	list := func(hooks string) string {
		return `{"status":"success","data":[` + hooks + `],"meta":{"pagination":` +
			`{"total":1,"per_page":100,"current_page":1,"last_page":1,"has_more":false}}}`
	}
	hook := func(mode, xid string, active bool) string {
		return fmt.Sprintf(`{"id":"wh_1","name":"n","event_type":"All","authen_type":"No_Authen",`+
			`"webhook_url":"https://x.test/h","is_verify_payment":false,"skip_if_no_code":false,`+
			`"only_va":false,"bank_mode":%q,"bank_account_xid":%q,"bank_sub_account_xids":[],`+
			`"prefix_filters":[],"alert_threshold":null,"active":%t,"created_at":""}`, mode, xid, active)
	}

	cases := []struct {
		name     string
		body     string
		contains string
		silent   bool
	}{
		{"a bank-mode all webhook covers everything", list(hook("all", "", true)), "every bank account", false},
		{"no webhook at all", list(""), "No active webhook", false},
		{"only inactive webhooks", list(hook("all", "", false)), "No active webhook", false},
		{"a single-account webhook names what it covers", list(hook("single", "ba_7", true)), "ba_7", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})
			describeCoverage(context.Background(), f, fetchWebhookSet(context.Background(), f))
			if tc.silent {
				assert.Empty(t, errOut.String())
				return
			}
			assert.Contains(t, errOut.String(), tc.contains)
		})
	}
}

func TestDescribeCoverage_StaysSilentWhenTheListFails(t *testing.T) {
	f, _, errOut := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	describeCoverage(context.Background(), f, fetchWebhookSet(context.Background(), f))
	assert.Empty(t, errOut.String())
}

func TestAcceptWebhook(t *testing.T) {
	frame := func(xid string) listen.Frame {
		fr, err := listen.Decode([]byte(fmt.Sprintf(
			`{"id":"evt_1","type":"payment_in","webhook_xid":%q,"headers":{},"payload":{},"raw_body":"{}"}`, xid)))
		require.NoError(t, err)
		return fr
	}
	cases := []struct {
		name    string
		want    string
		frameID string
		accept  bool
	}{
		{"no filter forwards everything", "", "wh_a", true},
		{"filter keeps its own delivery", "wh_a", "wh_a", true},
		{"filter drops a sibling's delivery", "wh_a", "wh_b", false},
		// An older backend sends no webhook_xid; filtering on it would drop everything.
		{"unknown origin still passes", "wh_a", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accept := acceptWebhook(tc.want)
			if accept == nil {
				assert.True(t, tc.accept, "a nil filter accepts everything")
				return
			}
			assert.Equal(t, tc.accept, accept(frame(tc.frameID)))
		})
	}
}

func waitForErr(t *testing.T, buf *safeBuf, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !strings.Contains(buf.String(), want) {
		select {
		case <-deadline:
			t.Fatalf("did not see %q on stderr; got:\n%s", want, buf.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func deadTarget(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func listenAgainstDeadHandler(t *testing.T, extraArgs ...string) (*safeBuf, func()) {
	t.Helper()
	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {\"environment\":\"sandbox\"}\n\n",
		"event: webhook\ndata: {\"id\":\"evt_1\",\"type\":\"payment_in\",\"payload\":{}}\n\n",
	))
	apiSrv := httptest.NewServer(mintAPI(hub.URL))

	f, _, _ := factoryForAPI(apiSrv.URL)
	errBuf := &safeBuf{}
	f.IOStreams.ErrOut = errBuf
	f.IOStreams.Out = io.Discard

	ctx, cancel := context.WithCancel(context.Background())
	cmd := NewCmdWebhook(f)
	cmd.SetArgs(append([]string{"listen", "--forward", deadTarget(t)}, extraArgs...))
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	return errBuf, func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Error("command did not exit after cancel")
		}
		apiSrv.Close()
		hub.Close()
	}
}

func TestListen_ArmsARetryWhenTheHandlerIsUnreachable(t *testing.T) {
	errBuf, stop := listenAgainstDeadHandler(t)

	waitForErr(t, errBuf, "retry 1/7 in ~1m")
	assert.Contains(t, errBuf.String(), "replays the original signature")

	stop()
	assert.Contains(t, errBuf.String(), "1 event(s) still pending retry",
		"Ctrl+C abandons what is armed and says how much was lost")
}

func TestListen_NoRetryWhenDisabled(t *testing.T) {
	errBuf, stop := listenAgainstDeadHandler(t, "--forward-retries", "0")

	waitForErr(t, errBuf, "not responding")
	stop()

	assert.NotContains(t, errBuf.String(), "retry 1/")
	assert.NotContains(t, errBuf.String(), "pending retry")
}

func TestListen_RejectsAnOutOfRangeRetryCount(t *testing.T) {
	f, _, _ := factoryForAPI("http://127.0.0.1:1")
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", "localhost:3000", "--forward-retries", "99"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--forward-retries must be between 0 and 10")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// A negative cap would reach the check as "0 = unlimited".
func TestListen_RejectsANegativeReconnectCap(t *testing.T) {
	f, _, _ := factoryForAPI("http://127.0.0.1:1")
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", "localhost:3000", "--reconnect-max", "-1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--reconnect-max must be 0 or more")
	assert.Equal(t, cmdutil.ExitUsage, cmdutil.ExitCodeForError(err))
}

// One stream carries several webhooks, each with its own retry setting.
func TestRetryNon2xx_ReadsThePerWebhookSetting(t *testing.T) {
	on := Webhook{ID: "wh_on", Active: true, RetryConditions: &RetryConditions{Non2xxStatusCode: 1}}
	off := Webhook{ID: "wh_off", Active: true, RetryConditions: &RetryConditions{Non2xxStatusCode: 0}}
	// The API emits null, not a zeroed object.
	unset := Webhook{ID: "wh_unset", Active: true}

	frame := func(xid string) listen.Frame {
		raw := fmt.Sprintf(`{"webhook_xid":%q,"type":"payment_in","payload":{}}`, xid)
		fr, err := listen.Decode([]byte(raw))
		require.NoError(t, err)
		return fr
	}

	t.Run("nil when the list could not be read", func(t *testing.T) {
		assert.Nil(t, retryNon2xx(webhookSet{err: assert.AnError}),
			"an unreadable list must leave the channel on transport failures only")
	})

	t.Run("nil when the company has no webhook", func(t *testing.T) {
		assert.Nil(t, retryNon2xx(webhookSet{}))
	})

	t.Run("per webhook", func(t *testing.T) {
		pred := retryNon2xx(webhookSet{hooks: []Webhook{on, off, unset}})
		require.NotNil(t, pred)
		assert.True(t, pred(frame("wh_on")))
		assert.False(t, pred(frame("wh_off")))
		assert.False(t, pred(frame("wh_unset")), "a null retry_conditions is transport-only")
	})

	t.Run("an unnamed origin falls back to any webhook that enables it", func(t *testing.T) {
		assert.True(t, retryNon2xx(webhookSet{hooks: []Webhook{on, off}})(frame("")))
		assert.False(t, retryNon2xx(webhookSet{hooks: []Webhook{off, unset}})(frame("")))
		assert.True(t, retryNon2xx(webhookSet{hooks: []Webhook{on}})(frame("wh_gone")),
			"a webhook created after the session started is not on the list")
	})
}

func TestFetchWebhookSet_DecodesRetryConditions(t *testing.T) {
	body := webhookList(t,
		Webhook{ID: "wh_on", Active: true, RetryConditions: &RetryConditions{Non2xxStatusCode: 1}},
		Webhook{ID: "wh_unset", Active: true},
	)
	f, _, _ := testFactory(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	set := fetchWebhookSet(context.Background(), f)
	require.NoError(t, set.err)
	require.Len(t, set.hooks, 2)
	assert.True(t, set.hooks[0].retriesNon2xx())
	assert.Nil(t, set.hooks[1].RetryConditions, "an absent field stays nil, not a zero object")
	assert.False(t, set.hooks[1].retriesNon2xx())
}

func TestListen_RetriesANon2xxWhenTheWebhookAsksForIt(t *testing.T) {
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer handler.Close()

	hub := httptest.NewServer(sseHub(t,
		"event: ready\ndata: {\"environment\":\"sandbox\"}\n\n",
		"event: webhook\ndata: {\"id\":\"evt_1\",\"webhook_xid\":\"wh_on\",\"type\":\"payment_in\",\"payload\":{}}\n\n",
	))
	defer hub.Close()

	hooks := webhookList(t, Webhook{
		ID: "wh_on", Active: true, BankMode: "all", EventType: "All",
		RetryConditions: &RetryConditions{Non2xxStatusCode: 1},
	})
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/listen") {
			mintAPI(hub.URL)(w, r)
			return
		}
		_, _ = w.Write([]byte(hooks))
	}))
	defer apiSrv.Close()

	f, _, _ := factoryForAPI(apiSrv.URL)
	errBuf := &safeBuf{}
	f.IOStreams.ErrOut = errBuf
	outBuf := &safeBuf{}
	f.IOStreams.Out = outBuf

	ctx, cancel := context.WithCancel(context.Background())
	cmd := NewCmdWebhook(f)
	cmd.SetArgs([]string{"listen", "--forward", handler.URL})
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()

	deadline := time.After(3 * time.Second)
	for !strings.Contains(outBuf.String(), "retry 1/7") {
		select {
		case <-deadline:
			t.Fatalf("a non-2xx did not arm a retry; stdout:\n%s\nstderr:\n%s", outBuf.String(), errBuf.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// The handler answered, so the note follows the stdout delivery line.
	assert.NotContains(t, errBuf.String(), "retry 1/7")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("command did not exit after cancel")
	}
}

func TestWebhookTag_NamesTransactionWholeAndWebhookShort(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	fr, err := listen.Decode([]byte(`{"transaction_xid":"8f2c1a9e-1234-11f1-b996-fa163e2f7c98",` +
		`"webhook_xid":"a9bed278-8a2c-11f1-b996-fa163e2f7c98","payload":{"id":92704}}`))
	require.NoError(t, err)

	got := webhookTag(ios, fr)
	assert.Contains(t, got, "txn=8f2c1a9e-1234-11f1-b996-fa163e2f7c98", "the whole xid, not a segment")
	assert.Contains(t, got, "wh=a9bed278")
	assert.NotContains(t, got, "92704", "the payload's numeric id is not a handle any command accepts")
	assert.Less(t, strings.Index(got, "txn="), strings.Index(got, "wh="))
}

func TestWebhookTag_ToleratesAMissingXid(t *testing.T) {
	ios, _, _, _ := iostreams.Test()

	noTxn, err := listen.Decode([]byte(`{"webhook_xid":"wh_1","payload":{"id":7}}`))
	require.NoError(t, err)
	assert.Equal(t, "   wh=wh_1", webhookTag(ios, noTxn))

	empty, err := listen.Decode([]byte(`{"payload":{}}`))
	require.NoError(t, err)
	assert.Empty(t, webhookTag(ios, empty))
}
