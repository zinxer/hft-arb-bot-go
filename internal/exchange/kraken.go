package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Kraken is a thin spot REST adapter (USDT quote). Private calls use the
// documented API-Sign scheme: base64(HMAC-SHA512(base64dec(secret), path + SHA256(nonce + body))).
// UNVERIFIED against the live API; see http.go. Kraken reports only total
// balances, so Free is set to the total and Used to zero (as the Node bot did).
type Kraken struct {
	Key, Secret string
	BaseURL     string
	HTTP        *http.Client
	Now         func() time.Time
}

// NewKraken builds the adapter from API credentials.
func NewKraken(key, secret string) *Kraken {
	return &Kraken{Key: key, Secret: secret, BaseURL: "https://api.kraken.com", HTTP: newHTTPClient(), Now: time.Now}
}

func (k *Kraken) Name() string { return "Kraken" }

func krakenAsset(a string) string {
	if a == "BTC" {
		return "XBT"
	}
	return a
}

// fromKrakenAsset normalises legacy balance codes such as XXBT/XETH/ZUSD.
func fromKrakenAsset(a string) string {
	switch a {
	case "XXBT", "XBT":
		return "BTC"
	case "XETH":
		return "ETH"
	case "XXRP":
		return "XRP"
	case "XLTC":
		return "LTC"
	}
	return a
}

func krakenPair(symbol string) (string, error) {
	b, q, err := SplitSymbol(symbol)
	return krakenAsset(b) + krakenAsset(q), err
}

// signature computes the API-Sign header value.
func (k *Kraken) signature(path, nonce, body string) (string, error) {
	secret, err := base64.StdEncoding.DecodeString(k.Secret)
	if err != nil {
		return "", errors.New("kraken secret is not valid base64")
	}
	sum := sha256.Sum256([]byte(nonce + body))
	m := hmac.New(sha512.New, secret)
	m.Write([]byte(path))
	m.Write(sum[:])
	return base64.StdEncoding.EncodeToString(m.Sum(nil)), nil
}

type krakenResp struct {
	Error  []string `json:"error"`
	Result map[string]any
}

func (k *Kraken) public(ctx context.Context, path string, q url.Values) (map[string]any, error) {
	req, err := ctxReq(ctx, http.MethodGet, k.BaseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	return k.finish(req)
}

func (k *Kraken) private(ctx context.Context, path string, v url.Values) (map[string]any, error) {
	nonce := strconv.FormatInt(k.Now().UnixMilli(), 10)
	v.Set("nonce", nonce)
	body := v.Encode()
	sig, err := k.signature(path, nonce, body)
	if err != nil {
		return nil, err
	}
	req, err := ctxReq(ctx, http.MethodPost, k.BaseURL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("API-Key", k.Key)
	req.Header.Set("API-Sign", sig)
	return k.finish(req)
}

func (k *Kraken) finish(req *http.Request) (map[string]any, error) {
	var r krakenResp
	if err := doJSON(k.HTTP, req, &r); err != nil {
		return nil, err
	}
	if len(r.Error) > 0 {
		return nil, errors.New("kraken: " + strings.Join(r.Error, "; "))
	}
	return r.Result, nil
}

// first returns element 0 of a JSON array of strings, e.g. ticker "a":["price","wholeLots","lots"].
func first(v any) string {
	if arr, ok := v.([]any); ok && len(arr) > 0 {
		if s, ok := arr[0].(string); ok {
			return s
		}
	}
	return ""
}

func (k *Kraken) FetchTicker(ctx context.Context, symbol string) (Ticker, error) {
	pair, err := krakenPair(symbol)
	if err != nil {
		return Ticker{}, err
	}
	res, err := k.public(ctx, "/0/public/Ticker", url.Values{"pair": {pair}})
	if err != nil {
		return Ticker{}, err
	}
	for _, v := range res { // result is keyed by Kraken's canonical pair name
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		return Ticker{Bid: dec(first(m["b"])), Ask: dec(first(m["a"])), Last: dec(first(m["c"]))}, nil
	}
	return Ticker{}, errors.New("kraken: empty ticker result")
}

func (k *Kraken) FetchBalance(ctx context.Context) (Balance, error) {
	res, err := k.private(ctx, "/0/private/Balance", url.Values{})
	if err != nil {
		return nil, err
	}
	out := Balance{}
	for code, v := range res {
		s, _ := v.(string)
		out[fromKrakenAsset(code)] = AssetBalance{Free: dec(s)}
	}
	return out, nil
}

func (k *Kraken) CreateLimitOrder(ctx context.Context, symbol, side string, amount, price decimal.Decimal, postOnly bool) (Order, error) {
	pair, err := krakenPair(symbol)
	if err != nil {
		return Order{}, err
	}
	v := url.Values{"pair": {pair}, "type": {side}, "ordertype": {"limit"}, "volume": {amount.String()}, "price": {price.String()}}
	if postOnly {
		v.Set("oflags", "post")
	}
	id, err := k.addOrder(ctx, v)
	if err != nil {
		return Order{}, err
	}
	return Order{ID: id, Symbol: symbol, Side: side, Type: "limit", Price: price, Amount: amount, Status: StatusOpen}, nil
}

func (k *Kraken) CreateMarketOrder(ctx context.Context, symbol, side string, amount decimal.Decimal) (Order, error) {
	pair, err := krakenPair(symbol)
	if err != nil {
		return Order{}, err
	}
	id, err := k.addOrder(ctx, url.Values{"pair": {pair}, "type": {side}, "ordertype": {"market"}, "volume": {amount.String()}})
	if err != nil {
		return Order{}, err
	}
	return Order{ID: id, Symbol: symbol, Side: side, Type: "market", Amount: amount}, nil
}

func (k *Kraken) addOrder(ctx context.Context, v url.Values) (string, error) {
	res, err := k.private(ctx, "/0/private/AddOrder", v)
	if err != nil {
		return "", err
	}
	ids, _ := res["txid"].([]any)
	if len(ids) == 0 {
		return "", errors.New("kraken: no txid in AddOrder response")
	}
	return fmt.Sprint(ids[0]), nil
}

func (k *Kraken) CancelOrder(ctx context.Context, id, symbol string) error {
	_, err := k.private(ctx, "/0/private/CancelOrder", url.Values{"txid": {id}})
	return err
}

func (k *Kraken) FetchOrder(ctx context.Context, id, symbol string) (Order, error) {
	res, err := k.private(ctx, "/0/private/QueryOrders", url.Values{"txid": {id}})
	if err != nil {
		return Order{}, err
	}
	m, ok := res[id].(map[string]any)
	if !ok {
		return Order{}, errors.New("kraken: order not found in QueryOrders response")
	}
	str := func(key string) string { s, _ := m[key].(string); return s }
	o := Order{ID: id, Symbol: symbol, Side: str("type"), Amount: dec(str("vol")), Filled: dec(str("vol_exec")), Average: dec(str("price"))}
	switch str("status") {
	case "pending", "open":
		o.Status = StatusOpen
	case "closed":
		o.Status = StatusClosed
	default: // canceled, expired
		o.Status = StatusCanceled
	}
	return o, nil
}
