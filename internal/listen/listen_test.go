package listen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/internal/auth"
	"github.com/sepayvn/sepay-cli/internal/config"
	"github.com/sepayvn/sepay-cli/internal/forward"
	"github.com/sepayvn/sepay-cli/internal/sse"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// raw_body is not byte-identical to a re-encode of payload.
func TestFrame_BodyIsExactSignedBytes(t *testing.T) {
	signed := `{"content":"Thanh toán \/ ABC","transferAmount":100000}`
	rawBodyField, err := json.Marshal(signed)
	require.NoError(t, err)

	fr, err := Decode([]byte(`{"id":"evt_1","type":"payment_in","headers":{},` +
		`"payload":{"content":"Thanh toán / ABC","transferAmount":100000},` +
		`"raw_body":` + string(rawBodyField) + `}`))
	require.NoError(t, err)

	assert.Equal(t, signed, string(fr.Body()),
		"forwarded body must be the exact signed bytes, not a re-encode of payload")
	assert.NotEqual(t, string(fr.Payload), string(fr.Body()),
		"payload (unescaped object) must differ from the signed raw_body")
}

func TestFrame_BodyFallsBackToPayload(t *testing.T) {
	fr, err := Decode([]byte(`{"id":"e","type":"payment_in","payload":{"a":1}}`))
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(fr.Body()))
}

func TestDecode_KeepsRawForProductFields(t *testing.T) {
	fr, err := Decode([]byte(`{"id":"e","type":"pg_ipn.order_paid","pg_only":"x","payload":{}}`))
	require.NoError(t, err)

	var extra struct {
		PgOnly string `json:"pg_only"`
	}
	require.NoError(t, json.Unmarshal(fr.Raw, &extra))
	assert.Equal(t, "x", extra.PgOnly, "Raw must carry fields the shared struct does not model")
}

func TestDeliveryHeaders(t *testing.T) {
	fr, err := Decode([]byte(`{"headers":{"X-SePay-Signature":"sha256=abc","Content-Length":"12","Host":"evil","Authorization":"Apikey k"}}`))
	require.NoError(t, err)

	got := DeliveryHeaders(fr)
	assert.Equal(t, "sha256=abc", got.Get("X-SePay-Signature"))
	assert.Equal(t, "Apikey k", got.Get("Authorization"), "SePay's own delivery auth is replayed verbatim")
	assert.Empty(t, got.Get("Content-Length"), "hop-by-hop headers belong to the HTTP client")
	assert.Empty(t, got.Get("Host"))
	assert.Equal(t, "application/json", got.Get("Content-Type"), "a JSON default when the channel sent none")
}

func TestDeliveryHeaders_KeepsAnExplicitContentType(t *testing.T) {
	fr, err := Decode([]byte(`{"headers":{"Content-Type":"application/x-www-form-urlencoded"}}`))
	require.NoError(t, err)
	assert.Equal(t, "application/x-www-form-urlencoded", DeliveryHeaders(fr).Get("Content-Type"))
}

