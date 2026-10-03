package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/zinxer/hft-arb-bot-go/internal/exchange"
)

// RateCacheTTL is how long a USDT/MYR rate is cached.
const RateCacheTTL = 60 * time.Minute

// defaultPriceAPI is a public price endpoint used when no market ticker is available.
const defaultPriceAPI = "https://api.coingecko.com/api/v3/simple/price?ids=tether&vs_currencies=myr"

// RateProvider resolves the USDT/MYR rate: fixed value, else the master's
// USDT/MYR market ticker (preferred), else a public price API. Cached for 60 minutes.
type RateProvider struct {
	Fixed    decimal.Decimal // if positive, always returned
	Master   exchange.Exchange
	Market   string // e.g. "USDT/MYR"
	APIFetch func(ctx context.Context) (decimal.Decimal, error)
	Now      func() time.Time

	mu    sync.Mutex
	rate  decimal.Decimal
	stamp time.Time
}

// NewRateProvider builds a provider using the real public price API fallback.
func NewRateProvider(fixed decimal.Decimal, master exchange.Exchange, market string) *RateProvider {
	return &RateProvider{Fixed: fixed, Master: master, Market: market, APIFetch: fetchPriceAPI, Now: time.Now}
}

// Rate returns the current USDT/MYR rate.
func (p *RateProvider) Rate(ctx context.Context) (decimal.Decimal, error) {
	if p.Fixed.IsPositive() {
		return p.Fixed, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	if p.rate.IsPositive() && now().Sub(p.stamp) < RateCacheTTL {
		return p.rate, nil
	}
	var rate decimal.Decimal
	if p.Master != nil && p.Market != "" {
		if t, err := p.Master.FetchTicker(ctx, p.Market); err == nil && t.Last.IsPositive() {
			rate = t.Last
		}
	}
	if !rate.IsPositive() && p.APIFetch != nil {
		r, err := p.APIFetch(ctx)
		if err != nil {
			return decimal.Zero, fmt.Errorf("usdt/myr rate unavailable: %w", err)
		}
		rate = r
	}
	if !rate.IsPositive() {
		return decimal.Zero, errors.New("usdt/myr rate unavailable")
	}
	p.rate, p.stamp = rate, now()
	return rate, nil
}

func fetchPriceAPI(ctx context.Context) (decimal.Decimal, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultPriceAPI, nil)
	if err != nil {
		return decimal.Zero, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return decimal.Zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decimal.Zero, fmt.Errorf("price api status %d", resp.StatusCode)
	}
	var body struct {
		Tether struct {
			MYR json.Number `json:"myr"`
		} `json:"tether"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromString(body.Tether.MYR.String())
}
