package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/build"
)

func TestNewerThan(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true},
		{"0.1.1", "0.1.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0", false},
		{"0.1.0", "0.2.0", false},
		{"0.1.0", "0.1.0-rc.1", true},
		{"0.1.0-rc.1", "0.1.0", false},
		{"0.1.0-rc.10", "0.1.0-rc.2", true},
		{"0.1.0-rc.2", "0.1.0-rc.10", false},
		{"0.1.0-rc.1", "0.1.0-beta.1", true},
		{"0.1.0-rc.1.1", "0.1.0-rc.1", true},
		{"0.1.0-alpha", "0.1.0-1", true},
	}
	for _, c := range cases {
		a, ok := parse(c.a)
		require.True(t, ok, c.a)
		b, ok := parse(c.b)
		require.True(t, ok, c.b)
		assert.Equal(t, c.want, a.newerThan(b), "%s > %s", c.a, c.b)
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{"dev", "", "1.2", "1.2.3.4", "01.2.3", "1.2.3-", "v1.x.0",
		"1.2.3-rc..1", "1.2.3-\x1b[2J", "1.2.3-rc\u202e1", "1.2.3-rc 1"} {
		_, ok := parse(s)
		assert.False(t, ok, s)
	}
	_, ok := parse("v0.1.0")
	assert.True(t, ok)
}

func TestCheck_RefusesAVersionThatCarriesTerminalControls(t *testing.T) {
	hits := 0
	srv := registry(t, "9.9.9-\u202egnp.exe", nil, &hits)
	c := &Checker{Client: srv.Client(), BaseURL: srv.URL + "/", StatePath: filepath.Join(t.TempDir(), "s.json"), Now: time.Now}

	res, err := c.Check(context.Background(), "0.1.0")
	assert.Error(t, err)
	assert.Empty(t, res.Latest)
}

func TestCleanStripsBidiControls(t *testing.T) {
	assert.Equal(t, "use 0.2.1 exe.gnp", clean("use 0.2.1 \u202eexe.gnp"))
}

