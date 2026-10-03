package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// UNVERIFIED: the REST adapters in this package (luno.go, binance.go, kraken.go)
// were written from public API documentation and have NEVER been run against a
// live exchange. Endpoints, parameter names, symbol mappings and response
// parsing may be wrong. Test with tiny sizes (or exchange test environments)
// before trusting any of it. Each adapter exposes BaseURL and HTTP so tests can
// point it at an httptest server.

const defaultTimeout = 15 * time.Second

func newHTTPClient() *http.Client { return &http.Client{Timeout: defaultTimeout} }

// doJSON executes the request and decodes a JSON body into out.
func doJSON(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: http %d: %s", req.Method, req.URL.Path, resp.StatusCode, truncate(string(body), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func ctxReq(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, method, url, body)
}
