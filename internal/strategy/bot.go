package strategy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/zinxer/hft-arb-bot-go/internal/config"
	"github.com/zinxer/hft-arb-bot-go/internal/exchange"
)

const opTimeout = 30 * time.Second

// Trade is the record emitted after a master fill has been hedged on a slave.
type Trade struct {
	Market        string
	MasterSide    string
	Amount        decimal.Decimal
	LimitPrice    decimal.Decimal
	MasterValue   decimal.Decimal
	SlaveEx       string
	SlaveSide     string
	SlaveFilled   decimal.Decimal
	SlavePriceMYR decimal.Decimal
	Rate          decimal.Decimal
	SafeGapPct    decimal.Decimal
	ProfitMYR     decimal.Decimal
}

type openOrder struct {
	id      string
	slaveEx string
}

// Bot runs one concurrent loop per asset: one master exchange against one or
// more slave exchanges.
type Bot struct {
	Master       exchange.Exchange
	Slaves       []exchange.Exchange // order matters: ties go to the earlier slave
	Cfg          config.Config
	Rate         *RateProvider
	Log          *slog.Logger
	OnTrade      func(Trade)
	PollInterval time.Duration // pause between cycles when no order was placed
	BalanceTTL   time.Duration // balances are shared between asset loops for this long

	balMu    sync.Mutex
	balStamp time.Time
	balCache map[string]exchange.Balance
	open     map[string]map[string]openOrder // asset -> master side -> order (touched only by that asset's goroutine, then by Run after they exit)
}

// NewBot wires a bot with sane defaults.
func NewBot(cfg config.Config, master exchange.Exchange, slaves []exchange.Exchange, rate *RateProvider, log *slog.Logger) *Bot {
	if log == nil {
		log = slog.Default()
	}
	return &Bot{Master: master, Slaves: slaves, Cfg: cfg, Rate: rate, Log: log,
		PollInterval: time.Second, BalanceTTL: time.Second}
}

