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
	"strings"

	"github.com/sepayvn/sepay-cli/internal/api"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/jsonfmt"

	"github.com/spf13/cobra"
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
otherwise as a string; use --input for a full raw body.`,
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
	return cmdutil.Annotate(cmd, map[string]string{
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
		if err := f.ConfirmMutation(fmt.Sprintf("api %s %s", method, endpoint), method == http.MethodDelete); err != nil {
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
	env, err := client.Request(cmd.Context(), method, endpoint, query, opts)
	if err != nil {
		return err
	}
	return printResponse(f, env)
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
		return jsonfmt.RenderYAML(f.IOStreams.Out, settleNumbers(generic))
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := f.IOStreams.Out.Write(buf.Bytes())
	return err
}

// settleNumbers resolves each json.Number to int64 when whole and in range, else float64.
func settleNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = settleNumbers(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = settleNumbers(e)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	default:
		return v
	}
}
