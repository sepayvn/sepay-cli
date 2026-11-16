// Package api implements "sepay api", an authenticated raw passthrough to the public SePay API.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// NewCmdAPI builds the "sepay api" command.
func NewCmdAPI(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api <path>",
		Short: "Make an authenticated request to any SePay API endpoint",
		Long: `Send an authenticated request to a public SePay API endpoint and print the
response. Everything the CLI does is a public API call; this is the escape
hatch for endpoints without a dedicated command.

<path> is relative to the active environment's API base (e.g. /transactions).
On GET, --field values are sent as query parameters; on a write method they form
a JSON body. A --field value that is valid JSON (42, true, [1,2]) is sent typed,
otherwise as a string; use --input for a full raw body.

In sandbox, -X DELETE prompts before it is sent; other methods do not.`,
		Example: `  sepay api /transactions
  sepay api /transactions -f per_page=5
  sepay api /webhooks -X POST -f url=https://example.com/hook -f is_verify_payment=true
  sepay api /webhooks -X POST --input body.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAPI(cmd, f, args[0])
		},
	}
	fl := cmd.Flags()
	fl.StringP("method", "X", "GET", "HTTP method")
	fl.StringArrayP("field", "f", nil, "Add a body field (key=value, repeatable)")
	fl.StringArrayP("header", "H", nil, "Add a request header (repeatable)")
	fl.String("input", "", "Read the request body from a file, or - for stdin")
	cmdutil.AddDryRunFlag(cmd, f)
	fl.Lookup("dry-run").Usage = "Print the request without sending it, a GET included"
	return cmdutil.Annotate(cmd, map[string]string{
		cmdutil.AnnConfirms: "sandbox=conditional,production=terminal",
		cmdutil.AnnGuidance: "This bypasses every per-command guard: -X POST|PUT|DELETE reaches any endpoint, including ones whose dedicated command refuses production. Prefer the dedicated command when one exists.",
	})
}

func runAPI(cmd *cobra.Command, f *cmdutil.Factory, endpoint string) error {
	fl := cmd.Flags()
	methodFlag, _ := fl.GetString("method")
	method := strings.ToUpper(methodFlag)
	fields, _ := fl.GetStringArray("field")
	headers, _ := fl.GetStringArray("header")
	input, _ := fl.GetString("input")

	arg := endpoint
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	isRead := method == http.MethodGet || method == http.MethodHead

	var query url.Values
	var opts api.WriteOptions
	if isRead {
		if input != "" {
			return &cmdutil.FlagError{Err: fmt.Errorf("--input is not allowed with %s", method)}
		}
		var err error
		if query, err = fieldQuery(fields); err != nil {
			return err
		}
	} else {
		body, err := requestBody(f, fields, input)
		if err != nil {
			return err
		}
		opts.Body = body
		// Dropping an invalid key would also disable the transport-error retry.
		if opts.IdempotencyKey, err = f.IdempotencyKey(); err != nil {
			return err
		}
	}

	var err error
	if opts.Header, err = parseHeaders(headers); err != nil {
		return err
	}

	if !isRead {
		if err := f.ConfirmMutation(fmt.Sprintf("api %s %s", method, actionEndpoint(endpoint)), namedResource(endpoint), method == http.MethodDelete); err != nil {
			return err
		}
	}

	client, err := f.NewAPIClient()
	if err != nil {
		return err
	}
	// Check the argument as typed: the prepended "/" hides a scheme from looksLikeURL.
	if err := rejectAbsoluteURL(client.BaseURL(), arg); err != nil {
		return err
	}
	if err := rejectDotSegments(client.BaseURL(), endpoint); err != nil {
		return err
	}
	if err := rejectDuplicateBasePath(client.BaseURL(), endpoint); err != nil {
		return err
	}
	if f.Global.DryRun {
		return dryRun(f, client.BaseURL(), method, endpoint, query, opts)
	}
	env, err := client.Request(cmd.Context(), method, endpoint, query, opts)
	if err != nil {
		return err
	}
	return printResponse(f, env)
}

// dryRun prints the request without sending it, reads included: the target as client.Request
// builds it, the headers -H sets by name only, once per value sent, and every credential-named
// query value and body key masked. The headers the client adds itself (Authorization,
// X-Idempotency-Key, the version pin) are left out.
func dryRun(f *cmdutil.Factory, baseURL, method, endpoint string, query url.Values, opts api.WriteOptions) error {
	target := endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	// The real run fails here too, before anything is sent.
	if _, err := url.Parse(baseURL + target); err != nil {
		return err
	}
	// A fragment never leaves the client, and whatever follows "#" goes with it.
	target, _, _ = strings.Cut(target, "#")
	reqPath, rawQuery, _ := strings.Cut(target, "?")
	var names []string
	for name, values := range opts.Header {
		for range values {
			names = append(names, http.CanonicalHeaderKey(name))
		}
	}
	slices.Sort(names)
	body, err := maskedBody(opts.Body, f.Global.Output == "yaml")
	if err != nil {
		return err
	}
	return f.RenderDryRunRequest(cmdutil.DryRunRequest{
		Method: method, Path: reqPath, Query: maskedQuery(rawQuery), Headers: names, Body: body,
	})
}

// maskedQuery masks the value of each credential-named parameter, keeping the rest byte for byte.
// "&", ";" and a second "?" all start a parameter: a path's own query and the -f fields join as
// "a=1?b=2", and a server may split on ";". A masked value runs to the next "&", since a server
// splitting on "&" alone reads it that far. An empty value stays empty: it clears, not sets.
func maskedQuery(raw string) string {
	var b strings.Builder
	for raw != "" {
		param, sep := raw, ""
		if i := strings.IndexAny(raw, "&;?"); i >= 0 {
			param, sep, raw = raw[:i], raw[i:i+1], raw[i+1:]
		} else {
			raw = ""
		}
		k, v, hasValue := strings.Cut(param, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if hasValue && credentialKey(name) {
			if sep != "" && sep != "&" {
				rest, after, found := strings.Cut(raw, "&")
				v += sep + rest
				sep, raw = "", after
				if found {
					sep = "&"
				}
			}
			if v != "" {
				param = k + "=***"
			} else {
				param = k + "="
			}
		}
		b.WriteString(param)
		b.WriteString(sep)
	}
	return b.String()
}

// maskedBody is the body as the client encodes it, from -f fields or --input alike, with the value
// of every credential-named key masked at any depth. Numbers keep the digits sent, under YAML too,
// as plain scalars.
func maskedBody(body any, forYAML bool) (any, error) {
	if body == nil {
		return nil, nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return nil, err
	}
	if forYAML {
		decoded = mapNumbers(decoded, func(n json.Number) any {
			return &yaml.Node{Kind: yaml.ScalarNode, Value: n.String()}
		})
	}
	return maskCredentials(decoded), nil
}

func maskCredentials(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			// An empty or null credential clears one: "***" would read as a secret being set.
			if credentialKey(k) && e != nil && e != "" {
				out[k] = "***"
				continue
			}
			out[k] = maskCredentials(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = maskCredentials(e)
		}
		return out
	default:
		return v
	}
}

// credentialWords name a secret when one is a whole word of a key.
var credentialWords = []string{
	"secret", "password", "passwd", "passcode", "pass", "pin", "otp", "totp", "hotp",
	"credential", "credentials", "authorization", "cookie", "apikey", "pwd",
	"jwt", "bearer", "signature", "cvv", "refresh",
}

// credentialKey reports a key naming a secret: a word of credentialWords in any part, a last part
// "token" or "key" after another word (access_token, accessToken, api-key, subscriber_key), "key"
// alone, one word ending in "token" (token, accesstoken), or mat_khau in any spelling. Masking too
// much only hides a value from the preview; too little prints a secret.
func credentialKey(key string) bool {
	words := keyWords(key)
	for _, w := range words {
		if slices.Contains(credentialWords, w) {
			return true
		}
	}
	joined := strings.Join(words, "")
	for _, w := range []string{"secret", "password", "apikey", "credential", "matkhau"} {
		if strings.Contains(joined, w) {
			return true
		}
	}
	n := len(words)
	return n > 1 && (words[n-1] == "token" || words[n-1] == "key") || n == 1 && (words[0] == "key" || strings.HasSuffix(words[0], "token"))
}

// keyWords splits a key into lower-case words at separators and camelCase humps.
func keyWords(key string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(key)
	for i, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1])):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return words
}

// resourceID is the named-resource form of the production write rule: a UUID or a number.
var resourceID = regexp.MustCompile(`^(?i:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9]+)$`)

// namedResource returns reqPath when its last segment is a resource id, else "": a production write
// accepts --yes only when it names the resource it changes. A parent id does not count, since
// POST /bank-accounts/<id>/orders writes a new resource under it.
func namedResource(reqPath string) string {
	p := reqPath
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimRight(p, "/")
	if resourceID.MatchString(p[strings.LastIndex(p, "/")+1:]) {
		return p
	}
	return ""
}

// actionEndpoint is endpoint as the confirmation prompt and the refusals print it: a query value
// may be a credential, so each one, and any parameter without "=", becomes ***. So does the
// userinfo of a pasted URL, which the confirm prints before rejectAbsoluteURL refuses it. The
// fragment is never sent, so it is dropped.
func actionEndpoint(endpoint string) string {
	endpoint, _, _ = strings.Cut(endpoint, "#")
	p, rawQuery, ok := strings.Cut(endpoint, "?")
	if looksLikeURL(p) || looksLikeURL(strings.TrimPrefix(p, "/")) {
		authStart := strings.Index(p, "//") + 2
		authority, _, _ := strings.Cut(p[authStart:], "/")
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			p = p[:authStart] + "***" + p[authStart+at:]
		}
	}
	if !ok {
		return p
	}
	params := strings.FieldsFunc(rawQuery, func(r rune) bool { return r == '&' || r == ';' })
	for i, kv := range params {
		if k, _, hasValue := strings.Cut(kv, "="); hasValue {
			params[i] = k + "=***"
		} else {
			params[i] = "***"
		}
	}
	return p + "?" + strings.Join(params, "&")
}

// looksLikeURL reports a scheme or "//host" prefix; anchored, so a URL in a query value is fine.
func looksLikeURL(arg string) bool {
	if strings.HasPrefix(arg, "//") {
		return true
	}
	i := strings.Index(arg, "://")
	if i <= 0 {
		return false
	}
	for _, r := range arg[:i] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '+', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// basePathCandidates returns the prefixes a path must not repeat, longest first. A local backend serves v2
// under another prefix, where only the tail gets repeated.
func basePathCandidates(baseURL string) []string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil // not ours to report; the request will surface it
	}
	full := strings.TrimRight(u.Path, "/")
	if full == "" {
		return nil
	}
	candidates := []string{full}
	if i := strings.LastIndex(full, "/"); i > 0 {
		candidates = append(candidates, full[i:])
	}
	return candidates
}

// rejectAbsoluteURL refuses a pasted URL, which would become a path segment and 404. The host cannot move either way.
func rejectAbsoluteURL(baseURL, arg string) error {
	if !looksLikeURL(arg) {
		return nil
	}
	suggestion := "/transactions" // nothing usable in the argument; name the shape
	if u, err := url.Parse(arg); err == nil && u.Path != "" && u.Path != "/" {
		p := u.Path
		for _, prefix := range basePathCandidates(baseURL) {
			if strings.HasPrefix(p, prefix+"/") {
				p = strings.TrimPrefix(p, prefix)
				break
			}
		}
		if u.RawQuery != "" {
			p += "?" + u.RawQuery
		}
		suggestion = p
	}
	return &cmdutil.FlagError{Err: fmt.Errorf(
		"path is relative to the API base (%s), so it must not be a full URL; use %s",
		baseURL, suggestion)}
}

// rejectDotSegments refuses a path escaping the API base: the server resolves "x/../../oauth/tokens"
// past the prefix guards.
func rejectDotSegments(baseURL, reqPath string) error {
	rawPath, suffix := reqPath, ""
	if i := strings.IndexAny(reqPath, "?#"); i >= 0 {
		rawPath, suffix = reqPath[:i], reqPath[i:]
	}
	if !api.HasDotSegment(rawPath) {
		return nil
	}
	resolved := rawPath
	if decoded, err := url.PathUnescape(rawPath); err == nil {
		resolved = decoded
	}
	return &cmdutil.FlagError{Err: fmt.Errorf(
		"path is relative to the API base (%s), so it must not contain \".\" or \"..\" segments; use %s",
		baseURL, cleanPath(baseURL, resolved)+suffix)}
}

// cleanPath resolves dot segments into a suggestion rejectDuplicateBasePath also accepts.
func cleanPath(baseURL, p string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	for _, prefix := range basePathCandidates(baseURL) {
		if strings.HasPrefix(cleaned, prefix+"/") {
			cleaned = strings.TrimPrefix(cleaned, prefix)
			break
		}
	}
	if cleaned == "/" || cleaned == "" {
		return "/transactions" // nothing usable left; name the shape
	}
	return cleaned
}

// rejectDuplicateBasePath refuses a path repeating the base's prefix (`sepay api /v2/...`), which can only 404.
func rejectDuplicateBasePath(baseURL, p string) error {
	for _, prefix := range basePathCandidates(baseURL) {
		if p != prefix && !strings.HasPrefix(p, prefix+"/") {
			continue
		}
		suggestion := strings.TrimPrefix(p, prefix)
		if suggestion == "" {
			suggestion = "/transactions" // path was the prefix alone; nothing to keep
		}
		return &cmdutil.FlagError{Err: fmt.Errorf(
			"path is relative to the API base (%s), so it must not repeat %q; use %s",
			baseURL, prefix, suggestion)}
	}
	return nil
}

// requestBody builds a write's JSON body from --input or --field (mutually exclusive).
func requestBody(f *cmdutil.Factory, fields []string, input string) (any, error) {
	if input != "" {
		if len(fields) > 0 {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("use either --input or --field, not both")}
		}
		raw, err := readInput(f, input)
		if err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("--input is not valid JSON")}
		}
		return json.RawMessage(raw), nil
	}
	if len(fields) == 0 {
		return nil, nil
	}
	body := make(map[string]any, len(fields))
	for _, kv := range fields {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("invalid --field %q, expected key=value", kv)}
		}
		body[k] = fieldValue(v)
	}
	return body, nil
}

func fieldQuery(fields []string) (url.Values, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	q := make(url.Values, len(fields))
	for _, kv := range fields {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("invalid --field %q, expected key=value", kv)}
		}
		q.Add(k, v)
	}
	return q, nil
}

// fieldValue sends a value typed when it is valid JSON, else as a string.
func fieldValue(v string) any {
	var typed any
	if err := json.Unmarshal([]byte(v), &typed); err == nil {
		return typed
	}
	return v
}

func readInput(f *cmdutil.Factory, input string) ([]byte, error) {
	if input == "-" {
		return io.ReadAll(f.IOStreams.In)
	}
	raw, err := os.ReadFile(filepath.Clean(input))
	if err != nil {
		return nil, fmt.Errorf("could not read --input file: %w", err)
	}
	return raw, nil
}

// parseHeaders parses "Name: value" pairs. Invalid ones are refused here: net/http fails them locally
// and the client would retry that as a network error.
func parseHeaders(headers []string) (http.Header, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	h := make(http.Header, len(headers))
	for _, raw := range headers {
		name, value, ok := strings.Cut(raw, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || name == "" {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("invalid --header %q, expected Name: value", raw)}
		}
		if !validHeaderName(name) {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("invalid --header name %q, expected letters, digits or !#$%%&'*+-.^_`|~", name)}
		}
		if !validHeaderValue(value) {
			return nil, &cmdutil.FlagError{Err: fmt.Errorf("invalid --header %q, the value must not contain a control character", name)}
		}
		h.Add(name, value)
	}
	return h, nil
}