func (b *Bot) slave(name string) exchange.Exchange {
	for _, s := range b.Slaves {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

func (b *Bot) openFor(asset string) map[string]openOrder {
	if b.open == nil {
		b.open = map[string]map[string]openOrder{}
	}
	if b.open[asset] == nil {
		b.open[asset] = map[string]openOrder{}
	}
	return b.open[asset]
}

// Run cycles every asset concurrently until ctx is cancelled, then waits for
// the loops to stop, cancels any open master limit orders and hedges fills.
func (b *Bot) Run(ctx context.Context) {
	if b.Cfg.SafeGapPct.LessThan(config.MinSafeGapPercent) {
		b.Log.Error("SAFE_GAP_PERCENT below minimum", "value", b.Cfg.SafeGapPct.String())
		return
	}
	for _, a := range b.Cfg.Assets { // allocate state before goroutines start
		b.openFor(a)
	}
	var wg sync.WaitGroup
	for _, asset := range b.Cfg.Assets {
		wg.Add(1)
		go func(asset string) {
			defer wg.Done()
			b.loop(ctx, asset)
		}(asset)
	}
	wg.Wait()
	b.Log.Info("stopping: cancelling open limit orders and hedging fills")
	b.Shutdown()
}

// Shutdown cancels open master orders and hedges any fills. It must not run
// concurrently with cycle loops (Run guarantees this).
func (b *Bot) Shutdown() {
	for _, asset := range b.Cfg.Assets {
		ctx, cancel := context.WithTimeout(context.Background(), 2*opTimeout)
		if err := b.cancelAndHedge(ctx, asset); err != nil {
			b.Log.Error("shutdown cleanup failed", "asset", asset, "err", err)
		}
		cancel()
	}
	b.Log.Info("exited")
}

func (b *Bot) loop(ctx context.Context, asset string) {
	for ctx.Err() == nil {
		placed, err := b.Cycle(ctx, asset)
		if err != nil && ctx.Err() == nil {
			b.Log.Error("cycle error", "asset", asset, "err", err)
		}
		pause := b.PollInterval
		if placed {
			pause = b.Cfg.CycleTime
		}
		sleepCtx(ctx, pause)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

type snapshot struct {
	rate     decimal.Decimal
	master   exchange.Ticker
	slaves   map[string]exchange.Ticker
	balances map[string]exchange.Balance
}

func (b *Bot) balances(ctx context.Context) (map[string]exchange.Balance, error) {
	b.balMu.Lock()
	defer b.balMu.Unlock()
	if b.balCache != nil && time.Since(b.balStamp) < b.BalanceTTL {
		return b.balCache, nil
	}
	out := map[string]exchange.Balance{}
	all := append([]exchange.Exchange{b.Master}, b.Slaves...)
	for _, ex := range all {
		bal, err := ex.FetchBalance(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s balance: %w", ex.Name(), err)
		}
		out[ex.Name()] = bal
	}
	b.balCache, b.balStamp = out, time.Now()
	return out, nil
}

func (b *Bot) fetch(ctx context.Context, asset string) (snapshot, error) {
	var s snapshot
	var err error
	if s.rate, err = b.Rate.Rate(ctx); err != nil {
		return s, err
	}
	if s.master, err = b.Master.FetchTicker(ctx, asset+"/"+b.Cfg.MasterBase); err != nil {
		return s, fmt.Errorf("%s ticker: %w", b.Master.Name(), err)
	}
	s.slaves = map[string]exchange.Ticker{}
	for _, sl := range b.Slaves {
		t, err := sl.FetchTicker(ctx, asset+"/"+b.Cfg.SlaveBase)
		if err != nil {
			return s, fmt.Errorf("%s ticker: %w", sl.Name(), err)
		}
		s.slaves[sl.Name()] = t
	}
	s.balances, err = b.balances(ctx)
	return s, err
}

// Cycle runs one iteration for an asset: fetch data, cancel the previous limit
// order (hedging if filled), then evaluate the gap and possibly rest a new order.
// placed is true when a new master limit order was created.
func (b *Bot) Cycle(ctx context.Context, asset string) (placed bool, err error) {
	snap, err := b.fetch(ctx, asset)
	if err != nil {
		return false, err
	}
	// Order operations are detached from ctx so a shutdown never orphans an order.
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()
	if err := b.cancelAndHedge(opCtx, asset); err != nil {
		return false, err
	}
	if ctx.Err() != nil {
		return false, nil // shutting down: do not rest a new order
	}
	return b.placeLimit(opCtx, asset, snap)
}

func (b *Bot) placeLimit(ctx context.Context, asset string, s snapshot) (bool, error) {
	cfg := b.Cfg
	var quotes []SlaveQuote
	for _, sl := range b.Slaves {
		quotes = append(quotes, SlaveQuote{Name: sl.Name(), Bid: s.slaves[sl.Name()].Bid})
	}
	slaveEx, gap, ok := ChooseSlave(s.master.Last, s.rate, quotes)
	if !ok {
		b.Log.Info("no gap", "asset", asset)
		return false, nil
	}
	b.Log.Info("best price gap", "asset", asset, "gap_pct", gap.StringFixed(2), "slave", slaveEx)
	if !MeetsThreshold(gap, cfg.SafeGapPct) {
		return false, nil
	}
	side := MasterSide(gap)
	mb := s.balances[b.Master.Name()]
	sb := s.balances[slaveEx]
	bal := Balances{
		MasterAssetFree: mb[asset].Free, MasterQuoteFree: mb[cfg.MasterBase].Free,
		SlaveAssetFree: sb[asset].Free, SlaveQuoteFree: sb[cfg.SlaveBase].Free,
		MasterLast: s.master.Last, SlaveLast: s.slaves[slaveEx].Last,
	}
	if ok, why := BalancesSufficient(side, bal, s.rate, cfg.OrderSizeMYR, len(cfg.Assets)); !ok {
		b.Log.Warn("balance check failed", "asset", asset, "side", side, "slave", slaveEx, "reason", why)
		return false, nil
	}
	price := LimitPrice(side, s.master.Bid, s.master.Ask, cfg.PriceOffset)
	amount := OrderAmount(cfg.OrderSizeMYR, price, config.Decimals[asset])
	if !amount.IsPositive() {
		b.Log.Warn("order amount rounds to zero", "asset", asset)
		return false, nil
	}
	o, err := b.Master.CreateLimitOrder(ctx, asset+"/"+cfg.MasterBase, side, amount, price, cfg.PostOnly)
	if err != nil {
		return false, fmt.Errorf("create limit order: %w", err)
	}
	b.openFor(asset)[side] = openOrder{id: o.ID, slaveEx: slaveEx}
	b.Log.Info("limit order placed", "asset", asset, "side", side, "amount", amount.String(), "price", price.String(), "slave", slaveEx)
	return true, nil
}

// cancelAndHedge cancels each open master order for the asset and, if it
// (partially) filled, places the opposite-side market order on the slave.
func (b *Bot) cancelAndHedge(ctx context.Context, asset string) error {
	var errs []error
	orders := b.openFor(asset)
	for _, side := range []string{exchange.Buy, exchange.Sell} {
		oo, ok := orders[side]
		if !ok {
			continue
		}
		if err := b.settle(ctx, asset, side, oo); err != nil {
			errs = append(errs, err) // order stays tracked and is retried
			continue
		}
		delete(orders, side)
	}
	return errors.Join(errs...)
}

func (b *Bot) settle(ctx context.Context, asset, side string, oo openOrder) error {
	market := asset + "/" + b.Cfg.MasterBase
	o, err := b.Master.FetchOrder(ctx, oo.id, market)
	if err != nil {
		return fmt.Errorf("fetch master order: %w", err)
	}
	if o.Status == exchange.StatusOpen {
		if err := b.Master.CancelOrder(ctx, oo.id, market); err != nil {
			return fmt.Errorf("cancel master order: %w", err)
		}
		if o, err = b.Master.FetchOrder(ctx, oo.id, market); err != nil {
			return fmt.Errorf("fetch master order: %w", err)
		}
		if o.Status == exchange.StatusOpen {
			return errors.New("master order still open after cancel")
		}
	}
	if !o.Filled.IsPositive() {
		b.Log.Info("order not filled, cancelled", "asset", asset, "side", side)
		return nil
	}
	places := config.Decimals[asset]
	amount := o.Filled.Round(places)
	if !amount.IsPositive() {
		b.Log.Warn("master fill too small for slave precision, skipping hedge", "asset", asset, "filled", o.Filled.String())
		return nil
	}
	slave := b.slave(oo.slaveEx)
	if slave == nil {
		return fmt.Errorf("unknown slave %q for hedge", oo.slaveEx)
	}
	hedgeSide := HedgeSide(side)
	slaveMarket := asset + "/" + b.Cfg.SlaveBase
	mo, err := slave.CreateMarketOrder(ctx, slaveMarket, hedgeSide, amount)
	if err != nil {
		return fmt.Errorf("hedge on %s: %w", oo.slaveEx, err)
	}
	// From here the hedge exists on the slave: never retry it.
	if b.Cfg.HedgeSettle > 0 {
		sleepCtx(ctx, b.Cfg.HedgeSettle)
	}
	info, err := slave.FetchOrder(ctx, mo.ID, slaveMarket)
	if err != nil {
		b.Log.Error("hedge placed but read-back failed; profit unknown", "asset", asset, "slave", oo.slaveEx, "err", err)
		return nil
	}
	rate, err := b.Rate.Rate(ctx)
	if err != nil {
		b.Log.Error("hedge placed but rate unavailable; profit unknown", "asset", asset, "err", err)
		return nil
	}
	slaveMYR := UsdtToMyr(info.Average, rate)
	t := Trade{
		Market: market, MasterSide: side, Amount: amount, LimitPrice: o.Price,
		MasterValue: amount.Mul(o.Price).Round(2), SlaveEx: oo.slaveEx, SlaveSide: hedgeSide,
		SlaveFilled: info.Filled, SlavePriceMYR: slaveMYR, Rate: rate, SafeGapPct: b.Cfg.SafeGapPct,
		ProfitMYR: Profit(side, o.Price, slaveMYR, amount).Round(2),
	}
	b.Log.Info("trade hedged", "market", t.Market, "master_side", t.MasterSide, "amount", t.Amount.String(),
		"limit_price", t.LimitPrice.String(), "slave", t.SlaveEx, "slave_price_myr", t.SlavePriceMYR.String(),
		"profit_myr", t.ProfitMYR.String())
	if b.OnTrade != nil {
		b.OnTrade(t)
	}
	return nil
}
