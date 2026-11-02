package config_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sepayvn/sepay-cli/internal/config"
)

func TestTryLockFile_RefusesASecondHolderAndFreesOnRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "x.json")

	first, ok, err := config.TryLockFile(path)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	if _, ok, err = config.TryLockFile(path); err != nil || ok {
		t.Fatalf("a second holder must be refused: ok=%v err=%v", ok, err)
	}
	first.Release()

	again, ok, err := config.TryLockFile(path)
	if err != nil || !ok {
		t.Fatalf("release must free it: ok=%v err=%v", ok, err)
	}
	again.Release()
}

func TestTryLockFileFor_GivesUpWithoutAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	held, ok, err := config.TryLockFile(path)
	if err != nil || !ok {
		t.Fatalf("setup: ok=%v err=%v", ok, err)
	}
	defer held.Release()

	start := time.Now()
	lock, ok, err := config.TryLockFileFor(path, 60*time.Millisecond)
	if err != nil {
		t.Fatalf("a contended lock is not an error: %v", err)
	}
	if ok {
		lock.Release()
		t.Fatal("acquired a lock another holder has")
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("gave up after %v, before the wait it was given", elapsed)
	}
}

func TestTryLockFileFor_SucceedsOnceTheHolderReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.json")
	held, ok, _ := config.TryLockFile(path)
	if !ok {
		t.Fatal("setup")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		held.Release()
	}()

	lock, ok, err := config.TryLockFileFor(path, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("waited but never got it: ok=%v err=%v", ok, err)
	}
	lock.Release()
}

func TestTrySharedLockFile_ExclusiveOnlyOnceEveryHolderIsGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.own")

	first, ok, err := config.TrySharedLockFile(path)
	if err != nil || !ok {
		t.Fatalf("first shared acquire: ok=%v err=%v", ok, err)
	}
	second, ok, err := config.TrySharedLockFile(path)
	if err != nil || !ok {
		t.Fatalf("shared holders must coexist: ok=%v err=%v", ok, err)
	}
	if _, ok, err = config.TryLockFile(path); err != nil || ok {
		t.Fatalf("exclusive must fail while anyone holds it: ok=%v err=%v", ok, err)
	}

	first.Release()
	if _, ok, err = config.TryLockFile(path); err != nil || ok {
		t.Fatalf("one holder left is still a holder: ok=%v err=%v", ok, err)
	}

	second.Release()
	exclusive, ok, err := config.TryLockFile(path)
	if err != nil || !ok {
		t.Fatalf("exclusive after the last release: ok=%v err=%v", ok, err)
	}
	if _, ok, err = config.TrySharedLockFileFor(path, 50*time.Millisecond); err != nil || ok {
		t.Fatalf("shared must wait out an exclusive holder: ok=%v err=%v", ok, err)
	}
	exclusive.Release()
}
