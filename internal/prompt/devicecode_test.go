package prompt

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var waitText = DeviceWaitText{
	CopyLink: "copy link", CopyCode: "copy code", OpenBrowser: "open browser", Cancel: "cancel",
	Waiting: "waiting", LinkCopied: "link copied", CodeCopied: "code copied", Opened: "opened", NotOpened: "not opened",
}

func osc52(s string) string { return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(s)) }

// waitWith runs WaitDevice with keys typed after the first frame; poll blocks until ctx ends or
// release closes, then returns pollErr.
func waitWith(t *testing.T, w DeviceWait, keys string, release <-chan struct{}, pollErr error) (out string, cancelled bool, err error) {
	t.Helper()
	var buf bytes.Buffer
	in, typed := io.Pipe()
	t.Cleanup(func() { _ = typed.Close() })
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = typed.Write([]byte(keys))
	}()
	ended := make(chan bool, 1)
	err = WaitDevice(context.Background(), Terminal{In: in, Out: &buf}, w, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			ended <- true
			return ctx.Err()
		case <-release:
			ended <- false
			return pollErr
		}
	})
	select {
	case cancelled = <-ended:
		return buf.String(), cancelled, err
	case <-time.After(2 * time.Second):
		t.Fatal("poll never ended")
		return "", false, nil
	}
}

func releaseAfter(d time.Duration) <-chan struct{} {
	ch := make(chan struct{})
	time.AfterFunc(d, func() { close(ch) })
	return ch
}

func TestWaitDevice_CopiesTheLinkAndTheCodeOnKeys(t *testing.T) {
	w := DeviceWait{URL: "https://my.sepay.vn/device", Code: "WDJB-MJHT", Text: waitText}
	out, cancelled, err := waitWith(t, w, "cy", releaseAfter(400*time.Millisecond), nil)

	require.NoError(t, err)
	assert.False(t, cancelled)
	assert.Contains(t, out, osc52("https://my.sepay.vn/device"))
	assert.Contains(t, out, osc52("WDJB-MJHT"))
}

func TestWaitDevice_InTmuxAlsoWrapsTheSequence(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	w := DeviceWait{URL: "https://my.sepay.vn/device", Code: "WDJB-MJHT", Text: waitText}
	out, _, err := waitWith(t, w, "y", releaseAfter(400*time.Millisecond), nil)

	require.NoError(t, err)
	assert.Contains(t, out, osc52("WDJB-MJHT"))
	assert.Contains(t, out, "\x1bPtmux;\x1b"+osc52("WDJB-MJHT"))
}

func TestWaitDevice_NoLinkKeyWithoutATrustedURL(t *testing.T) {
	w := DeviceWait{Code: "WDJB-MJHT", Text: waitText}
	out, _, err := waitWith(t, w, "c", releaseAfter(400*time.Millisecond), nil)

	require.NoError(t, err)
	assert.NotContains(t, out, "\x1b]52;", "nothing is copied for an untrusted link")
	assert.NotContains(t, out, "copy link")
	assert.NotContains(t, out, "open browser", "no browser key without Open")
}

func TestWaitDevice_EnterOpensTheBrowser(t *testing.T) {
	opened := 0
	w := DeviceWait{URL: "https://my.sepay.vn/device", Code: "X", Text: waitText, Open: func() bool { opened++; return true }}
	_, _, err := waitWith(t, w, "\r", releaseAfter(400*time.Millisecond), nil)

	require.NoError(t, err)
	assert.Equal(t, 1, opened)
}

func TestWaitDevice_ReturnsThePollError(t *testing.T) {
	boom := errors.New("expired")
	_, _, err := waitWith(t, DeviceWait{Code: "X", Text: waitText}, "", releaseAfter(150*time.Millisecond), boom)
	assert.ErrorIs(t, err, boom)
}

func TestWaitDevice_CtrlCCancelsThePoll(t *testing.T) {
	_, cancelled, err := waitWith(t, DeviceWait{Code: "X", Text: waitText}, "\x03", make(chan struct{}), nil)
	require.ErrorIs(t, err, ErrInterrupted)
	assert.True(t, cancelled, "the poll's context ends with the screen")
}

func TestWaitDevice_ClearsTheScreenWhenDone(t *testing.T) {
	m := &waitModel{screen: DeviceWait{Code: "X", Text: waitText}}
	assert.Contains(t, view(m), "waiting")
	next, _ := m.Update(pollDone{})
	assert.Empty(t, view(next))
}
