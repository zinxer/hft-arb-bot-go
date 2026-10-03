package strategy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/zinxer/hft-arb-bot-go/internal/config"
	"github.com/zinxer/hft-arb-bot-go/internal/exchange"
)

type fixture struct {
	master, slave *exchange.MockExchange
	bot           *Bot
	mu            sync.Mutex
	trades        []Trade
}

func newFixture(t *testing.T, masterLast, slaveBid string) *fixture {
	t.Helper()
	f := &fixture{master: exchange.NewMock("master"), slave: exchange.NewMock("slave")}
	f.master.SetTicker("BTC/MYR", masterLast, masterLast, masterLast)
	f.slave.SetTicker("BTC/USDT", slaveBid, slaveBid, slaveBid)
	f.master.SetBalance("BTC", "1")
	f.master.SetBalance("MYR", "100000")
	f.slave.SetBalance("BTC", "1")
	f.slave.SetBalance("USDT", "100000")
	cfg := config.Config{
		Assets: []string{"BTC"}, OrderSizeMYR: decimal.NewFromInt(100), SafeGapPct: decimal.RequireFromString("0.8"),
		PriceOffset: decimal.RequireFromString("0.0001"), MasterBase: "MYR", SlaveBase: "USDT",
		CycleTime: time.Hour, FixedUSDTMYR: decimal.NewFromInt(4),
	}
	rate := NewRateProvider(cfg.FixedUSDTMYR, nil, "")
	f.bot = NewBot(cfg, f.master, []exchange.Exchange{f.slave}, rate, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.bot.BalanceTTL = 0
	f.bot.PollInterval = time.Millisecond
	f.bot.OnTrade = func(tr Trade) { f.mu.Lock(); f.trades = append(f.trades, tr); f.mu.Unlock() }
	return f
}

func (f *fixture) tradeCount() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.trades) }

func countPrefix(log []string, p string) int {
	n := 0
	for _, l := range log {
		if strings.HasPrefix(l, p) {
			n++
		}
	}
	return n
}

// Master 100000 MYR vs slave bid 20000 USDT * 4 = 80000 MYR: master is above, so sell on master.
func TestFillThenHedge(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "100000", "20000")
	placed, err := f.bot.Cycle(ctx, "BTC")
	if err != nil || !placed {
		t.Fatalf("placed=%v err=%v", placed, err)
	}
	o := f.master.Order("master-1")
	if o.Side != exchange.Sell || !o.Price.Equal(decimal.RequireFromString("99990")) || !o.Amount.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("unexpected limit order %+v", o)
	}
	f.master.FillOrder("master-1", "0.001")
	if _, err := f.bot.Cycle(ctx, "BTC"); err != nil {
		t.Fatal(err)
	}
	if got := countPrefix(f.slave.CallLog(), "market buy BTC/USDT"); got != 1 {
		t.Fatalf("want 1 slave market buy, log=%v", f.slave.CallLog())
	}
	if f.tradeCount() != 1 {
		t.Fatal("trade not recorded")
	}
	// sold at 99990 MYR, bought at 20000*4=80000 MYR: 0.001 * 19990 = 19.99
	if p := f.trades[0].ProfitMYR; !p.Equal(decimal.RequireFromString("19.99")) {
		t.Fatalf("profit %s", p)
	}
	// the same cycle rests a fresh order
	if o2 := f.master.Order("master-2"); o2.Status != exchange.StatusOpen {
		t.Fatal("expected a new open order")
	}
}

func TestNoFillCancels(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "100000", "20000")
	f.bot.Cycle(ctx, "BTC")
	f.bot.Cycle(ctx, "BTC")
	if f.master.Order("master-1").Status != exchange.StatusCanceled {
		t.Fatal("first order should be cancelled")
	}
	if len(f.slave.CallLog()) != 0 || f.tradeCount() != 0 {
		t.Fatal("no hedge expected without a fill")
	}
}

func TestPartialFillIsHedged(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "100000", "20000")
	f.bot.Cycle(ctx, "BTC")
	f.master.FillOrder("master-1", "0.0005")
	f.bot.Cycle(ctx, "BTC")
	if f.master.Order("master-1").Status != exchange.StatusCanceled {
		t.Fatal("remainder must be cancelled")
	}
	if f.tradeCount() != 1 || !f.trades[0].Amount.Equal(decimal.RequireFromString("0.0005")) {
		t.Fatalf("partial fill not hedged: %+v", f.trades)
	}
}

// Master below slave: buy on master, hedge with a slave sell.
func TestBuySideHedge(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "60000", "20000") // slave 80000 MYR
	if placed, err := f.bot.Cycle(ctx, "BTC"); !placed || err != nil {
		t.Fatal(placed, err)
	}
	if o := f.master.Order("master-1"); o.Side != exchange.Buy {
		t.Fatalf("want buy, got %s", o.Side)
	}
	f.master.FillOrder("master-1", "0.0016")
	f.bot.Cycle(ctx, "BTC")
	if countPrefix(f.slave.CallLog(), "market sell") != 1 {
		t.Fatalf("log=%v", f.slave.CallLog())
	}
	if !f.trades[0].ProfitMYR.IsPositive() {
		t.Fatalf("buy low sell high should profit: %s", f.trades[0].ProfitMYR)
	}
}