func TestForwardEvent_Filters(t *testing.T) {
	cases := []struct {
		name    string
		events  []string
		accept  func(Frame) bool
		frame   string
		forward bool
	}{
		{"no filters forwards", nil, nil, `{"type":"payment_in","payload":{}}`, true},
		{"events filter keeps a match", []string{"payment_in"}, nil, `{"type":"payment_in","payload":{}}`, true},
		{"events filter drops a non-match", []string{"payment_in"}, nil, `{"type":"payment_out","payload":{}}`, false},
		{"source Accept drops", nil, func(Frame) bool { return false }, `{"type":"payment_in","payload":{}}`, false},
		{"source Accept keeps", nil, func(Frame) bool { return true }, `{"type":"payment_in","payload":{}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				got++
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			fwd, err := forward.NewForwarder(strings.TrimPrefix(srv.URL, "http://"), false)
			require.NoError(t, err)

			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
			frame, ok := decodeFrame([]byte(tc.frame), f)
			require.True(t, ok)
			if accepts(frame, toSet(tc.events), Source{Accept: tc.accept}) {
				_, _ = forwardEvent(context.Background(), job{frame: frame}, fwd, Source{Accept: tc.accept}, Options{}, f)
			}

			if tc.forward {
				assert.Equal(t, 1, got, "should have forwarded")
			} else {
				assert.Zero(t, got, "should have been dropped")
			}
		})
	}
}

func TestForwardEvent_MalformedFrameIsSkipped(t *testing.T) {
	var got int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	fwd, err := forward.NewForwarder(strings.TrimPrefix(srv.URL, "http://"), false)
	require.NoError(t, err)

	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	_, ok := decodeFrame([]byte(`{not json`), f)
	assert.False(t, ok, "a malformed frame must not be forwarded")
	_ = fwd

	assert.Zero(t, got)
	assert.Contains(t, errOut.String(), "malformed")
}

func TestPrintListening_NamesTheProductAndEnv(t *testing.T) {
	fwd, err := forward.NewForwarder("localhost:3000", false)
	require.NoError(t, err)
	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	printListening(f, Source{Noun: "PG IPN"}, &Session{Environment: "sandbox"}, fwd, false)
	assert.Contains(t, errOut.String(), "Listening for sandbox PG IPN")
	assert.Contains(t, errOut.String(), "(Ctrl+C to stop)",
		"a session with no control layer must not advertise one")
}

func TestPrintListening_AnnouncesTheControlKeys(t *testing.T) {
	fwd, err := forward.NewForwarder("localhost:3000", false)
	require.NoError(t, err)
	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	printListening(f, Source{}, &Session{Environment: "sandbox"}, fwd, true)
	assert.Contains(t, errOut.String(), "(Ctrl+C to stop · retries+enter to manage)")
}

func TestPrintListening_DefaultsToEvents(t *testing.T) {
	fwd, err := forward.NewForwarder("localhost:3000", false)
	require.NoError(t, err)
	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	printListening(f, Source{}, &Session{Environment: "sandbox"}, fwd, false)
	assert.Contains(t, errOut.String(), fmt.Sprintf("Listening for sandbox events → %s", fwd.Target()))
}

func TestDisplayHeaderValue(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"Authorization", "Bearer real", "<redacted>"},
		{"authorization", "Bearer real", "<redacted>"},
		{"X-Secret-Key", "s3cr3t", "<redacted>"},
		{"Proxy-Authorization", "Basic abc", "<redacted>"},
		{"Cookie", "sid=1", "<redacted>"},
		{"X-Future-Token", "abc", "<redacted>"},
		{"X-Client-Password", "abc", "<redacted>"},
		{"X-SePay-Signature", "sha256=abc", "sha256=abc"},
		{"X-SePay-Timestamp", "1700", "1700"},
		{"Content-Type", "application/json", "application/json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, displayHeaderValue(tc.name, tc.value))
		})
	}
}

func TestWithholdCredentials(t *testing.T) {
	h := http.Header{}
	h.Set("X-Secret-Key", "s3cr3t")
	h.Set("Authorization", "Bearer real")
	h.Set("X-Future-Token", "abc")
	h.Set("X-SePay-Signature", "sha256=abc")
	h.Set("Content-Type", "application/json")

	withholdCredentials(h)

	assert.Empty(t, h.Get("X-Secret-Key"))
	assert.Empty(t, h.Get("Authorization"))
	assert.Empty(t, h.Get("X-Future-Token"), "the substring net applies here too")
	assert.Equal(t, "sha256=abc", h.Get("X-SePay-Signature"), "a signature is not a credential")
	assert.Equal(t, "application/json", h.Get("Content-Type"))
}

func TestForwardEvent_ForwardsTheRealCredentialItRedactsOnScreen(t *testing.T) {
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	fwd, err := forward.NewForwarder(strings.TrimPrefix(srv.URL, "http://"), false)
	require.NoError(t, err)

	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	frame, ok := decodeFrame([]byte(`{"type":"ORDER_PAID","headers":{"X-Secret-Key":"s3cr3t"},"payload":{}}`), f)
	require.True(t, ok)
	_, _ = forwardEvent(context.Background(), job{frame: frame}, fwd, Source{}, Options{Headers: true}, f)

	select {
	case h := <-got:
		assert.Equal(t, "s3cr3t", h.Get("X-Secret-Key"), "the forwarded request keeps the real value")
	case <-time.After(3 * time.Second):
		require.Fail(t, "handler never received the delivery")
	}
	assert.NotContains(t, out.String(), "s3cr3t", "but stdout must not show it")
	assert.Contains(t, out.String(), "<redacted>")
}

func TestRedactPayload_HidesCardFieldsAtAnyDepth(t *testing.T) {
	payload := []byte(`{"order":{"order_id":"INV-1","amount":250000},` +
		`"transaction":{"card_number":"4111111111111111","card_holder_name":"NGUYEN VAN A",` +
		`"card_expiry":"12/28","cvv":"123","transaction_id":"txn_1"}}`)

	got := string(redactPayload(payload))

	for _, secret := range []string{"4111111111111111", "NGUYEN VAN A", "12/28", "123"} {
		assert.NotContains(t, got, secret)
	}
	assert.Contains(t, got, "INV-1")
	assert.Contains(t, got, "250000")
	assert.Contains(t, got, "txn_1")
	assert.Contains(t, got, "<redacted>")
}

func TestRedactPayload_RefusesToPrintWhatItCannotParse(t *testing.T) {
	assert.Equal(t, "<unprintable payload>", string(redactPayload([]byte(`{"card_number":`))))
}

func TestOutboundHeaders_WithholdDropsCredentialsKeepsSignature(t *testing.T) {
	fr := Frame{Headers: map[string]string{
		"X-Secret-Key":      "s3cr3t",
		"Authorization":     "Bearer t0ken",
		"X-Sepay-Signature": "sha256=abc",
		"Content-Type":      "application/json",
	}}

	loopback := OutboundHeaders(fr, false)
	assert.Equal(t, "s3cr3t", loopback.Get("X-Secret-Key"))
	assert.Equal(t, "Bearer t0ken", loopback.Get("Authorization"))

	remote := OutboundHeaders(fr, true)
	assert.Empty(t, remote.Get("X-Secret-Key"))
	assert.Empty(t, remote.Get("Authorization"))
	assert.Equal(t, "sha256=abc", remote.Get("X-Sepay-Signature"))
	assert.Equal(t, "application/json", remote.Get("Content-Type"))
}

func TestReadLoop_FilteredEventsDoNotConsumeQueueSlots(t *testing.T) {
	ios, _, _, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	filter := toSet([]string{"payment_in"})
	jobs := make(chan job, 2) // deliberately tiny
	dropped := 0

	for i := 0; i < 100; i++ {
		frame, ok := decodeFrame([]byte(`{"type":"payment_out","payload":{}}`), f)
		require.True(t, ok)
		if accepts(frame, filter, Source{}) {
			select {
			case jobs <- job{frame: frame}:
			default:
				dropped++
			}
		}
	}
	require.Empty(t, jobs, "filtered-out events must never be queued")
	require.Zero(t, dropped, "filtered-out events must never count as drops")

	frame, ok := decodeFrame([]byte(`{"type":"payment_in","payload":{}}`), f)
	require.True(t, ok)
	require.True(t, accepts(frame, filter, Source{}))
	select {
	case jobs <- job{frame: frame}:
	default:
		require.Fail(t, "the wanted event was evicted by traffic the filter rejects")
	}
	assert.Len(t, jobs, 1)
}

func TestAccepts_HonoursProductFilter(t *testing.T) {
	fr := Frame{Type: "payment_in"}
	assert.True(t, accepts(fr, nil, Source{}))
	assert.False(t, accepts(fr, nil, Source{Accept: func(Frame) bool { return false }}))
	assert.False(t, accepts(fr, toSet([]string{"payment_out"}), Source{}))
}

func TestPayloadString(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		path    []string
		want    string
	}{
		{"webhook keeps an integer id exact", `{"id":92704}`, []string{"id"}, "92704"},
		{"a large id does not become scientific notation", `{"id":9007199254740991}`, []string{"id"}, "9007199254740991"},
		{"gateway IPN nests it under transaction", `{"transaction":{"id":"7c1e2b3a-4d5f"}}`, []string{"transaction", "id"}, "7c1e2b3a-4d5f"},
		{"a missing key is empty, not a zero", `{"other":1}`, []string{"id"}, ""},
		{"a missing parent is empty", `{}`, []string{"transaction", "id"}, ""},
		{"a parent of the wrong shape is empty", `{"transaction":7}`, []string{"transaction", "id"}, ""},
		{"an unparseable payload is empty", `{`, []string{"id"}, ""},
		{"a null id is empty", `{"id":null}`, []string{"id"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PayloadString(Frame{Payload: []byte(tc.payload)}, tc.path...))
		})
	}
}

func TestTag_SanitizesAndSkipsEmpty(t *testing.T) {
	ios, _, _, _ := iostreams.Test()

	assert.Empty(t, Tag(ios, "txn", ""), "no label without a value")
	assert.Equal(t, "   txn=92704", Tag(ios, "txn", "92704"))
	got := Tag(ios, "txn", "9\x1b[2J270\x1b]0;x\x07")
	assert.NotContains(t, got, "\x1b[2J", "a screen clear must not reach the terminal")
	assert.NotContains(t, got, "\x1b]0;", "nor an OSC title sequence")
}

func factoryForLogin(profile, email, env string) *cmdutil.Factory {
	ios, _, _, _ := iostreams.Test()
	hosts := &config.Hosts{
		Active:   profile,
		Profiles: map[string]*config.Profile{profile: {Host: "my.sepay.vn", Email: email}},
	}
	return &cmdutil.Factory{
		IOStreams:   ios,
		Global:      &cmdutil.GlobalOptions{Env: env},
		Credentials: func() (*auth.Manager, error) { return auth.NewManager(hosts), nil },
	}
}

func TestChannelScope_SeparatesAccountsAndEnvironments(t *testing.T) {
	acme := channelScope(factoryForLogin("acme", "ops@acme.vn", "sandbox"), "webhook")

	assert.NotEqual(t, acme, channelScope(factoryForLogin("beta", "ops@beta.vn", "sandbox"), "webhook"),
		"two logins are two channels")
	assert.Equal(t, acme, channelScope(factoryForLogin("acme", "ops@acme.vn", "sandbox"), "webhook"),
		"one login on one product is one channel")
	assert.NotEqual(t, acme, channelScope(factoryForLogin("acme", "ops@acme.vn", "production"), "webhook"))
	assert.NotEqual(t, acme, channelScope(factoryForLogin("acme", "ops@acme.vn", "sandbox"), "pg_ipn"))
}

func TestChannelScope_TwoNamesForOneLoginShareTheChannel(t *testing.T) {
	byEmail := channelScope(factoryForLogin("dev@shop.vn", "dev@shop.vn", "sandbox"), "webhook")
	byNickname := channelScope(factoryForLogin("shop", "dev@shop.vn", "sandbox"), "webhook")
	assert.Equal(t, byEmail, byNickname)
}

func TestChannelScope_FallsBackToTheProfileNameWithoutAnEmail(t *testing.T) {
	a := channelScope(factoryForLogin("a", "", "sandbox"), "webhook")
	b := channelScope(factoryForLogin("b", "", "sandbox"), "webhook")
	assert.NotEmpty(t, a)
	assert.NotEqual(t, a, b)
}

func TestChannelScope_StaysEmptyWithoutAProductScope(t *testing.T) {
	assert.Empty(t, channelScope(factoryForLogin("acme", "ops@acme.vn", "sandbox"), ""))
	assert.Nil(t, newCoordinator("", "http://localhost:3000/", 7))
}

func TestRevoke_FailureIsReportedAndNeverLogsTheKey(t *testing.T) {
	const key = "lk_supersecretvalue0123456789"

	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Listen-Key")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","error":{"code":"boom"}}`))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	client := api.NewClient(srv.URL, "t")
	Revoke(context.Background(), client, "/v2/webhooks/listen", key)

	assert.Equal(t, key, gotKey, "the key travels in the header, not the path")
	out := logs.String()
	assert.Contains(t, out, "could not revoke the listen session")
	assert.NotContains(t, out, key, "the credential must never reach a log line")
}

func TestRevoke_SuccessLogsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	client := api.NewClient(srv.URL, "t")
	Revoke(context.Background(), client, "/v2/webhooks/listen", "lk_"+strings.Repeat("a", 32))

	assert.Empty(t, logs.String())
}

func TestRevoke_EmptyArgsMakeNoCallAndNoLog(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	defer srv.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(prev)

	client := api.NewClient(srv.URL, "t")
	Revoke(context.Background(), client, "", "lk_x")
	Revoke(context.Background(), client, "/v2/webhooks/listen", "")

	assert.False(t, called)
	assert.Empty(t, logs.String())
}

func TestWithholdCredentialsFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		target  string
		skipTLS bool
		optIn   bool
		want    bool
		reasonN string
	}{
		{"loopback replays", "localhost:3000", false, false, false, ""},
		{"loopback https with verification off withholds", "https://localhost:3443", true, false, true, "TLS verification is off"},
		{"loopback https with verification off withholds even with the opt-in", "https://127.0.0.1:3443", true, true, true, "TLS verification is off"},
		{"loopback http ignores --skip-tls-verify", "http://localhost:3000", true, false, false, ""},
		{"loopback ignores the opt-in", "http://127.0.0.1:3000", false, true, false, ""},
		{"https off-machine withholds by default", "https://staging.example.com/hook", false, false, true, "--forward-credentials"},
		{"https off-machine replays with the opt-in", "https://staging.example.com/hook", false, true, false, ""},
		{"plaintext off-machine withholds", "http://staging.example.com/hook", false, false, true, "plaintext http"},
		{"plaintext off-machine withholds even with the opt-in", "http://staging.example.com/hook", false, true, true, "plaintext http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fwd, err := forward.NewForwarder(tc.target, tc.skipTLS)
			require.NoError(t, err)

			got := WithholdCredentialsFor(fwd, tc.optIn)
			assert.Equal(t, tc.want, got)

			if tc.reasonN != "" {
				assert.Contains(t, WithholdReason(fwd), tc.reasonN,
					"the warning must not point at a flag that cannot help this target")
			}
		})
	}
}

