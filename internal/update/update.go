// Package update notifies, at most once a day, of a newer or deprecated release. It never installs.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/internal/envvar"

	"github.com/mattn/go-isatty"
)

const (
	// Package is the npm package; its latest dist-tag is the newest stable release.
	Package = "sepay-cli"
	// DisableEnv turns the check off.
	DisableEnv = "SEPAY_NO_UPDATE_NOTIFIER"

	registryURL = "https://registry.npmjs.org/sepay-cli/"
	// installScriptURL must match the get.sepay.vn redirect to the sepay-cli-install.sh release asset.
	installScriptURL = "https://get.sepay.vn/sepay-cli-install.sh"
	interval         = 24 * time.Hour
	stateName        = "update-state.json"
	maxBody          = 1 << 20
	// A network-read deprecation message is cut to this length.
	maxMessage = 300
)

// Checker queries the registry and caches the answer.
type Checker struct {
	Client *http.Client
	// BaseURL is the package document URL ending in "/".
	BaseURL   string
	StatePath string
	Now       func() time.Time
}

// Result is what one check found; empty means nothing to say.
type Result struct {
	Latest     string
	Deprecated string
}

type state struct {
	CheckedAt  time.Time `json:"checked_at"`
	Latest     string    `json:"latest"`
	Version    string    `json:"version"`
	Deprecated string    `json:"deprecated,omitempty"`
}

// Enabled reports whether a check may run: interactive stderr, not CI, not disabled, a release build.
func Enabled() bool {
	if envvar.Enabled(DisableEnv) || envvar.Enabled("CI") {
		return false
	}
	if _, ok := parse(build.Version); !ok {
		return false
	}
	fd := os.Stderr.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// New returns a Checker that caches its answer in configDir.
func New(configDir string) *Checker {
	return &Checker{
		Client:    &http.Client{Timeout: 2 * time.Second},
		BaseURL:   registryURL,
		StatePath: filepath.Join(configDir, stateName),
		Now:       time.Now,
	}
}

// Check reports a newer release and deprecation, querying the registry at most once per interval.
func (c *Checker) Check(ctx context.Context, current string) (Result, error) {
	cur, ok := parse(current)
	if !ok {
		return Result{}, nil
	}
	current = strings.TrimPrefix(current, "v")
	st, err := c.fetch(ctx, current)
	if err != nil {
		return Result{}, err
	}
	res := Result{Deprecated: st.Deprecated}
	if v, ok := parse(st.Latest); ok && v.newerThan(cur) {
		res.Latest = strings.TrimPrefix(st.Latest, "v")
	}
	return res, nil
}

func (c *Checker) fetch(ctx context.Context, current string) (state, error) {
	now := c.Now()
	if st, err := c.read(); err == nil && st.Version == current &&
		now.Sub(st.CheckedAt) < interval && now.After(st.CheckedAt) {
		return st, nil
	}
	var latest struct {
		Version string `json:"version"`
	}
	if _, err := c.get(ctx, "latest", &latest); err != nil {
		return state{}, err
	}
	if _, ok := parse(latest.Version); !ok {
		return state{}, fmt.Errorf("registry returned version %q", latest.Version)
	}
	var mine struct {
		Deprecated json.RawMessage `json:"deprecated"`
	}
	found, err := c.get(ctx, current, &mine)
	if err != nil {
		return state{}, err
	}
	st := state{CheckedAt: now, Latest: latest.Version, Version: current}
	if found {
		var msg string
		if json.Unmarshal(mine.Deprecated, &msg) == nil {
			st.Deprecated = clean(msg)
		}
	}
	_ = c.write(st)
	return st, nil
}

// get treats 404 as not found: a source build was never published.
func (c *Checker) get(ctx context.Context, path string, v any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, http.NoBody)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("registry answered %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return false, err
	}
	return true, nil
}

// clean strips control characters and bounds the length of a registry message.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxMessage {
		s = string(r[:maxMessage]) + "…"
	}
	return s
}

func (c *Checker) read() (state, error) {
	var st state
	b, err := os.ReadFile(c.StatePath)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	if st.CheckedAt.IsZero() {
		return st, errors.New("no check recorded")
	}
	return st, nil
}

func (c *Checker) write(st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.StatePath), 0o700); err != nil {
		return err
	}
	tmp := c.StatePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.StatePath)
}

