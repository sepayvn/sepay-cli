package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

const (
	releasesURL = "https://github.com/sepayvn/sepay-cli/releases/"
	// SignerIdentity is the certificate identity of the release workflow, as in scripts/install.sh.
	SignerIdentity = `^https://github\.com/sepayvn/sepay-cli/\.github/workflows/release\.yml@refs/(heads/main|tags/v.*)$`
	signerIssuer   = "https://token.actions.githubusercontent.com"
	// RequireSignatureEnv refuses an update without cosign, as it refuses an install in the scripts.
	RequireSignatureEnv = "SEPAY_REQUIRE_SIGNATURE"

	maxChecksums = 1 << 20
	maxArchive   = 200 << 20
	maxBinary    = 200 << 20
)

// ErrChecksum means a download does not match its SHA-256 in checksums.txt.
var ErrChecksum = errors.New("checksum mismatch")

// Updater fetches a release of sepay for one platform from GitHub Releases.
type Updater struct {
	Client *http.Client
	// BaseURL is the releases URL ending in "/".
	BaseURL      string
	GOOS, GOARCH string
	// Cosign verifies checksums.txt against its signature and certificate; nil when cosign is not
	// installed, and then only the SHA-256 is checked.
	Cosign func(ctx context.Context, checksums, sig, cert string) error
	// Probe runs the unpacked binary and returns its version banner.
	Probe func(ctx context.Context, path string) (string, error)
}

// NewUpdater returns an Updater for the running platform.
func NewUpdater() *Updater {
	u := &Updater{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
			// GitHub answers a release download with a redirect to its asset host; a redirect
			// to plain http would let anyone on the path swap the archive and checksums.txt together.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				if req.URL.Scheme != "https" && via[0].URL.Scheme == "https" {
					return fmt.Errorf("refusing a redirect to %s", req.URL.Scheme)
				}
				return nil
			},
		},
		BaseURL: releasesURL,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
		Probe:   probe,
	}
	if path, err := exec.LookPath("cosign"); err == nil {
		u.Cosign = func(ctx context.Context, checksums, sig, cert string) error {
			//nolint:gosec // G204: cosign from PATH, fixed arguments; the file paths are our own temp files
			return exec.CommandContext(ctx, path, "verify-blob", checksums,
				"--signature", sig, "--certificate", cert,
				"--certificate-identity-regexp", SignerIdentity,
				"--certificate-oidc-issuer", signerIssuer).Run()
		}
	}
	return u
}

// ReleaseNotesURL is the GitHub release page of version.
func ReleaseNotesURL(version string) string {
	return releasesURL + "tag/v" + version
}

// Archive names the release archive of version for the Updater's platform.
func (u *Updater) Archive(version string) string {
	ext := ".tar.gz"
	if u.GOOS == "windows" {
		ext = ".zip"
	}
	return "sepay_" + version + "_" + u.GOOS + "_" + u.GOARCH + ext
}