func TestRetryable_MintUnauthorized(t *testing.T) {
	unauthorized := &api.HTTPError{StatusCode: http.StatusUnauthorized, Code: "unauthorized", Message: "session expired"}

	ok, _ := retryable(unauthorized)
	assert.False(t, ok, "a revoked grant must end the session")

	stale := fmt.Errorf("%w: %v", cmdutil.ErrTokenRefreshUnreachable, "dial tcp: i/o timeout")
	ok, _ = retryable(fmt.Errorf("%w (%v)", stale, unauthorized)) //nolint:errorlint // mirrors listen.go
	assert.True(t, ok, "a stale-token 401 must reconnect")
}

// A proxy in front of the API answers 502/504 while the app restarts; errors.go calls those
// "retry in a moment", so the session retries them instead of ending.
func TestRetryable_GatewayErrorsReconnect(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusInternalServerError, false},
		{http.StatusBadRequest, false},
	} {
		ok, _ := retryable(&api.HTTPError{StatusCode: tc.status})
		assert.Equal(t, tc.want, ok, "status %d", tc.status)
	}
}

func TestReconnectReason_GatewayErrorIsNotUnreachable(t *testing.T) {
	assert.Equal(t, "SePay is temporarily unavailable (HTTP 502)",
		reconnectReason(phaseMint, &api.HTTPError{StatusCode: http.StatusBadGateway}))
	assert.Equal(t, "the hub is temporarily unavailable (HTTP 504)",
		reconnectReason(phaseOpen, &sse.RetryableError{StatusCode: http.StatusGatewayTimeout}))
	assert.Equal(t, "cannot reach SePay to open a listen session",
		reconnectReason(phaseMint, errors.New("dial tcp: connection refused")))
}

