package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sepayvn/sepay-cli/internal/build"
	"github.com/sepayvn/sepay-cli/internal/golden"
	upd "github.com/sepayvn/sepay-cli/internal/update"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil"
	"github.com/sepayvn/sepay-cli/pkg/cmdutil/prompttest"
	"github.com/sepayvn/sepay-cli/pkg/iostreams"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// releases serves linux/amd64 releases of each version, the newest named first; each binary holds
// the version it reports. hits counts the requests.
type releases struct {
	srv  *httptest.Server
	hits atomic.Int32
	// onArchive, when set, runs before an archive is served.
	onArchive func(*http.Request)
}

func serve(t *testing.T, versions ...string) *releases {
	t.Helper()
	files := map[string][]byte{}
	for i, v := range versions {
		name := "sepay_" + v + "_linux_amd64.tar.gz"
		archive := tarGz(t, "sepay", v)
		h := sha256.Sum256(archive)
		sums := []byte(hex.EncodeToString(h[:]) + "  " + name + "\n")
		files["download/v"+v+"/"+name] = archive
		files["download/v"+v+"/checksums.txt"] = sums
		if i == 0 {
			files["latest/download/checksums.txt"] = sums
		}
	}
	r := &releases{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		if strings.HasSuffix(req.URL.Path, ".tar.gz") && r.onArchive != nil {
			r.onArchive(req)
		}
		body, ok := files[strings.TrimPrefix(req.URL.Path, "/")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}))
	_, err := tw.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func (r *releases) updater() *upd.Updater {
	return &upd.Updater{
		Client:  r.srv.Client(),
		BaseURL: r.srv.URL + "/",
		GOOS:    "linux",
		GOARCH:  "amd64",
		Probe: func(_ context.Context, path string) (string, error) {
			b, err := os.ReadFile(path)
			return "sepay version " + string(b) + "\n", err
		},
	}
}

func stamp(t *testing.T, version string) {
	t.Helper()
	v, goInstall := build.Version, build.FromGoInstall
	t.Cleanup(func() { build.Version, build.FromGoInstall = v, goInstall })
	build.Version, build.FromGoInstall = version, false
}

type run struct {
	f        *cmdutil.Factory
	out, err *bytes.Buffer
	exe      string
	rel      *releases
}

// setup installs a standalone sepay at current, with releases of versions (the newest first) to update to.
func setup(t *testing.T, current string, versions ...string) *run {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake release is a linux/amd64 archive")
	}
	stamp(t, current)
	ios, _, out, errOut := iostreams.Test()
	exe := filepath.Join(t.TempDir(), "sepay")
	require.NoError(t, os.WriteFile(exe, []byte(current), 0o755))
	return &run{
		f:   &cmdutil.Factory{IOStreams: ios, Global: &cmdutil.GlobalOptions{Output: "table"}},
		out: out, err: errOut, exe: exe,
		rel: serve(t, versions...),
	}
}

func (r *run) exec(args ...string) error {
	return r.execContext(context.Background(), args...)
}

func (r *run) execContext(ctx context.Context, args ...string) error {
	opts := &options{
		executable: func() (string, error) { return r.exe, nil },
		updater:    r.rel.updater,
	}
	cmd := newCmdUpdate(r.f, opts)
	// A nil slice would make cobra read os.Args.
	cmd.SetArgs(append([]string{}, args...))
	cmd.SetOut(r.out)
	cmd.SetErr(r.err)
	return cmd.ExecuteContext(ctx)
}

func (r *run) installed(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(r.exe)
	require.NoError(t, err)
	return string(b)
}

func exitCode(err error) cmdutil.ExitCode {
	if err == nil {
		return cmdutil.ExitOK
	}
	var ec cmdutil.ExitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return cmdutil.ExitError
}

func TestUpdate_ReplacesAStandaloneBinary(t *testing.T) {
	r := setup(t, "0.1.7", "0.2.0")
	r.f.Global.Yes = true

	require.NoError(t, r.exec())

	assert.Equal(t, "0.2.0", r.installed(t))
	assert.Equal(t, "✓ Updated sepay 0.1.7 → 0.2.0\nRelease notes: https://github.com/sepayvn/sepay-cli/releases/tag/v0.2.0\n", r.out.String())
	assert.Equal(t, "Downloading sepay_0.2.0_linux_amd64.tar.gz...\n! cosign not found, checking SHA-256 only\n✓ SHA-256 verified\n", r.err.String())
	assert.Equal(t, []string{"sepay"}, dirNames(t, filepath.Dir(r.exe)), "nothing staged is left behind")
}

func TestUpdate_AsksBeforeReplacing(t *testing.T) {
	for _, yes := range []bool{true, false} {
		t.Run(fmt.Sprint(yes), func(t *testing.T) {
			r := setup(t, "0.1.7", "0.2.0")
			prompttest.Use(t, r.f, prompttest.Step{Label: "Update sepay 0.1.7 → 0.2.0 at " + r.exe + "?", Answer: yes})

			err := r.exec()

			if yes {
				require.NoError(t, err)
				assert.Equal(t, "0.2.0", r.installed(t))
				return
			}
			require.EqualError(t, err, "update cancelled")
			assert.Equal(t, cmdutil.ExitUsage, exitCode(err))
			assert.Equal(t, "0.1.7", r.installed(t))
		})
	}
}

