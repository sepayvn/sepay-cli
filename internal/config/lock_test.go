package config_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryLockFile_RefusesASecondHolderAndFreesOnRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.json")

	first, ok, err := config.TryLockFile(path)
	require.NoError(t, err, "first acquire")
	require.True(t, ok, "first acquire")
	_, ok, err = config.TryLockFile(path)
	require.NoError(t, err, "a second holder must be refused")
	require.False(t, ok, "a second holder must be refused")
	first.Release()

	again, ok, err := config.TryLockFile(path)
	require.NoError(t, err, "release must free it")
	require.True(t, ok, "release must free it")
	again.Release()
}

func TestTryLockFileFor_GivesUpWithoutAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	held, ok, err := config.TryLockFile(path)
	require.NoError(t, err, "setup")
	require.True(t, ok, "setup")
	defer held.Release()

	start := time.Now()
	lock, ok, err := config.TryLockFileFor(path, 60*time.Millisecond)
	require.NoError(t, err, "a contended lock is not an error")
	if ok {
		lock.Release()
	}
	require.False(t, ok, "acquired a lock another holder has")
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond, "gave up before the wait it was given")
}

func TestTryLockFileFor_SucceedsOnceTheHolderReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	held, ok, _ := config.TryLockFile(path)
	require.True(t, ok, "setup")
	go func() {
		time.Sleep(20 * time.Millisecond)
		held.Release()
	}()

	lock, ok, err := config.TryLockFileFor(path, 2*time.Second)
	require.NoError(t, err, "waited but never got it")
	require.True(t, ok, "waited but never got it")
	lock.Release()
}

func TestTrySharedLockFile_ExclusiveOnlyOnceEveryHolderIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.own")

	first, ok, err := config.TrySharedLockFile(path)
	require.NoError(t, err, "first shared acquire")
	require.True(t, ok, "first shared acquire")
	second, ok, err := config.TrySharedLockFile(path)
	require.NoError(t, err, "shared holders must coexist")
	require.True(t, ok, "shared holders must coexist")
	_, ok, err = config.TryLockFile(path)
	require.NoError(t, err, "exclusive must fail while anyone holds it")
	require.False(t, ok, "exclusive must fail while anyone holds it")

	first.Release()
	_, ok, err = config.TryLockFile(path)
	require.NoError(t, err, "one holder left is still a holder")
	require.False(t, ok, "one holder left is still a holder")

	second.Release()
	exclusive, ok, err := config.TryLockFile(path)
	require.NoError(t, err, "exclusive after the last release")
	require.True(t, ok, "exclusive after the last release")
	_, ok, err = config.TrySharedLockFileFor(path, 50*time.Millisecond)
	require.NoError(t, err, "shared must wait out an exclusive holder")
	require.False(t, ok, "shared must wait out an exclusive holder")
	exclusive.Release()
}

func TestTryLockFileContext_StopsWhenTheContextEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	held, ok, err := config.TryLockFile(path)
	require.NoError(t, err, "first acquire")
	require.True(t, ok, "first acquire")
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	lock, ok, err := config.TryLockFileContext(ctx, path)
	require.NoError(t, err, "a cancelled wait reports no error")
	require.False(t, ok, "a cancelled wait takes no lock")
	require.Nil(t, lock, "a cancelled wait takes no lock")
	assert.LessOrEqual(t, time.Since(start), time.Second, "the wait outlived its context")
}
