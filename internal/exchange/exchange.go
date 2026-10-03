// Package exchange defines the Exchange interface the strategy depends on,
// an in-memory MockExchange for tests, and thin REST adapters.
package exchange

import (
	"context"
	"errors"
	"strings"

	"github.com/shopspring/decimal"
)

// Order sides and statuses (normalised across exchanges).
const (
	Buy  = "buy"
	Sell = "sell"

	StatusOpen     = "open"
	StatusClosed   = "closed" // fully filled
	StatusCanceled = "canceled"
)

// ErrNotSupported is returned by adapters for operations they do not implement.
var ErrNotSupported = errors.New("operation not supported by this adapter")

// Ticker is a top-of-book snapshot.
type Ticker struct {
	Bid, Ask, Last decimal.Decimal
}

// AssetBalance is the free/used balance of one asset.
type AssetBalance struct {
	Free, Used decimal.Decimal
}

// Balance maps upper-case asset code to its balance.
type Balance map[string]AssetBalance

// Order is a normalised order record.
type Order struct {
	ID      string
	Symbol  string // "BASE/QUOTE"
	Side    string
	Type    string // "limit" or "market"
	Price   decimal.Decimal
	Amount  decimal.Decimal
	Filled  decimal.Decimal
	Average decimal.Decimal // average fill price (zero if unfilled)
	Status  string
}

// Exchange is the minimal surface the strategy needs. Symbols use "BASE/QUOTE".
type Exchange interface {
	Name() string
	FetchTicker(ctx context.Context, symbol string) (Ticker, error)
	FetchBalance(ctx context.Context) (Balance, error)
	CreateLimitOrder(ctx context.Context, symbol, side string, amount, price decimal.Decimal, postOnly bool) (Order, error)
	CreateMarketOrder(ctx context.Context, symbol, side string, amount decimal.Decimal) (Order, error)
	CancelOrder(ctx context.Context, id, symbol string) error
	FetchOrder(ctx context.Context, id, symbol string) (Order, error)
}

// SplitSymbol splits "BASE/QUOTE".
func SplitSymbol(symbol string) (base, quote string, err error) {
	b, q, ok := strings.Cut(symbol, "/")
	if !ok || b == "" || q == "" {
		return "", "", errors.New("invalid symbol " + symbol)
	}
	return b, q, nil
}
