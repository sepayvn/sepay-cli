package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	lockFile     = ".lock"
	lockPoll     = 50 * time.Millisecond
	lockTimeout  = 10 * time.Second
	staleTempAge = time.Hour
)

// LockDir takes an exclusive cross-process advisory lock on dir and returns its release func.
// It gives up after lockTimeout and is not re-entrant: never nest acquisitions.
func LockDir(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		ok, err := tryAcquireLock(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("timed out waiting for the config lock; another sepay process may be holding %s", filepath.Join(dir, lockFile))
		}
		time.Sleep(lockPoll)
	}
	return func() {
		_ = releaseLock(f)
		_ = f.Close()
	}, nil
}

// FileLock is an exclusive cross-process lock on one file.
type FileLock struct{ f *os.File }

// TryLockFile takes an exclusive advisory lock on path (created 0600) without waiting.
func TryLockFile(path string) (*FileLock, bool, error) {
	return takeFileLock(path, tryAcquireLock)
}

// TrySharedLockFile takes a shared advisory lock on path; TryLockFile succeeds only after every holder is gone.
func TrySharedLockFile(path string) (*FileLock, bool, error) {
	return takeFileLock(path, tryAcquireSharedLock)
}

func takeFileLock(path string, acquire func(*os.File) (bool, error)) (*FileLock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	ok, err := acquire(f)
	if err != nil || !ok {
		_ = f.Close()
		return nil, false, err
	}
	return &FileLock{f: f}, true, nil
}

// TryLockFileFor is TryLockFile with a bounded wait, for locks held only across a short rewrite.
func TryLockFileFor(path string, wait time.Duration) (*FileLock, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return TryLockFileContext(ctx, path)
}

// TrySharedLockFileFor is TrySharedLockFile with a bounded wait.
func TrySharedLockFileFor(path string, wait time.Duration) (*FileLock, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return waitForLock(ctx, func() (*FileLock, bool, error) { return TrySharedLockFile(path) })
}

// TryLockFileContext is TryLockFile, waiting until ctx is done; ok is false when it is, with no error.
func TryLockFileContext(ctx context.Context, path string) (*FileLock, bool, error) {
	return waitForLock(ctx, func() (*FileLock, bool, error) { return TryLockFile(path) })
}

func waitForLock(ctx context.Context, try func() (*FileLock, bool, error)) (*FileLock, bool, error) {
	for {
		lock, ok, err := try()
		if err != nil || ok {
			return lock, ok, err
		}
		select {
		case <-ctx.Done():
			return nil, false, nil
		case <-time.After(lockPoll / 10):
		}
	}
}

// File returns the locked file.
func (l *FileLock) File() *os.File { return l.f }

// Release drops the lock and closes the file.
func (l *FileLock) Release() {
	_ = releaseLock(l.f)
	_ = l.f.Close()
}

// WriteFileAtomic writes data to dir/name as 0600 via a synced temp file and rename.
// It does not lock; pair with LockDir for read-modify-write.
func WriteFileAtomic(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	sweepStaleTemps(dir, name)
	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// sweepStaleTemps removes crash-leftover temps older than staleTempAge, sparing in-flight ones.
func sweepStaleTemps(dir, name string) {
	matches, err := filepath.Glob(filepath.Join(dir, name+".*.tmp"))
	if err != nil {
		return
	}
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && time.Since(fi.ModTime()) > staleTempAge {
			_ = os.Remove(m)
		}
	}
}