func TestRetryable_HubKeyRefusalReconnects(t *testing.T) {
	refused := &sse.KeyRefusedError{StatusCode: http.StatusUnauthorized, Code: "unauthorized", Message: "Khoá không hợp lệ"}

	ok, _ := retryable(refused)
	assert.True(t, ok, "a dead listen_key is fixed by minting another one")
	assert.NotEqual(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(refused),
		"`sepay login` cannot fix a refused listen_key, so the exit code must not ask for it")
	assert.Contains(t, reconnectReason(phaseOpen, refused), "refused this listen session")

	capped := &api.HTTPError{StatusCode: refused.StatusCode, Code: refused.Code, Message: refused.Message}
	ok, _ = retryable(capped)
	assert.False(t, ok, "past the cap the refusal is the hub's standing answer")
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(capped))
}

func TestNoteUnforwardedFrame(t *testing.T) {
	cases := []struct {
		name  string
		ev    sse.Event
		twice bool
		want  []string
		quiet bool
	}{
		{name: "ready is silent", ev: sse.Event{Name: "ready", Data: []byte(`{"environment":"sandbox"}`)}, quiet: true},
		{name: "gap names the count", ev: sse.Event{Name: "gap", Data: []byte(`{"dropped":7}`)}, want: []string{"dropped 7 event(s)"}},
		{name: "gap without a count still speaks", ev: sse.Event{Name: "gap", Data: []byte(`{`)}, want: []string{"dropped events"}},
		{name: "unknown name warns once", ev: sse.Event{Name: "webhook.v2"}, twice: true, want: []string{`named "webhook.v2"`, "upgrade the CLI"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ios, _, _, errOut := iostreams.Test()
			f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}
			warned := false

			noteUnforwardedFrame(f, tc.ev, &warned)
			if tc.twice {
				noteUnforwardedFrame(f, tc.ev, &warned)
			}

			got := errOut.String()
			if tc.quiet {
				assert.Empty(t, got)
				return
			}
			for _, want := range tc.want {
				assert.Contains(t, got, want)
			}
			if tc.twice {
				assert.Equal(t, 1, strings.Count(got, "upgrade the CLI"), "an unknown name must be reported once per session")
			}
		})
	}
}