func TestUpdate_WithoutATerminalNeedsYes(t *testing.T) {
	r := setup(t, "0.1.7", "0.2.0")

	err := r.exec()

	require.EqualError(t, err, "pass --yes to update without a prompt")
	assert.Equal(t, cmdutil.ExitUsage, exitCode(err))
	assert.Equal(t, "0.1.7", r.installed(t))
	assert.Equal(t, int32(1), r.rel.hits.Load(), "only the latest version was read")
}

func TestUpdate_RequireSignatureRefusesWithoutCosign(t *testing.T) {
	r := setup(t, "0.1.7", "0.2.0")
	r.f.Global.Yes = true
	t.Setenv(upd.RequireSignatureEnv, "1")

	err := r.exec()

	require.EqualError(t, err, "SEPAY_REQUIRE_SIGNATURE is set but cosign is not on PATH; install cosign (https://docs.sigstore.dev) or unset it")
	assert.Equal(t, cmdutil.ExitError, exitCode(err))
	assert.Equal(t, "0.1.7", r.installed(t))
	assert.Equal(t, int32(1), r.rel.hits.Load(), "nothing is downloaded")
}

func TestUpdate_RequireSignatureOffChecksSHA256Only(t *testing.T) {
	r := setup(t, "0.1.7", "0.2.0")
	r.f.Global.Yes = true
	t.Setenv(upd.RequireSignatureEnv, "0")

	require.NoError(t, r.exec())
	assert.Equal(t, "0.2.0", r.installed(t))
}

func TestUpdate_UpToDate(t *testing.T) {
	cases := []struct{ name, current string }{
		{"at the latest release", "0.2.0"},
		{"on a newer prerelease, which is never downgraded unasked", "0.3.0-rc.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := setup(t, tc.current, "0.2.0")

			require.NoError(t, r.exec())

			assert.Equal(t, "✓ sepay "+tc.current+" is up to date\n", r.out.String())
			assert.Equal(t, tc.current, r.installed(t))
		})
	}
}

func TestUpdate_VersionInstallsThatRelease(t *testing.T) {
	r := setup(t, "0.2.0", "0.2.0", "0.1.9")
	r.f.Global.Yes = true

	require.NoError(t, r.exec("--version", "v0.1.9"))

	assert.Equal(t, "0.1.9", r.installed(t), "a version asked for by name may be older")
}

func TestUpdate_JSON(t *testing.T) {
	r := setup(t, "0.1.7", "0.2.0")
	r.f.Global.Yes = true
	r.f.Global.Output = "json"

	require.NoError(t, r.exec())

	assert.JSONEq(t, `{"updated":true,"from":"0.1.7","to":"0.2.0","path":"`+r.exe+`"}`, r.out.String())
	assert.Equal(t, "! cosign not found, checking SHA-256 only\n", r.err.String(), "no progress under --json")
}

func TestUpdate_RefusesAPackageManagerInstall(t *testing.T) {
	cases := []struct {
		channel, exe string
		goInstalled  bool
		want         string
	}{
		{
			"npm", "/usr/lib/node_modules/@sepayjsc/cli-linux-x64/bin/sepay", false,
			"sepay was installed with npm; update it with: npm install -g sepay-cli@latest  (in a project: npm install -D sepay-cli@latest)",
		},
		{"homebrew", "/opt/homebrew/Caskroom/sepay/0.1.7/sepay", false, "sepay was installed with Homebrew; update it with: brew upgrade sepay"},
		{"scoop", "C:/Users/u/scoop/apps/sepay/current/sepay.exe", false, "sepay was installed with Scoop; update it with: scoop update sepay"},
		{"go", "/home/u/go/bin/sepay", true, "sepay was installed with go install; update it with: go install github.com/sepayvn/sepay-cli/cmd/sepay@latest"},
	}
	for _, tc := range cases {
		t.Run(tc.channel, func(t *testing.T) {
			r := setup(t, "0.1.7", "0.2.0")
			build.FromGoInstall = tc.goInstalled
			r.exe = tc.exe
			r.f.Global.Yes = true

			err := r.exec()

			require.EqualError(t, err, tc.want)
			assert.Equal(t, cmdutil.ExitError, exitCode(err))
			assert.Zero(t, r.rel.hits.Load(), "nothing is downloaded")
		})
	}
}

