// Command hft-arb-bot runs the cross-exchange arbitrage loop.
//
// WARNING: the exchange adapters are unverified against live APIs. Not
// financial advice, not audited, use at your own risk.
package main

import (
	"context"
	"encoding/csv"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zinxer/hft-arb-bot-go/internal/config"
	"github.com/zinxer/hft-arb-bot-go/internal/exchange"
	"github.com/zinxer/hft-arb-bot-go/internal/strategy"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	master := exchange.NewLuno(cfg.Master.Key, cfg.Master.Secret)
	var slaves []exchange.Exchange
	if cfg.Kraken.Key != "" {
		log.Info("slave exchange enabled", "name", "Kraken")
		slaves = append(slaves, exchange.NewKraken(cfg.Kraken.Key, cfg.Kraken.Secret))
	}
	if cfg.Binance.Key != "" {
		log.Info("slave exchange enabled", "name", "Binance")
		slaves = append(slaves, exchange.NewBinance(cfg.Binance.Key, cfg.Binance.Secret))
	}
	log.Warn("exchange adapters are UNVERIFIED against live APIs; use tiny sizes")

	f, err := os.OpenFile(cfg.TradeCSVPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)

	bot := strategy.NewBot(cfg, master, slaves, strategy.NewRateProvider(cfg.FixedUSDTMYR, master, cfg.UsdtMyrMarket), log)
	bot.OnTrade = func(t strategy.Trade) {
		_ = w.Write([]string{time.Now().UTC().Format(time.RFC3339), t.Market, "master", t.MasterSide, t.Amount.String(),
			t.LimitPrice.String(), t.MasterValue.String(), t.SlaveEx, t.SlaveSide, t.SlaveFilled.String(),
			t.SlavePriceMYR.String(), t.Rate.String(), t.SafeGapPct.String(), t.ProfitMYR.String()})
		w.Flush()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("starting", "assets", cfg.Assets, "slaves", len(slaves))
	bot.Run(ctx) // returns after cancelling open orders and hedging fills
	return w.Error()
}