// Notice returns the stderr text for r, or "". exe, with symlinks resolved, tells the install channel.
func Notice(current string, r Result, exe string) string {
	if r.Latest == "" && r.Deprecated == "" {
		return ""
	}
	current = strings.TrimPrefix(current, "v")
	var b strings.Builder
	b.WriteString("\n")
	if r.Deprecated != "" {
		fmt.Fprintf(&b, "Warning: sepay-cli %s is deprecated: %s\n", current, r.Deprecated)
	}
	if r.Latest != "" {
		fmt.Fprintf(&b, "A new release of sepay-cli is available: %s → %s\n", current, r.Latest)
	}
	if cmd := upgradeCommand(current, r, exe); cmd != "" {
		b.WriteString("To upgrade: " + cmd + "\n")
	}
	fmt.Fprintf(&b, "Turn this notice off with %s=1.\n", DisableEnv)
	return b.String()
}

// upgradeCommand names the upgrade for the channel that installed exe; "" for a Windows binary
// copied by hand, which has no command to offer.
func upgradeCommand(current string, r Result, exe string) string {
	path := filepath.ToSlash(exe)
	switch {
	case strings.Contains(path, "/node_modules/"):
		// A prerelease's notice points to its own channel: latest may still be a placeholder.
		tag := "latest"
		if r.Latest == "" {
			tag = distTag(current)
		}
		return "npm install -g " + Package + "@" + tag + "  (in a project: npm install -D " + Package + "@" + tag + ")"
	case strings.Contains(path, "/Caskroom/"), strings.Contains(path, "/Cellar/"):
		return "brew upgrade sepay"
	case strings.Contains(strings.ToLower(path), "/scoop/apps/"):
		return "scoop update sepay"
	case build.FromGoInstall:
		return "go install github.com/sepayvn/sepay-cli/cmd/sepay@latest"
	case runtime.GOOS == "windows":
		return ""
	}
	// install.sh, or an archive unpacked by hand: reinstall into the same directory.
	dir := filepath.Dir(exe)
	if home, err := os.UserHomeDir(); err == nil && dir == filepath.Join(home, ".local", "bin") {
		return "curl -fsSL " + installScriptURL + " | sh"
	}
	return "curl -fsSL " + installScriptURL + " | SEPAY_INSTALL_DIR=" + shellQuote(dir) + " sh"
}

// shellQuote leaves a plain path as it is and single-quotes anything else.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("/._-+~", r) && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// distTag mirrors the dist-tag step of .github/workflows/release.yml.
func distTag(v string) string {
	_, pre, ok := strings.Cut(v, "-")
	switch {
	case !ok:
		return "latest"
	case strings.HasPrefix(pre, "alpha."):
		return "alpha"
	case strings.HasPrefix(pre, "beta."):
		return "beta"
	default:
		return "next"
	}
}

type version struct {
	core [3]int
	pre  []string
}

func parse(s string) (version, bool) {
	var v version
	s = strings.TrimPrefix(s, "v")
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		v.core[i] = n
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
		for _, id := range v.pre {
			if !validIdentifier(id) {
				return v, false
			}
		}
	}
	return v, true
}

// validIdentifier refuses rather than cleans: the version is printed in the notice.
func validIdentifier(id string) bool {
	if id == "" {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool {
		return r != '-' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
	}) < 0
}

func (v version) newerThan(o version) bool {
	for i := range v.core {
		if v.core[i] != o.core[i] {
			return v.core[i] > o.core[i]
		}
	}
	switch {
	case len(v.pre) == 0 && len(o.pre) == 0:
		return false
	case len(v.pre) == 0:
		return true // a release outranks its prereleases
	case len(o.pre) == 0:
		return false
	}
	for i := 0; i < len(v.pre) && i < len(o.pre); i++ {
		a, b := v.pre[i], o.pre[i]
		if a == b {
			continue
		}
		an, aErr := strconv.Atoi(a)
		bn, bErr := strconv.Atoi(b)
		switch {
		case aErr == nil && bErr == nil:
			return an > bn
		case aErr == nil:
			return false // numeric identifiers rank below alphanumeric ones
		case bErr == nil:
			return true
		default:
			return a > b
		}
	}
	return len(v.pre) > len(o.pre)
}