// A package-manager command printed for --version installs that release, never the latest one.
func TestUpdate_VersionOnAPackageManagerInstallNamesThatRelease(t *testing.T) {
	cases := []struct {
		channel, exe string
		goInstalled  bool
		want         string
	}{
		{
			"npm", "/usr/lib/node_modules/@sepayjsc/cli-linux-x64/bin/sepay", false,
			"sepay was installed with npm; install 0.1.6 with: npm install -g sepay-cli@0.1.6  (in a project: npm install -D sepay-cli@0.1.6)",
		},
		{"go", "/home/u/go/bin/sepay", true, "sepay was installed with go install; install 0.1.6 with: go install github.com/sepayvn/sepay-cli/cmd/sepay@v0.1.6"},
		{"homebrew", "/opt/homebrew/Caskroom/sepay/0.1.7/sepay", false, "sepay was installed with Homebrew, which installs only its newest release, not 0.1.6"},
		{"scoop", "C:/Users/u/scoop/apps/sepay/current/sepay.exe", false, "sepay was installed with Scoop, which installs only its newest release, not 0.1.6"},
	}
	for _, tc := range cases {
		t.Run(tc.channel, func(t *testing.T) {
			r := setup(t, "0.1.7", "0.2.0", "0.1.6")
			build.FromGoInstall = tc.goInstalled
			r.exe = tc.exe

			err := r.exec("--version", "v0.1.6")

			require.EqualError(t, err, tc.want)
			assert.Equal(t, cmdutil.ExitError, exitCode(err))
			assert.Zero(t, r.rel.hits.Load(), "nothing is downloaded")
		})
	}
}

func TestUpdate_Check(t *testing.T) {
	cases := []struct {
		name, current, exe string
		golden             string
		table              string
	}{
		{
			"standalone", "0.1.7", "", "check-standalone",
			"A new release of sepay is available: 0.1.7 → 0.2.0\nTo update: sepay update\n",
		},
		{
			"homebrew", "0.1.7", "/opt/homebrew/Caskroom/sepay/0.1.7/sepay", "check-homebrew",
			"A new release of sepay is available: 0.1.7 → 0.2.0\nTo update: brew upgrade sepay\n",
		},
		{"up to date", "0.2.0", "", "check-up-to-date", "✓ sepay 0.2.0 is up to date\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := setup(t, tc.current, "0.2.0")
			if tc.exe != "" {
				r.exe = tc.exe
			}
			require.NoError(t, r.exec("--check"))
			assert.Equal(t, tc.table, r.out.String())

			r.out.Reset()
			r.f.Global.Output = "json"
			require.NoError(t, r.exec("--check"))
			golden.Assert(t, tc.golden, r.out.Bytes())
			assert.Empty(t, r.err.String())
		})
	}
}

func TestUpdate_RefusesBadFlagsAndBuildsWithoutARelease(t *testing.T) {
	cases := []struct {
		name, current string
		args          []string
		want          string
		code          cmdutil.ExitCode
	}{
		{"dev build", "dev", nil, "this build has no release to update to", cmdutil.ExitError},
		{"snapshot", "0.1.8-SNAPSHOT-abc1234", []string{"--check"}, "this build has no release to update to", cmdutil.ExitError},
		{"bad version", "0.1.7", []string{"--version", "0.2.0\n"}, `invalid value "0.2.0\n" for --version (must look like 0.2.0 or 0.2.0-rc.1)`, cmdutil.ExitUsage},
		{"check with version", "0.1.7", []string{"--check", "--version", "0.2.0"}, "--check and --version are mutually exclusive", cmdutil.ExitUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := setup(t, tc.current, "0.2.0")
			r.f.Global.Yes = true

			err := r.exec(tc.args...)

			require.EqualError(t, err, tc.want)
			assert.Equal(t, tc.code, exitCode(err))
			assert.Zero(t, r.rel.hits.Load())
		})
	}
}

func TestUpdate_ABadDownloadLeavesTheBinaryAlone(t *testing.T) {
	r := setup(t, "0.1.7", "0.2.0")
	r.f.Global.Yes = true

	err := r.exec("--version", "0.3.0")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not update to 0.3.0")
	assert.Equal(t, "0.1.7", r.installed(t))
	assert.Equal(t, []string{"sepay"}, dirNames(t, filepath.Dir(r.exe)))
}

func TestUpdate_InterruptedDownloadLeavesTheBinaryAlone(t *testing.T) {
	r := setup(t, "0.1.7", "0.2.0")
	r.f.Global.Yes = true
	ctx, cancel := context.WithCancel(context.Background())
	r.rel.onArchive = func(req *http.Request) {
		cancel()
		<-req.Context().Done()
	}

	err := r.execContext(ctx)

	require.ErrorIs(t, err, context.Canceled, "main reports it as exit 130")
	assert.Equal(t, "0.1.7", r.installed(t))
	assert.Equal(t, []string{"sepay"}, dirNames(t, filepath.Dir(r.exe)))
}

func TestUpdate_UnwritableDirectoryNamesTheFix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to any directory")
	}
	r := setup(t, "0.1.7", "0.2.0")
	r.f.Global.Yes = true
	dir := filepath.Dir(r.exe)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := r.exec()

	require.EqualError(t, err, "cannot write to "+dir+"; run: sudo sepay update")
	assert.Equal(t, "0.1.7", r.installed(t))
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}
