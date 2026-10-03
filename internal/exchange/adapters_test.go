package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// These tests exercise the adapters against local httptest servers only.
// They verify request construction and response parsing against the docs-based
// assumptions, not against any real exchange.

func TestLunoAdapter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "k" || p != "s" {
			http.Error(w, "auth", 401)
			return
		}
		switch r.URL.Path {
		case "/api/1/ticker":
			if r.URL.Query().Get("pair") != "XBTMYR" {
				http.Error(w, "pair", 400)
				return
			}
			fmt.Fprint(w, `{"bid":"99","ask":"101","last_trade":"100"}`)
		case "/api/1/balance":
			fmt.Fprint(w, `{"balance":[{"asset":"XBT","balance":"2","reserved":"0.5"}]}`)
		case "/api/1/postorder":
			_ = r.ParseForm()
			if r.Form.Get("type") != "ASK" || r.Form.Get("post_only") != "true" {
				http.Error(w, "form", 400)
				return
			}
			fmt.Fprint(w, `{"order_id":"abc"}`)
		case "/api/1/orders/abc":
			fmt.Fprint(w, `{"state":"COMPLETE","type":"ASK","limit_price":"100","limit_volume":"2","base":"1","counter":"100"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	l := NewLuno("k", "s")
	l.BaseURL = srv.URL
	ctx := context.Background()
	tk, err := l.FetchTicker(ctx, "BTC/MYR")
	if err != nil || !tk.Last.Equal(decimal.NewFromInt(100)) {
		t.Fatal(tk, err)
	}
	bal, err := l.FetchBalance(ctx)
	if err != nil || !bal["BTC"].Free.Equal(decimal.RequireFromString("1.5")) {
		t.Fatal(bal, err)
	}
	o, err := l.CreateLimitOrder(ctx, "BTC/MYR", Sell, decimal.NewFromInt(2), decimal.NewFromInt(100), true)
	if err != nil || o.ID != "abc" {
		t.Fatal(o, err)
	}
	got, err := l.FetchOrder(ctx, "abc", "BTC/MYR")
	if err != nil || got.Status != StatusCanceled || !got.Filled.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("COMPLETE below limit volume is a stopped order: %+v %v", got, err)
	}
	if _, err := l.CreateMarketOrder(ctx, "BTC/MYR", Buy, decimal.NewFromInt(1)); err != ErrNotSupported {
		t.Fatal("master adapter has no market orders")
	}
}

func TestBinanceSigning(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.Header.Get("X-MBX-APIKEY") != "k" {
			http.Error(w, "key", 401)
			return
		}
		fmt.Fprint(w, `{"orderId":42}`)
	}))
	defer srv.Close()
	b := NewBinance("k", "secret")
	b.BaseURL = srv.URL
	b.Now = func() time.Time { return time.UnixMilli(1700000000000) }
	o, err := b.CreateMarketOrder(context.Background(), "BTC/USDT", Sell, decimal.RequireFromString("0.5"))
	if err != nil || o.ID != "42" {
		t.Fatal(o, err)
	}
	payload, sig, _ := strings.Cut(gotQuery, "&signature=")
	m := hmac.New(sha256.New, []byte("secret"))
	m.Write([]byte(payload))
	if sig != hex.EncodeToString(m.Sum(nil)) {
		t.Fatal("signature mismatch")
	}
	v, _ := url.ParseQuery(payload)
	if v.Get("symbol") != "BTCUSDT" || v.Get("side") != "SELL" || v.Get("type") != "MARKET" || v.Get("timestamp") != "1700000000000" {
		t.Fatalf("params %v", v)
	}
}

func TestBinanceFetchOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"FILLED","side":"SELL","type":"MARKET","price":"0","origQty":"0.5","executedQty":"0.5","cummulativeQuoteQty":"10000"}`)
	}))
	defer srv.Close()
	b := NewBinance("k", "s")
	b.BaseURL = srv.URL
	o, err := b.FetchOrder(context.Background(), "1", "BTC/USDT")
	if err != nil || o.Status != StatusClosed || !o.Average.Equal(decimal.NewFromInt(20000)) {
		t.Fatal(o, err)
	}
}

func TestKrakenSigningAndParsing(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte("not-a-real-secret"))
	var gotSig, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/0/public/Ticker":
			fmt.Fprint(w, `{"error":[],"result":{"XBTUSDT":{"a":["101","1","1"],"b":["99","1","1"],"c":["100","0.1"]}}}`)
		case "/0/private/AddOrder":
			gotSig, gotPath = r.Header.Get("API-Sign"), r.URL.Path
			b := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(b)
			gotBody = string(b)
			fmt.Fprint(w, `{"error":[],"result":{"txid":["OABC"]}}`)
		case "/0/private/Balance":
			fmt.Fprint(w, `{"error":[],"result":{"XXBT":"1.5","USDT":"10"}}`)
		default:
			fmt.Fprint(w, `{"error":["EGeneral:Unknown"]}`)
		}
	}))
	defer srv.Close()
	k := NewKraken("k", secret)
	k.BaseURL = srv.URL
	k.Now = func() time.Time { return time.UnixMilli(1700000000000) }
	ctx := context.Background()
	tk, err := k.FetchTicker(ctx, "BTC/USDT")
	if err != nil || !tk.Bid.Equal(decimal.NewFromInt(99)) || !tk.Ask.Equal(decimal.NewFromInt(101)) {
		t.Fatal(tk, err)
	}
	o, err := k.CreateMarketOrder(ctx, "BTC/USDT", Buy, decimal.NewFromInt(1))
	if err != nil || o.ID != "OABC" {
		t.Fatal(o, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(secret)
	sum := sha256.Sum256([]byte("1700000000000" + gotBody))
	m := hmac.New(sha512.New, raw)
	m.Write([]byte(gotPath))
	m.Write(sum[:])
	if gotSig != base64.StdEncoding.EncodeToString(m.Sum(nil)) {
		t.Fatal("API-Sign mismatch")
	}
	bal, err := k.FetchBalance(ctx)
	if err != nil || !bal["BTC"].Free.Equal(decimal.RequireFromString("1.5")) {
		t.Fatal(bal, err)
	}
	if _, err := k.FetchOrder(ctx, "x", "BTC/USDT"); err == nil {
		t.Fatal("API errors must surface")
	}
}
