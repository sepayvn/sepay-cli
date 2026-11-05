package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHosts_SaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEPAY_CONFIG_DIR", dir)

	h, err := LoadHosts()
	require.NoError(t, err)
	h.Active = "work"
	h.Profiles["work"] = &Profile{Host: "my.sepay.vn", Scopes: []string{"sandbox"}, Email: "dev@shop.vn"}
	require.NoError(t, h.Save())

	got, err := LoadHosts()
	require.NoError(t, err)
	assert.Equal(t, "work", got.Active)
	require.Contains(t, got.Profiles, "work")
	assert.Equal(t, "dev@shop.vn", got.Profiles["work"].Email)
	assert.Equal(t, []string{"sandbox"}, got.Profiles["work"].Scopes)
}

func TestHosts_UpdateMergesConcurrentWrite(t *testing.T) {
	dir := t.TempDir()

	h0, err := LoadHostsFrom(dir)
	require.NoError(t, err)
	h0.Profiles["a"] = &Profile{Host: "h"}
	require.NoError(t, h0.Save())

	mine, err := LoadHostsFrom(dir)
	require.NoError(t, err)

	// A concurrent process adds B between the load and the write.
	other, err := LoadHostsFrom(dir)
	require.NoError(t, err)
	other.Profiles["b"] = &Profile{Host: "h"}
	require.NoError(t, other.Save())

	require.NoError(t, mine.Update(func(h *Hosts) error {
		h.Profiles["c"] = &Profile{Host: "h"}
		return nil
	}))

	final, err := LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.Contains(t, final.Profiles, "a")
	assert.Contains(t, final.Profiles, "b")
	assert.Contains(t, final.Profiles, "c")

	assert.Contains(t, mine.Profiles, "b")
	assert.Contains(t, mine.Profiles, "c")
}

func TestHosts_UpdateAbortsOnMutateError(t *testing.T) {
	dir := t.TempDir()
	h, err := LoadHostsFrom(dir)
	require.NoError(t, err)
	h.Profiles["a"] = &Profile{Host: "h"}
	require.NoError(t, h.Save())

	sentinel := assert.AnError
	err = h.Update(func(h *Hosts) error {
		h.Profiles["b"] = &Profile{Host: "h"}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	final, err := LoadHostsFrom(dir)
	require.NoError(t, err)
	assert.NotContains(t, final.Profiles, "b", "a failed mutate must not be written")
}

func TestHosts_LoadFromExplicitDir(t *testing.T) {
	std, explicit := t.TempDir(), t.TempDir()
	t.Setenv("SEPAY_CONFIG_DIR", std)

	h := &Hosts{Profiles: map[string]*Profile{"p": {Host: "my.sepay.vn"}}, dir: explicit}
	require.NoError(t, h.Save())

	got, err := LoadHostsFrom(explicit)
	require.NoError(t, err)
	require.Contains(t, got.Profiles, "p")

	std0, err := LoadHosts()
	require.NoError(t, err)
	assert.Empty(t, std0.Profiles, "standard dir must be untouched")
}

func TestHosts_MissingFileIsEmpty(t *testing.T) {
	t.Setenv("SEPAY_CONFIG_DIR", t.TempDir())
	h, err := LoadHosts()
	require.NoError(t, err)
	assert.Empty(t, h.Active)
	assert.Empty(t, h.Profiles)
}

func TestHosts_SaveWrites0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are meaningless on Windows")
	}
	dir := t.TempDir()
	t.Setenv("SEPAY_CONFIG_DIR", dir)

	h, err := LoadHosts()
	require.NoError(t, err)
	h.Profiles["p"] = &Profile{Host: "my.sepay.vn"}
	require.NoError(t, h.Save())

	info, err := os.Stat(filepath.Join(dir, HostsFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestHosts_LoosePerms(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEPAY_CONFIG_DIR", dir)

	h, err := LoadHosts()
	require.NoError(t, err)
	h.Profiles["p"] = &Profile{Host: "my.sepay.vn"}
	require.NoError(t, h.Save())

	loose, err := LoosePerms(dir, HostsFile)
	require.NoError(t, err)
	assert.False(t, loose)

	if runtime.GOOS == "windows" {
		return // Unix permission bits are meaningless on Windows; LoosePerms is always false.
	}
	require.NoError(t, os.Chmod(filepath.Join(dir, HostsFile), 0o644))
	loose, err = LoosePerms(dir, HostsFile)
	require.NoError(t, err)
	assert.True(t, loose)
}