func TestNoteUnforwardedFrame_GapRepeats(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{}}
	warned := false

	gap := sse.Event{Name: "gap", Data: []byte(`{"dropped":2}`)}
	noteUnforwardedFrame(f, gap, &warned)
	noteUnforwardedFrame(f, gap, &warned)

	assert.Equal(t, 2, strings.Count(errOut.String(), "dropped 2 event(s)"))
}

// ExitAuth would tell the user to run `sepay login`, which cannot help when the OAuth host is unreachable.
func TestStaleTokenMintFailure_ExitsGenericNotAuth(t *testing.T) {
	unauthorized := &api.HTTPError{StatusCode: http.StatusUnauthorized, Code: "unauthorized", Message: "session expired"}
	assert.Equal(t, cmdutil.ExitAuth, cmdutil.ExitCodeForError(unauthorized))

	stale := fmt.Errorf("%w: %v", cmdutil.ErrTokenRefreshUnreachable, "dial tcp: i/o timeout")
	wrapped := fmt.Errorf("%w (%v)", stale, unauthorized) //nolint:errorlint // mirrors listen.go
	assert.Equal(t, cmdutil.ExitError, cmdutil.ExitCodeForError(wrapped))
	assert.ErrorIs(t, wrapped, cmdutil.ErrTokenRefreshUnreachable)
}

