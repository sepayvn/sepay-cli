package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sepayvn/sepay-cli/internal/api"
)

// The OAuth and token-management calls name a failed connection the way the API client does.
func TestClient_TransportFailuresAreNamed(t *testing.T) {
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closed.Close()
	cutOff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"ac`))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	defer cutOff.Close()

	calls := map[string]func(c *Client) error{
		"refresh": func(c *Client) error { _, err := c.Refresh(context.Background(), "rt", nil); return err },
		"list tokens": func(c *Client) error {
			_, err := c.ListTokens(context.Background(), "at")
			return err
		},
	}
	servers := []struct {
		name, url string
		reason    func(host string) string
	}{
		{"refused", closed.URL, func(h string) string { return h + " refused the connection" }},
		{"cut off", cutOff.URL, func(h string) string { return "the connection to " + h + " was cut off" }},
	}
	for callName, call := range calls {
		for _, srv := range servers {
			t.Run(callName+"/"+srv.name, func(t *testing.T) {
				err := call(NewClient(srv.url, "sepay-cli", nil))
				var te *api.TransportError
				require.ErrorAs(t, err, &te)
				want := srv.reason(strings.TrimPrefix(srv.url, "http://"))
				assert.Equal(t, want+"; check your internet connection and retry", err.Error())
			})
		}
	}
}
