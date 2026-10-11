package ops

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// probeBody bounds how much of a health answer is read before closing.
const probeBody = 4 << 10

// HTTPProbe is a check that GETs rawURL and is up on any 2xx. header is
// sent with each request and never quoted in an error; a password in the
// URL is redacted from it.
func HTTPProbe(client *http.Client, rawURL string, header http.Header) func(context.Context) error {
	u, err := url.Parse(rawURL)
	return func(ctx context.Context) error {
		if err != nil {
			return fmt.Errorf("health url: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return fmt.Errorf("GET %s: %w", u.Redacted(), err)
		}
		for k, vs := range header {
			req.Header[k] = vs
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("GET %s: %w", u.Redacted(), err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, probeBody))
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return fmt.Errorf("GET %s: answered %d %s", u.Redacted(), resp.StatusCode, http.StatusText(resp.StatusCode))
		}
		return nil
	}
}