func TestRetryable_DisabledFeatureIsNotTransient(t *testing.T) {
	disabled := &api.HTTPError{
		StatusCode: http.StatusServiceUnavailable,
		Code:       "sse_hub_disabled",
		Message:    "The webhook listen feature is not enabled",
	}
	ok, _ := retryable(disabled)
	assert.False(t, ok, "the kill switch must stop the loop, not feed it")
	assert.Equal(t, cmdutil.ExitBlocked, cmdutil.ExitCodeForError(disabled))

	unavailable := &api.HTTPError{StatusCode: http.StatusServiceUnavailable, Code: "sse_hub_unavailable"}
	ok, _ = retryable(unavailable)
	assert.True(t, ok, "an unreachable hub does clear on its own")
}

func TestReconnectOrFail_DisabledFeatureStopsImmediately(t *testing.T) {
	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios}
	disabled := &api.HTTPError{StatusCode: http.StatusServiceUnavailable, Code: "sse_hub_disabled"}

	attempt := 0
	err := reconnectOrFail(context.Background(), f, &attempt, 0, phaseMint, disabled)

	require.ErrorIs(t, err, error(disabled))
	assert.Equal(t, 0, attempt, "no attempt is spent on a switch only SePay can flip")
	assert.NotContains(t, errOut.String(), "retrying")
}

func TestReconnectReason_SessionLimitIsNotAPlainRateLimit(t *testing.T) {
	limit := &api.HTTPError{StatusCode: http.StatusTooManyRequests, Code: "sse_hub_session_limit"}
	assert.Contains(t, reconnectReason(phaseMint, limit), "session limit", "session cap must name itself")

	rate := &api.HTTPError{StatusCode: http.StatusTooManyRequests, Code: "rate_limited"}
	assert.Equal(t, "rate limited by SePay", reconnectReason(phaseMint, rate), "an ordinary 429 must still read as a rate limit")
}