func registry(t *testing.T, latest string, deprecated map[string]string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		v := strings.TrimPrefix(r.URL.Path, "/")
		if v == "latest" {
			v = latest
		} else if v != latest && !known[v] {
			http.NotFound(w, r)
			return
		}
		doc := map[string]any{"name": Package, "version": v}
		if msg, ok := deprecated[v]; ok {
			doc["deprecated"] = msg
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	t.Cleanup(srv.Close)
	return srv
}

var known = map[string]bool{"0.1.0": true, "0.1.0-rc.1": true, "0.2.0": true, "0.2.0-rc.1": true}

func checker(srv *httptest.Server, dir string, now *time.Time) *Checker {
	return &Checker{Client: srv.Client(), BaseURL: srv.URL + "/", StatePath: filepath.Join(dir, stateName),
		Now: func() time.Time { return *now }}
}

func TestCheckAsksOncePerDay(t *testing.T) {
	hits := 0
	srv := registry(t, "0.2.0", nil, &hits)
	now := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	c := checker(srv, t.TempDir(), &now)

	got, err := c.Check(context.Background(), "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, Result{Latest: "0.2.0"}, got)
	assert.Equal(t, 2, hits, "one request for latest, one for the running version")

	now = now.Add(23 * time.Hour)
	got, err = c.Check(context.Background(), "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, "0.2.0", got.Latest, "the cached answer still says a newer release exists")
	assert.Equal(t, 2, hits, "no request inside 24h")

	now = now.Add(2 * time.Hour)
	_, err = c.Check(context.Background(), "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, 4, hits, "asks again once the day is over")

	info, err := os.Stat(c.StatePath)
	require.NoError(t, err)
	if os.PathSeparator == '/' {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestCheckAsksAgainAfterAnUpgrade(t *testing.T) {
	hits := 0
	srv := registry(t, "0.2.0", map[string]string{"0.1.0": "old"}, &hits)
	now := time.Now()
	c := checker(srv, t.TempDir(), &now)

	got, err := c.Check(context.Background(), "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, "old", got.Deprecated)

	got, err = c.Check(context.Background(), "0.2.0")
	require.NoError(t, err)
	assert.Equal(t, Result{}, got, "the cache of 0.1.0 must not follow the user to 0.2.0")
	assert.Equal(t, 4, hits)
}

func TestCheckUpToDate(t *testing.T) {
	hits := 0
	srv := registry(t, "0.1.0", nil, &hits)
	now := time.Now()
	c := checker(srv, t.TempDir(), &now)

	for _, cur := range []string{"0.1.0", "v0.1.0", "0.2.0-rc.1"} {
		got, err := c.Check(context.Background(), cur)
		require.NoError(t, err)
		assert.Empty(t, got.Latest, cur)
	}
	got, err := c.Check(context.Background(), "0.1.0-rc.1")
	require.NoError(t, err)
	assert.Equal(t, "0.1.0", got.Latest, "a prerelease user hears about its final release")
}

func TestCheckDeprecated(t *testing.T) {
	hits := 0
	srv := registry(t, "0.2.1", map[string]string{"0.2.0": "SECURITY: vulnerable, fixed in 0.2.1"}, &hits)
	known["0.2.1"] = true
	t.Cleanup(func() { delete(known, "0.2.1") })
	now := time.Now()

	got, err := checker(srv, t.TempDir(), &now).Check(context.Background(), "0.2.0")
	require.NoError(t, err)
	assert.Equal(t, Result{Latest: "0.2.1", Deprecated: "SECURITY: vulnerable, fixed in 0.2.1"}, got)
}

func TestCheckDeprecatedWithoutNewer(t *testing.T) {
	// The withdrawn version is the newest; latest was moved back to 0.1.0.
	hits := 0
	srv := registry(t, "0.1.0", map[string]string{"0.2.0": "withdrawn, use 0.1.0"}, &hits)
	now := time.Now()

	got, err := checker(srv, t.TempDir(), &now).Check(context.Background(), "0.2.0")
	require.NoError(t, err)
	assert.Equal(t, Result{Deprecated: "withdrawn, use 0.1.0"}, got, "never suggests a downgrade as a new release")
}

func TestCheckUnpublishedVersion(t *testing.T) {
	hits := 0
	srv := registry(t, "0.2.0", nil, &hits)
	now := time.Now()

	got, err := checker(srv, t.TempDir(), &now).Check(context.Background(), "0.1.5")
	require.NoError(t, err, "a 404 for the running version is not an error")
	assert.Equal(t, Result{Latest: "0.2.0"}, got)
}

func TestCheckSkipsDevBuild(t *testing.T) {
	hits := 0
	srv := registry(t, "9.9.9", nil, &hits)
	now := time.Now()
	got, err := checker(srv, t.TempDir(), &now).Check(context.Background(), "dev")
	require.NoError(t, err)
	assert.Equal(t, Result{}, got)
	assert.Zero(t, hits, "a dev build never calls the registry")
}

func TestCheckRegistryFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	now := time.Now()
	c := checker(srv, t.TempDir(), &now)

	got, err := c.Check(context.Background(), "0.1.0")
	assert.Error(t, err)
	assert.Equal(t, Result{}, got)
	_, statErr := os.Stat(c.StatePath)
	assert.True(t, os.IsNotExist(statErr), "a failed check is not cached, so the next run retries")
}

func TestCheckRejectsGarbageVersion(t *testing.T) {
	hits := 0
	srv := registry(t, "latest", nil, &hits)
	now := time.Now()
	got, err := checker(srv, t.TempDir(), &now).Check(context.Background(), "0.1.0")
	assert.Error(t, err)
	assert.Equal(t, Result{}, got)
}

func TestCleanStripsTerminalEscapes(t *testing.T) {
	msg := "use 0.2.1\x1b]8;;https://evil.example\x07click\x1b]8;;\x07\x1b[2J\nsecond line\r\tend"
	got := clean(msg)
	assert.NotContains(t, got, "\x1b")
	assert.NotContains(t, got, "\x07")
	assert.NotContains(t, got, "\n")
	assert.NotContains(t, got, "\r")
	assert.Equal(t, "use 0.2.1 ]8;;https://evil.example click ]8;; [2J second line end", got)

	long := strings.Repeat("a", 400)
	assert.Equal(t, maxMessage, len([]rune(clean(long)))-1, "cut to maxMessage plus an ellipsis")
}

func TestNotice(t *testing.T) {
	const npmExe = "/home/u/.npm-global/lib/node_modules/@sepayjsc/cli-linux-x64/bin/sepay"
	assert.Empty(t, Notice("0.1.0", Result{}, npmExe), "nothing to say prints nothing")

	npm := Notice("v0.1.0", Result{Latest: "0.2.0"}, npmExe)
	assert.Contains(t, npm, "0.1.0 → 0.2.0")
	assert.Contains(t, npm, "npm install -g sepay-cli@latest")
	assert.NotContains(t, npm, "github.com")
	assert.Contains(t, npm, DisableEnv+"=1")
	assert.NotContains(t, npm, "deprecated")

	archive := Notice("0.1.0", Result{Latest: "0.2.0"}, "/usr/local/bin/sepay")
	assert.NotContains(t, archive, "npm install")
	assert.Contains(t, archive, "0.1.0 → 0.2.0")
	assert.NotContains(t, archive, "github.com")

	vuln := Notice("0.2.0", Result{Latest: "0.2.1", Deprecated: "SECURITY: fixed in 0.2.1"}, npmExe)
	assert.Contains(t, vuln, "Warning: sepay-cli 0.2.0 is deprecated: SECURITY: fixed in 0.2.1")
	assert.Contains(t, vuln, "0.2.0 → 0.2.1")
	assert.Less(t, strings.Index(vuln, "Warning:"), strings.Index(vuln, "A new release"), "the warning comes first")

	withdrawn := Notice("0.2.0", Result{Deprecated: "withdrawn, use 0.1.0"}, "/usr/local/bin/sepay")
	assert.Contains(t, withdrawn, "Warning: sepay-cli 0.2.0 is deprecated: withdrawn, use 0.1.0")
	assert.NotContains(t, withdrawn, "A new release")
	assert.NotContains(t, withdrawn, "github.com")
}

// While only prereleases exist, latest is a placeholder with no sepay command.
func TestNotice_DeprecatedPrereleaseStaysOnItsChannel(t *testing.T) {
	const npmExe = "/home/u/.npm-global/lib/node_modules/@sepayjsc/cli-linux-x64/bin/sepay"
	cases := []struct{ current, tag string }{
		{"0.1.0-rc.2", "next"},
		{"v0.1.0-beta.1", "beta"},
		{"0.1.0-alpha.3", "alpha"},
		{"0.1.0", "latest"},
	}
	for _, tc := range cases {
		got := Notice(tc.current, Result{Deprecated: "broken, use a newer build"}, npmExe)
		assert.Contains(t, got, "npm install -g sepay-cli@"+tc.tag+" ", tc.current)
		assert.Contains(t, got, "npm install -D sepay-cli@"+tc.tag+")", tc.current)
	}

	newer := Notice("0.1.0-rc.3", Result{Latest: "0.1.0", Deprecated: "superseded"}, npmExe)
	assert.Contains(t, newer, "npm install -g sepay-cli@latest", "a newer stable release is the upgrade")
}

func TestNotice_UpgradeCommandFollowsTheInstallChannel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the install.sh rows are Unix paths")
	}
	t.Setenv("HOME", "/home/u")
	cases := []struct {
		name, exe   string
		goInstalled bool
		want        string
	}{
		{"npm", "/home/u/.npm-global/lib/node_modules/@sepayjsc/cli-linux-x64/bin/sepay", false, "npm install -g sepay-cli@latest"},
		{"homebrew cask", "/opt/homebrew/Caskroom/sepay/0.1.0/sepay", false, "brew upgrade sepay"},
		{"homebrew cellar", "/home/linuxbrew/.linuxbrew/Cellar/sepay/0.1.0/bin/sepay", false, "brew upgrade sepay"},
		{"scoop", `C:/Users/u/scoop/apps/sepay/current/sepay.exe`, false, "scoop update sepay"},
		{"go install", "/home/u/go/bin/sepay", true, "go install github.com/sepayvn/sepay-cli/cmd/sepay@latest"},
		{"install.sh default dir", "/home/u/.local/bin/sepay", false, "curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | sh"},
		{"install.sh other dir", "/usr/local/bin/sepay", false, "curl -fsSL https://get.sepay.vn/sepay-cli-install.sh | SEPAY_INSTALL_DIR=/usr/local/bin sh"},
		{"dir that needs quoting", "/home/u/my tools/it's/sepay", false, `SEPAY_INSTALL_DIR='/home/u/my tools/it'\''s' sh`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func(v bool) { build.FromGoInstall = v }(build.FromGoInstall)
			build.FromGoInstall = tc.goInstalled
			got := Notice("0.1.0", Result{Latest: "0.2.0"}, tc.exe)
			assert.Contains(t, got, "To upgrade: ")
			assert.Contains(t, got, tc.want)
		})
	}
}
