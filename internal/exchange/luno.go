package exchange

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/shopspring/decimal"
)

// Luno is a thin REST adapter for the master exchange (MYR quote).
// UNVERIFIED against the live API; see http.go. Market orders are not
// implemented because the master only rests limit orders.
type Luno struct {
	Key, Secret string
	BaseURL     string
	HTTP        *http.Client
}

// NewLuno builds the adapter from API credentials.
func NewLuno(key, secret string) *Luno {
	return &Luno{Key: key, Secret: secret, BaseURL: "https://api.luno.com", HTTP: newHTTPClient()}
}

func (l *Luno) Name() string { return "luno" }

func lunoAsset(a string) string {
	if a == "BTC" {
		return "XBT"
	}
	return a
}

func fromLunoAsset(a string) string {
	if a == "XBT" {
		return "BTC"
	}
	return a
}

func lunoPair(symbol string) (string, error) {
	b, q, err := SplitSymbol(symbol)
	if err != nil {
		return "", err
	}
	return lunoAsset(b) + lunoAsset(q), nil
}

func (l *Luno) call(ctx context.Context, method, path string, form url.Values, out any) error {
	var req *http.Request
	var err error
	if method == http.MethodGet {
		u := l.BaseURL + path
		if len(form) > 0 {
			u += "?" + form.Encode()
		}
		req, err = ctxReq(ctx, method, u, nil)
	} else {
		req, err = ctxReq(ctx, method, l.BaseURL+path, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return err
	}
	if l.Key != "" {
		req.SetBasicAuth(l.Key, l.Secret)
	}
	return doJSON(l.HTTP, req, out)
}

func (l *Luno) FetchTicker(ctx context.Context, symbol string) (Ticker, error) {
	pair, err := lunoPair(symbol)
	if err != nil {
		return Ticker{}, err
	}
	var r struct {
		Bid       string `json:"bid"`
		Ask       string `json:"ask"`
		LastTrade string `json:"last_trade"`
	}
	if err := l.call(ctx, http.MethodGet, "/api/1/ticker", url.Values{"pair": {pair}}, &r); err != nil {
		return Ticker{}, err
	}
	return Ticker{Bid: dec(r.Bid), Ask: dec(r.Ask), Last: dec(r.LastTrade)}, nil
}

func (l *Luno) FetchBalance(ctx context.Context) (Balance, error) {
	var r struct {
		Balance []struct {
			Asset    string `json:"asset"`
			Balance  string `json:"balance"`
			Reserved string `json:"reserved"`
		} `json:"balance"`
	}
	if err := l.call(ctx, http.MethodGet, "/api/1/balance", nil, &r); err != nil {
		return nil, err
	}
	out := Balance{}
	for _, b := range r.Balance {
		total, used := dec(b.Balance), dec(b.Reserved)
		out[fromLunoAsset(b.Asset)] = AssetBalance{Free: total.Sub(used), Used: used}
	}
	return out, nil
}

func (l *Luno) CreateLimitOrder(ctx context.Context, symbol, side string, amount, price decimal.Decimal, postOnly bool) (Order, error) {
	pair, err := lunoPair(symbol)
	if err != nil {
		return Order{}, err
	}
	typ := "BID"
	if side == Sell {
		typ = "ASK"
	}
	form := url.Values{"pair": {pair}, "type": {typ}, "volume": {amount.String()}, "price": {price.String()}}
	if postOnly {
		form.Set("post_only", "true")
	}
	var r struct {
		OrderID string `json:"order_id"`
	}
	if err := l.call(ctx, http.MethodPost, "/api/1/postorder", form, &r); err != nil {
		return Order{}, err
	}
	return Order{ID: r.OrderID, Symbol: symbol, Side: side, Type: "limit", Price: price, Amount: amount, Status: StatusOpen}, nil
}

func (l *Luno) CreateMarketOrder(context.Context, string, string, decimal.Decimal) (Order, error) {
	return Order{}, ErrNotSupported
}

func (l *Luno) CancelOrder(ctx context.Context, id, symbol string) error {
	return l.call(ctx, http.MethodPost, "/api/1/stoporder", url.Values{"order_id": {id}}, nil)
}

func (l *Luno) FetchOrder(ctx context.Context, id, symbol string) (Order, error) {
	var r struct {
		State       string `json:"state"`
		Type        string `json:"type"`
		LimitPrice  string `json:"limit_price"`
		LimitVolume string `json:"limit_volume"`
		Base        string `json:"base"`
		Counter     string `json:"counter"`
	}
	if err := l.call(ctx, http.MethodGet, "/api/1/orders/"+url.PathEscape(id), nil, &r); err != nil {
		return Order{}, fmt.Errorf("luno fetch order: %w", err)
	}
	o := Order{ID: id, Symbol: symbol, Type: "limit", Price: dec(r.LimitPrice), Amount: dec(r.LimitVolume), Filled: dec(r.Base)}
	o.Side = Buy
	if r.Type == "ASK" {
		o.Side = Sell
	}
	if o.Filled.IsPositive() {
		o.Average = dec(r.Counter).Div(o.Filled)
	}
	switch {
	case r.State == "PENDING":
		o.Status = StatusOpen
	case o.Filled.GreaterThanOrEqual(o.Amount):
		o.Status = StatusClosed
	default:
		o.Status = StatusCanceled // COMPLETE with less than the limit volume: stopped early
	}
	return o, nil
}