// validHeaderName mirrors httpguts.ValidHeaderFieldName.
func validHeaderName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue mirrors httpguts.ValidHeaderFieldValue, which also rejects an embedded CRLF.
func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if c := value[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// printResponse prints the envelope's raw JSON so integers keep their form; YAML goes through a generic decode.
func printResponse(f *cmdutil.Factory, env *api.Envelope) error {
	var enc bytes.Buffer
	e := json.NewEncoder(&enc)
	e.SetEscapeHTML(false)
	if err := e.Encode(env); err != nil {
		return err
	}
	raw := enc.Bytes()
	if f.Global.Output == "yaml" {
		// Plain decoding turns numbers into float64 (1e+06, precision loss past 2^53).
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var generic any
		if err := dec.Decode(&generic); err != nil {
			return err
		}
		return jsonfmt.RenderYAML(f.IOStreams.Out, mapNumbers(generic, settleNumber))
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := f.IOStreams.Out.Write(buf.Bytes())
	return err
}

// mapNumbers replaces each json.Number in a generic decode with leaf(n).
func mapNumbers(v any, leaf func(json.Number) any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = mapNumbers(e, leaf)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = mapNumbers(e, leaf)
		}
		return t
	case json.Number:
		return leaf(t)
	default:
		return v
	}
}

// settleNumber resolves a json.Number to int64 when whole and in range, else float64.
func settleNumber(n json.Number) any {
	if i, err := n.Int64(); err == nil {
		return i
	}
	if f, err := n.Float64(); err == nil {
		return f
	}
	return n.String()
}
