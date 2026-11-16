package update

import (
	"archive/tar"
	"archive/zip"
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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type entry struct {
	name string
	body string
}

func tarGz(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func zipped(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		require.NoError(t, err)
		_, err = w.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// release serves files at their GitHub Releases paths, e.g. "download/v0.2.0/checksums.txt".
func release(t *testing.T, files map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func checksumsFor(files map[string][]byte) []byte {
	var b strings.Builder
	for name, body := range files {
		fmt.Fprintf(&b, "%s  %s\n", sum(body), name)
	}
	return []byte(b.String())
}

func testUpdater(srv *httptest.Server, goos, goarch string) *Updater {
	return &Updater{
		Client:  srv.Client(),
		BaseURL: srv.URL + "/",
		GOOS:    goos,
		GOARCH:  goarch,
		Probe: func(_ context.Context, path string) (string, error) {
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			// The fake binary holds the version it reports.
			return "sepay version " + string(b) + " (abc1234, 2026-11-12)\nhttps://example.test\n", nil
		},
	}
}

// publish serves release 0.2.0, whose linux/amd64 and windows/amd64 archives hold binaries
// reporting that version.
func publish(t *testing.T) (*httptest.Server, map[string][]byte) {
	t.Helper()
	const version = "0.2.0"
	assets := map[string][]byte{
		"sepay_" + version + "_linux_amd64.tar.gz": tarGz(t, entry{"LICENSE", "license"}, entry{"sepay", version}),
		"sepay_" + version + "_windows_amd64.zip":  zipped(t, entry{"LICENSE", "license"}, entry{"sepay.exe", version}),
	}
	files := map[string][]byte{}
	for name, body := range assets {
		files["download/v"+version+"/"+name] = body
	}
	files["download/v"+version+"/checksums.txt"] = checksumsFor(assets)
	files["latest/download/checksums.txt"] = checksumsFor(assets)
	return release(t, files), files
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

func TestLatest_ReadsTheVersionOffThePlatformArchive(t *testing.T) {
	srv, _ := publish(t)
	for _, goos := range []string{"linux", "windows"} {
		v, err := testUpdater(srv, goos, "amd64").Latest(context.Background())
		require.NoError(t, err, goos)
		assert.Equal(t, "0.2.0", v, goos)
	}
}

func TestLatest_NoArchiveForThisPlatform(t *testing.T) {
	srv, _ := publish(t)
	_, err := testUpdater(srv, "darwin", "arm64").Latest(context.Background())
	require.ErrorContains(t, err, "no archive for darwin/arm64")
}

func TestLatest_RefusesAVersionCarryingTerminalControls(t *testing.T) {
	name := "sepay_0.2.0-\x1b[2J_linux_amd64.tar.gz"
	srv := release(t, map[string][]byte{"latest/download/checksums.txt": checksumsFor(map[string][]byte{name: []byte("x")})})
	_, err := testUpdater(srv, "linux", "amd64").Latest(context.Background())
	require.Error(t, err)
}

func TestFetch_StagesTheVerifiedBinaryAndLeavesNothingElse(t *testing.T) {
	srv, _ := publish(t)
	for _, goos := range []string{"linux", "windows"} {
		dir := t.TempDir()
		staged, err := testUpdater(srv, goos, "amd64").Fetch(context.Background(), "0.2.0", dir)
		require.NoError(t, err, goos)

		assert.Equal(t, dir, filepath.Dir(staged), "staged next to the target so Replace is a rename")
		b, err := os.ReadFile(staged)
		require.NoError(t, err)
		assert.Equal(t, "0.2.0", string(b))
		assert.Equal(t, []string{filepath.Base(staged)}, dirNames(t, dir), "the download directory is removed")
	}
}

func TestFetch_RefusesWhatChecksumsTxtDoesNotVouchFor(t *testing.T) {
	const v = "0.2.0"
	archive := "sepay_" + v + "_linux_amd64.tar.gz"
	good := tarGz(t, entry{"sepay", v})
	cases := map[string]struct {
		files map[string][]byte
		want  string
	}{
		"archive swapped": {map[string][]byte{
			"download/v0.2.0/checksums.txt": checksumsFor(map[string][]byte{archive: good}),
			"download/v0.2.0/" + archive:    tarGz(t, entry{"sepay", "evil"}),
		}, "checksum mismatch"},
		"archive not listed": {map[string][]byte{
			"download/v0.2.0/checksums.txt": checksumsFor(map[string][]byte{"other.tar.gz": good}),
			"download/v0.2.0/" + archive:    good,
		}, "has no " + archive},
		"uppercase digest": {map[string][]byte{
			"download/v0.2.0/checksums.txt": []byte(strings.ToUpper(sum(good)) + "  " + archive + "\n"),
			"download/v0.2.0/" + archive:    good,
		}, "has no " + archive},
		"no checksums.txt": {map[string][]byte{
			"download/v0.2.0/" + archive: good,
		}, "404"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := testUpdater(release(t, c.files), "linux", "amd64").Fetch(context.Background(), v, dir)
			require.ErrorContains(t, err, c.want)
			assert.Empty(t, dirNames(t, dir))
		})
	}
}

func TestFetch_TakesOnlyTheBinaryAtTheArchiveRoot(t *testing.T) {
	const v = "0.2.0"
	archive := "sepay_" + v + "_linux_amd64.tar.gz"
	body := tarGz(t, entry{"../sepay", "escaped"}, entry{"bin/sepay", "nested"})
	srv := release(t, map[string][]byte{
		"download/v0.2.0/checksums.txt": checksumsFor(map[string][]byte{archive: body}),
		"download/v0.2.0/" + archive:    body,
	})
	parent := t.TempDir()
	dir := filepath.Join(parent, "bin")
	require.NoError(t, os.Mkdir(dir, 0o755))

	_, err := testUpdater(srv, "linux", "amd64").Fetch(context.Background(), v, dir)
	require.ErrorContains(t, err, "holds no sepay")
	assert.Equal(t, []string{"bin"}, dirNames(t, parent))
	assert.Empty(t, dirNames(t, dir))
}

func TestFetch_RefusesAVersionThatIsNotARelease(t *testing.T) {
	srv, _ := publish(t)
	for _, v := range []string{"v0.2.0", "0.2.0/../../x", "0.2", ""} {
		_, err := testUpdater(srv, "linux", "amd64").Fetch(context.Background(), v, t.TempDir())
		require.ErrorContains(t, err, "invalid version", v)
	}
}

func TestFetch_ChecksTheBinaryRunsAndIsTheVersionAskedFor(t *testing.T) {
	srv, _ := publish(t)
	cases := map[string]struct {
		probe func(context.Context, string) (string, error)
		want  string
	}{
		"does not run": {func(context.Context, string) (string, error) {
			return "", errors.New("exec format error")
		}, "does not run on this machine"},
		"other version": {func(context.Context, string) (string, error) {
			return "sepay version 0.2.01\n", nil
		}, `reports "sepay version 0.2.01", not version 0.2.0`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			u := testUpdater(srv, "linux", "amd64")
			u.Probe = c.probe
			dir := t.TempDir()
			_, err := u.Fetch(context.Background(), "0.2.0", dir)
			require.ErrorContains(t, err, c.want)
			assert.Empty(t, dirNames(t, dir))
		})
	}

	u := testUpdater(srv, "linux", "amd64")
	u.Probe = func(context.Context, string) (string, error) { return "sepay version 0.2.0\n", nil }
	_, err := u.Fetch(context.Background(), "0.2.0", t.TempDir())
	require.NoError(t, err, "a banner without commit and date")
}

func TestFetch_WithCosignTheSignatureIsRequired(t *testing.T) {
	const v = "0.2.0"
	srv, files := publish(t)

	var verified []string
	u := testUpdater(srv, "linux", "amd64")
	u.Cosign = func(_ context.Context, checksums, sig, cert string) error {
		verified = []string{filepath.Base(checksums), filepath.Base(sig), filepath.Base(cert)}
		return nil
	}
	_, err := u.Fetch(context.Background(), v, t.TempDir())
	require.ErrorContains(t, err, "has no signature for checksums.txt", "a missing signature fails closed")
	assert.Nil(t, verified)

	files["download/v0.2.0/checksums.txt.sig"] = []byte("sig")
	files["download/v0.2.0/checksums.txt.pem"] = []byte("pem")
	srv = release(t, files)
	u.Client, u.BaseURL = srv.Client(), srv.URL+"/"
	_, err = u.Fetch(context.Background(), v, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, []string{"checksums.txt", "checksums.txt.sig", "checksums.txt.pem"}, verified)

	u.Cosign = func(context.Context, string, string, string) error { return errors.New("exit status 1") }
	dir := t.TempDir()
	_, err = u.Fetch(context.Background(), v, dir)
	require.ErrorContains(t, err, "not signed by the sepay-cli release workflow")
	assert.Empty(t, dirNames(t, dir))
}

func TestNewUpdater_RefusesARedirectToPlainHTTP(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("forged"))
	}))
	t.Cleanup(plain.Close)
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(tlsSrv.Close)

	u := NewUpdater()
	u.Client.Transport = tlsSrv.Client().Transport
	u.BaseURL = tlsSrv.URL + "/"
	_, err := u.Latest(context.Background())
	require.ErrorContains(t, err, "refusing a redirect to http")
}

func TestSave_RefusesABodyOverTheLimit(t *testing.T) {
	srv := release(t, map[string][]byte{"big": []byte("12345")})
	u := testUpdater(srv, "linux", "amd64")
	dest := filepath.Join(t.TempDir(), "big")
	require.ErrorContains(t, u.save(context.Background(), "big", dest, 4), "larger than 4 bytes")
	require.NoError(t, u.save(context.Background(), "big", dest, 5))
}

func TestReplace_PutsTheNewBinaryInPlace(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "sepay")
	staged := filepath.Join(dir, ".sepay-new-1")
	require.NoError(t, os.WriteFile(exe, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(staged, []byte("new"), 0o755))

	require.NoError(t, Replace(exe, staged))
	b, err := os.ReadFile(exe)
	require.NoError(t, err)
	assert.Equal(t, "new", string(b))
	assert.NoFileExists(t, staged)

	RemoveOld(exe)
	if runtime.GOOS == "windows" {
		assert.NoFileExists(t, exe+".old")
	}
	assert.Equal(t, []string{"sepay"}, dirNames(t, dir))
}
