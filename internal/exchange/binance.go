package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Binance is a thin spot REST adapter (USDT quote), HMAC-SHA256 signed.
// UNVERIFIED against the live API; see http.go.
type Binance struct {
	Key, Secret string
	BaseURL     string
	HTTP        *http.Client
	Now         func() time.Time
}

// NewBinance builds the adapter from API credentials.
func NewBinance(key, secret string) *Binance {
	return &Binance{Key: key, Secret: secret, BaseURL: "https://api.binance.com", HTTP: newHTTPClient(), Now: time.Now}
}

func (b *Binance) Name() string { return "Binance" }

func binSymbol(symbol string) (string, error) {
	base, quote, err := SplitSymbol(symbol)
	return base + quote, err
}

// sign appends timestamp and the hex HMAC-SHA256 signature of the query string.
func (b *Binance) sign(v url.Values) string {
	v.Set("timestamp", strconv.FormatInt(b.Now().UnixMilli(), 10))
	q := v.Encode()
	m := hmac.New(sha256.New, []byte(b.Secret))
	m.Write([]byte(q))
	return q + "&signature=" + hex.EncodeToString(m.Sum(nil))
}

func (b *Binance) call(ctx context.Context, method, path string, v url.Values, signed bool, out any) error {
	q := v.Encode()
	if signed {
		q = b.sign(v)
	}
	req, err := ctxReq(ctx, method, b.BaseURL+path+"?"+q, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MBX-APIKEY", b.Key)
	return doJSON(b.HTTP, req, out)
}

func (b *Binance) FetchTicker(ctx context.Context, symbol string) (Ticker, error) {
	s, err := binSymbol(symbol)
	if err != nil {
		return Ticker{}, err
	}
	var book struct {
		Bid string `json:"bidPrice"`
		Ask string `json:"askPrice"`
	}
	if err := b.call(ctx, http.MethodGet, "/api/v3/ticker/bookTicker", url.Values{"symbol": {s}}, false, &book); err != nil {
		return Ticker{}, err
	}
	var last struct {
		Price string `json:"price"`
	}
	if err := b.call(ctx, http.MethodGet, "/api/v3/ticker/price", url.Values{"symbol": {s}}, false, &last); err != nil {
		return Ticker{}, err
	}
	return Ticker{Bid: dec(book.Bid), Ask: dec(book.Ask), Last: dec(last.Price)}, nil
}

func (b *Binance) FetchBalance(ctx context.Context) (Balance, error) {
	var r struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := b.call(ctx, http.MethodGet, "/api/v3/account", url.Values{}, true, &r); err != nil {
		return nil, err
	}
	out := Balance{}
	for _, x := range r.Balances {
		out[x.Asset] = AssetBalance{Free: dec(x.Free), Used: dec(x.Locked)}
	}
	return out, nil
}

func (b *Binance) CreateLimitOrder(ctx context.Context, symbol, side string, amount, price decimal.Decimal, postOnly bool) (Order, error) {
	s, err := binSymbol(symbol)
	if err != nil {
		return Order{}, err
	}
	typ := "LIMIT"
	v := url.Values{"symbol": {s}, "side": {strings.ToUpper(side)}, "quantity": {amount.String()}, "price": {price.String()}}
	if postOnly {
		typ = "LIMIT_MAKER"
	} else {
		v.Set("timeInForce", "GTC")
	}
	v.Set("type", typ)
	var r struct {
		OrderID int64 `json:"orderId"`
	}
	if err := b.call(ctx, http.MethodPost, "/api/v3/order", v, true, &r); err != nil {
		return Order{}, err
	}
	return Order{ID: strconv.FormatInt(r.OrderID, 10), Symbol: symbol, Side: side, Type: "limit", Price: price, Amount: amount, Status: StatusOpen}, nil
}

func (b *Binance) CreateMarketOrder(ctx context.Context, symbol, side string, amount decimal.Decimal) (Order, error) {
	s, err := binSymbol(symbol)
	if err != nil {
		return Order{}, err
	}
	v := url.Values{"symbol": {s}, "side": {strings.ToUpper(side)}, "type": {"MARKET"}, "quantity": {amount.String()}}
	var r struct {
		OrderID int64 `json:"orderId"`
	}
	if err := b.call(ctx, http.MethodPost, "/api/v3/order", v, true, &r); err != nil {
		return Order{}, err
	}
	return Order{ID: strconv.FormatInt(r.OrderID, 10), Symbol: symbol, Side: side, Type: "market", Amount: amount}, nil
}

func (b *Binance) CancelOrder(ctx context.Context, id, symbol string) error {
	s, err := binSymbol(symbol)
	if err != nil {
		return err
	}
	return b.call(ctx, http.MethodDelete, "/api/v3/order", url.Values{"symbol": {s}, "orderId": {id}}, true, nil)
}

func (b *Binance) FetchOrder(ctx context.Context, id, symbol string) (Order, error) {
	s, err := binSymbol(symbol)
	if err != nil {
		return Order{}, err
	}
	var r struct {
		Status   string `json:"status"`
		Side     string `json:"side"`
		Type     string `json:"type"`
		Price    string `json:"price"`
		OrigQty  string `json:"origQty"`
		ExecQty  string `json:"executedQty"`
		QuoteQty string `json:"cummulativeQuoteQty"`
	}
	if err := b.call(ctx, http.MethodGet, "/api/v3/order", url.Values{"symbol": {s}, "orderId": {id}}, true, &r); err != nil {
		return Order{}, err
	}
	o := Order{ID: id, Symbol: symbol, Side: strings.ToLower(r.Side), Type: strings.ToLower(r.Type),
		Price: dec(r.Price), Amount: dec(r.OrigQty), Filled: dec(r.ExecQty)}
	if o.Filled.IsPositive() {
		o.Average = dec(r.QuoteQty).Div(o.Filled)
	}
	switch r.Status {
	case "NEW", "PARTIALLY_FILLED":
		o.Status = StatusOpen
	case "FILLED":
		o.Status = StatusClosed
	default: // CANCELED, EXPIRED, REJECTED, ...
		o.Status = StatusCanceled
	}
	return o, nil
}