func TestBelowThresholdAndBalances(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "80200", "20000") // 0.25% gap
	if placed, _ := f.bot.Cycle(ctx, "BTC"); placed {
		t.Fatal("gap below threshold must not trade")
	}
	f = newFixture(t, "100000", "20000")
	f.master.SetBalance("BTC", "0.0001") // 10 MYR < 100
	if placed, _ := f.bot.Cycle(ctx, "BTC"); placed {
		t.Fatal("insufficient master asset balance must not trade")
	}
	f = newFixture(t, "100000", "20000")
	f.slave.SetBalance("USDT", "1")
	if placed, _ := f.bot.Cycle(ctx, "BTC"); placed {
		t.Fatal("insufficient slave quote balance must not trade")
	}
}

func TestPicksLargestGapSlave(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "100000", "20000")
	s2 := exchange.NewMock("slave2")
	s2.SetTicker("BTC/USDT", "15000", "15000", "15000") // 60000 MYR: bigger gap
	s2.SetBalance("USDT", "100000")
	f.bot.Slaves = append(f.bot.Slaves, s2)
	f.bot.Cycle(ctx, "BTC")
	f.master.FillOrder("master-1", "0.001")
	f.bot.Cycle(ctx, "BTC")
	if len(s2.CallLog()) != 1 || len(f.slave.CallLog()) != 0 {
		t.Fatalf("hedge must go to the largest-gap slave: %v %v", s2.CallLog(), f.slave.CallLog())
	}
}

func TestFailedCancelKeepsOrderTracked(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "100000", "20000")
	f.bot.Cycle(ctx, "BTC")
	f.master.FailNext["cancel"] = errors.New("boom")
	if _, err := f.bot.Cycle(ctx, "BTC"); err == nil {
		t.Fatal("expected cancel error")
	}
	if _, err := f.bot.Cycle(ctx, "BTC"); err != nil {
		t.Fatalf("retry should succeed: %v", err)
	}
	if f.master.Order("master-1").Status != exchange.StatusCanceled {
		t.Fatal("order should end cancelled")
	}
}

// SIGINT path: cancel the context while an order is resting and filled.
func TestShutdownCancelsAndHedges(t *testing.T) {
	f := newFixture(t, "100000", "20000")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.bot.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for countPrefix(f.master.CallLog(), "limit sell") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("order never placed")
		}
		time.Sleep(time.Millisecond)
	}
	f.master.FillOrder("master-1", "0.001")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if countPrefix(f.slave.CallLog(), "market buy") != 1 || f.tradeCount() != 1 {
		t.Fatalf("fill must be hedged on shutdown: %v", f.slave.CallLog())
	}
}

func TestShutdownCancelsUnfilled(t *testing.T) {
	f := newFixture(t, "100000", "20000")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.bot.Run(ctx); close(done) }()
	for countPrefix(f.master.CallLog(), "limit sell") == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	if f.master.Order("master-1").Status != exchange.StatusCanceled || len(f.slave.CallLog()) != 0 {
		t.Fatal("unfilled order must be cancelled without hedge")
	}
}

func TestRunRefusesLowSafeGap(t *testing.T) {
	f := newFixture(t, "100000", "20000")
	f.bot.Cfg.SafeGapPct = decimal.RequireFromString("0.4")
	f.bot.Run(context.Background()) // returns immediately
	if len(f.master.CallLog()) != 0 {
		t.Fatal("must not trade")
	}
}

func TestRateProvider(t *testing.T) {
	ctx := context.Background()
	if r, _ := NewRateProvider(decimal.NewFromInt(4), nil, "").Rate(ctx); !r.Equal(decimal.NewFromInt(4)) {
		t.Fatal("fixed")
	}
	m := exchange.NewMock("m")
	m.SetTicker("USDT/MYR", "4.2", "4.2", "4.2")
	calls := 0
	now := time.Unix(1000, 0)
	p := &RateProvider{Master: m, Market: "USDT/MYR", Now: func() time.Time { return now },
		APIFetch: func(context.Context) (decimal.Decimal, error) { calls++; return decimal.NewFromInt(9), nil }}
	if r, _ := p.Rate(ctx); !r.Equal(decimal.RequireFromString("4.2")) || calls != 0 {
		t.Fatal("market ticker preferred")
	}
	m.SetTicker("USDT/MYR", "5", "5", "5")
	if r, _ := p.Rate(ctx); !r.Equal(decimal.RequireFromString("4.2")) {
		t.Fatal("cached within 60 minutes")
	}
	now = now.Add(61 * time.Minute)
	if r, _ := p.Rate(ctx); !r.Equal(decimal.NewFromInt(5)) {
		t.Fatal("cache expiry")
	}
	// no market -> API fallback
	p2 := &RateProvider{Master: exchange.NewMock("x"), Market: "USDT/MYR", Now: func() time.Time { return now },
		APIFetch: func(context.Context) (decimal.Decimal, error) { return decimal.NewFromInt(9), nil }}
	if r, _ := p2.Rate(ctx); !r.Equal(decimal.NewFromInt(9)) {
		t.Fatal("api fallback")
	}
	p3 := &RateProvider{Master: exchange.NewMock("x"), Market: "USDT/MYR",
		APIFetch: func(context.Context) (decimal.Decimal, error) { return decimal.Zero, errors.New("down") }}
	if _, err := p3.Rate(ctx); err == nil {
		t.Fatal("expected error when both sources fail")
	}
}