// Latest returns the newest final release, read off the archive names in its checksums.txt, so
// no GitHub API call (and no API rate limit) is needed.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	sums, err := u.checksums(ctx, "latest/download/checksums.txt")
	if err != nil {
		return "", err
	}
	prefix, suffix := "sepay_", strings.TrimPrefix(u.Archive(""), "sepay_")
	for name := range sums {
		if v, ok := strings.CutPrefix(name, prefix); ok && strings.HasSuffix(v, suffix) {
			v = strings.TrimSuffix(v, suffix)
			if _, ok := parse(v); ok && !strings.HasPrefix(v, "v") {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("the latest release has no archive for %s/%s", u.GOOS, u.GOARCH)
}

// Fetch downloads version for this platform into dir, checks it against checksums.txt (and its
// signature when Cosign is set), unpacks the binary and probes it. It returns the path of the
// unpacked binary, inside dir so that Replace is a rename; on error nothing is left in dir.
func (u *Updater) Fetch(ctx context.Context, version, dir string) (string, error) {
	if !ValidVersion(version) {
		return "", fmt.Errorf("invalid version %q", version)
	}
	tag := "download/v" + version + "/"
	archive := u.Archive(version)

	work, err := os.MkdirTemp(dir, ".sepay-update-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(work) }()

	sumsPath := filepath.Join(work, "checksums.txt")
	if err := u.save(ctx, tag+"checksums.txt", sumsPath, maxChecksums); err != nil {
		return "", err
	}
	if u.Cosign != nil {
		// Whoever can swap the archive can also delete the signature, so a missing one stops here.
		sig, cert := filepath.Join(work, "checksums.txt.sig"), filepath.Join(work, "checksums.txt.pem")
		if err := u.save(ctx, tag+"checksums.txt.sig", sig, maxChecksums); err != nil {
			return "", fmt.Errorf("release %s has no signature for checksums.txt: %w", version, err)
		}
		if err := u.save(ctx, tag+"checksums.txt.pem", cert, maxChecksums); err != nil {
			return "", fmt.Errorf("release %s has no signature for checksums.txt: %w", version, err)
		}
		if err := u.Cosign(ctx, sumsPath, sig, cert); err != nil {
			return "", fmt.Errorf("checksums.txt of %s is not signed by the sepay-cli release workflow: %w", version, err)
		}
	}
	sums, err := readChecksums(sumsPath)
	if err != nil {
		return "", err
	}
	want, ok := sums[archive]
	if !ok {
		return "", fmt.Errorf("release %s has no %s", version, archive)
	}

	archivePath := filepath.Join(work, archive)
	if err := u.save(ctx, tag+archive, archivePath, maxArchive); err != nil {
		return "", err
	}
	got, err := fileSHA256(archivePath)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", fmt.Errorf("%s: %w", archive, ErrChecksum)
	}

	bin := "sepay"
	if u.GOOS == "windows" {
		bin = "sepay.exe"
	}
	unpacked := filepath.Join(work, bin)
	if strings.HasSuffix(archive, ".zip") {
		err = unzip(archivePath, bin, unpacked)
	} else {
		err = untar(archivePath, bin, unpacked)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", archive, err)
	}

	banner, err := u.Probe(ctx, unpacked)
	if err != nil {
		return "", fmt.Errorf("the downloaded sepay does not run on this machine: %w", err)
	}
	// "sepay version X" alone, or followed by " (commit, date)".
	if line := firstLine(banner); line != "sepay version "+version && !strings.HasPrefix(line, "sepay version "+version+" ") {
		return "", fmt.Errorf("the downloaded sepay reports %q, not version %s", line, version)
	}

	staged, err := os.CreateTemp(dir, ".sepay-new-*")
	if err != nil {
		return "", err
	}
	_ = staged.Close()
	if err := os.Rename(unpacked, staged.Name()); err != nil {
		_ = os.Remove(staged.Name())
		return "", err
	}
	return staged.Name(), nil
}

// Replace puts the binary at staged in place of exe. A running Windows executable cannot be
// overwritten but can be renamed, so there exe moves to exe+".old" first; RemoveOld deletes it later.
func Replace(exe, staged string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(staged, exe)
	}
	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(staged, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	return nil
}

// RemoveOld deletes the executable a Windows update left beside exe. It fails silently while
// that file still runs.
func RemoveOld(exe string) {
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
	}
}

func (u *Updater) checksums(ctx context.Context, path string) (map[string]string, error) {
	f, err := os.CreateTemp("", "sepay-checksums-")
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	defer func() { _ = os.Remove(f.Name()) }()
	if err := u.save(ctx, path, f.Name(), maxChecksums); err != nil {
		return nil, err
	}
	return readChecksums(f.Name())
}

// save downloads BaseURL+path to dest, refusing a body over limit bytes.
func (u *Updater) save(ctx context.Context, path, dest string, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.BaseURL+path, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := u.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The status code alone: the reason phrase is server text bound for the terminal.
		return fmt.Errorf("%s answered %d", u.BaseURL+path, resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // G304: a temp path this package created
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s is larger than %d bytes", u.BaseURL+path, limit)
	}
	return nil
}

// readChecksums parses "<sha256>  <name>" lines, skipping any line that is not one.
func readChecksums(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: a temp path this package created
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sums := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(fields[0]); err != nil || strings.ToLower(fields[0]) != fields[0] {
			continue
		}
		sums[fields[1]] = fields[0]
	}
	return sums, sc.Err()
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: a temp path this package created
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// untar writes the regular file called name at the archive root to dest. Every other entry is
// ignored, so no path inside the archive reaches the file system.
func untar(archive, name, dest string) error {
	f, err := os.Open(archive) //nolint:gosec // G304: a temp path this package created
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("holds no %s", name)
		}
		if err != nil {
			return err
		}
		if hdr.Name == name && hdr.Typeflag == tar.TypeReg {
			return writeBinary(tr, dest)
		}
	}
}

func unzip(archive, name, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	i := slices.IndexFunc(zr.File, func(zf *zip.File) bool { return zf.Name == name && zf.Mode().IsRegular() })
	if i < 0 {
		return fmt.Errorf("holds no %s", name)
	}
	rc, err := zr.File[i].Open()
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	return writeBinary(rc, dest)
}

func writeBinary(r io.Reader, dest string) error {
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) //nolint:gosec // G302: an executable everyone on the machine may run, as the one it replaces
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, maxBinary+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxBinary {
		return fmt.Errorf("binary is larger than %d bytes", maxBinary)
	}
	return nil
}

// probe runs "<path> version" with the update check off and returns what it printed.
func probe(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	cmd.Env = append(os.Environ(), DisableEnv+"=1")
	out, err := cmd.Output()
	return string(out), err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return clean(line)
}