func TestNormalizeHubURL(t *testing.T) {
	cases := map[string]string{
		"ssehub-sandbox.sepay.vn":         "https://ssehub-sandbox.sepay.vn",
		"  ssehub-sandbox.sepay.vn/  ":    "https://ssehub-sandbox.sepay.vn/",
		"//ssehub-sandbox.sepay.vn":       "https://ssehub-sandbox.sepay.vn",
		"ssehub-sandbox.sepay.vn:8443":    "https://ssehub-sandbox.sepay.vn:8443",
		"https://ssehub-sandbox.sepay.vn": "https://ssehub-sandbox.sepay.vn",
		"http://127.0.0.1:8443":           "http://127.0.0.1:8443",
		"ftp://ssehub.sepay.vn":           "ftp://ssehub.sepay.vn",
		"":                                "",
	}
	for in, want := range cases {
		assert.Equal(t, want, normalizeHubURL(in), "normalizeHubURL(%q)", in)
	}
}

func TestRenderDelivery_FitsTheTerminal(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	ios.SetTerminalWidth(60)
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	line := deliveryLine{
		event: "payment_in", target: "localhost:3000",
		tag: "   txn=8f2c1a9e-1234-11f1-b996-fa163e2f7c98   wh=a9bed278",
	}

	renderDelivery(f, line, &forward.Result{StatusCode: 500, Body: []byte(strings.Repeat("x", 200))}, nil, nil)

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, got, 2)
	for _, l := range got {
		assert.LessOrEqual(t, utf8.RuneCountInString(l), 60, "line %q overflows", l)
	}
	assert.Contains(t, got[0], "payment_in   → localhost:3000   500")
	assert.True(t, strings.HasSuffix(got[1], "…"), "the body snippet is cut too")
}

func TestRenderDelivery_PipeKeepsTheWholeLine(t *testing.T) {
	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	line := deliveryLine{event: "payment_in", target: "localhost:3000", tag: "   txn=8f2c1a9e-1234-11f1-b996-fa163e2f7c98"}

	renderDelivery(f, line, &forward.Result{StatusCode: 200}, nil, nil)

	assert.Contains(t, out.String(), "txn=8f2c1a9e-1234-11f1-b996-fa163e2f7c98")
	assert.NotContains(t, out.String(), "…")
}

func TestForwardEvent_PrintStripsInvisibleCharactersButForwardsVerbatim(t *testing.T) {
	sent := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent <- b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	fwd, err := forward.NewForwarder(strings.TrimPrefix(srv.URL, "http://"), false)
	require.NoError(t, err)

	hostile := "Chuyển khoản \u009b1A\u202e000 001\u200b\ufeff\u2066\U000e0041\u00ad x\u200dy\u200cz"
	content, err := json.Marshal(hostile)
	require.NoError(t, err)
	rawBody, err := json.Marshal(`{"content":` + string(content) + `}`)
	require.NoError(t, err)
	frameJSON := `{"type":"payment_in","headers":{},"payload":{"content":` + string(content) +
		`,"transferAmount":100000},"raw_body":` + string(rawBody) + `}`

	ios, _, out, _ := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}
	frame, ok := decodeFrame([]byte(frameJSON), f)
	require.True(t, ok)
	_, err = forwardEvent(context.Background(), job{frame: frame}, fwd, Source{}, Options{Print: true}, f)
	require.NoError(t, err)

	printed := out.String()
	for _, r := range []rune{0x9b, 0x202e, 0x200b, 0xfeff, 0x2066, 0xe0041, 0x00ad} {
		assert.NotContains(t, printed, string(r), "U+%04X", r)
	}
	assert.Contains(t, printed, "Chuyển khoản")
	assert.Contains(t, printed, "x\u200dy\u200cz", "ZWJ and ZWNJ survive")
	assert.Contains(t, printed, `"transferAmount":100000`)

	select {
	case b := <-sent:
		assert.Equal(t, frame.Body(), b)
		assert.Contains(t, string(b), "\u202e")
	case <-time.After(3 * time.Second):
		require.Fail(t, "handler never received the delivery")
	}
}

func TestPrintListening_CleansTheServerEnvironment(t *testing.T) {
	fwd, err := forward.NewForwarder("localhost:3000", false)
	require.NoError(t, err)
	ios, _, _, errOut := iostreams.Test()
	f := &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}}

	printListening(f, Source{}, &Session{Environment: "sandbox\x1b[2J\x1b]0;pwn\x07"}, fwd, false)
	assert.NotContains(t, errOut.String(), "\x1b[2J")
	assert.NotContains(t, errOut.String(), "\x07")
	assert.Contains(t, errOut.String(), "Listening for sandbox")
}
