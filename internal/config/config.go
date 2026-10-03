// Package config loads bot settings from environment variables.
// Variable names match the original Node.js bot's .env.example.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// MinSafeGapPercent is the minimum SAFE_GAP_PERCENT the bot accepts
// (same floor as the Node implementation).
var MinSafeGapPercent = decimal.RequireFromString("0.5")

// Decimals is the per-asset order-amount precision table (same as the Node bot).
var Decimals = map[string]int32{"BTC": 4, "BCH": 3, "ETH": 3, "XRP": 0, "LTC": 2}

// Creds are API credentials for one exchange.
type Creds struct{ Key, Secret string }

// Config is the full bot configuration.
type Config struct {
	Master        Creds
	Kraken        Creds
	Binance       Creds
	Assets        []string
	OrderSizeMYR  decimal.Decimal
	SafeGapPct    decimal.Decimal
	CycleTime     time.Duration
	PostOnly      bool
	FixedUSDTMYR  decimal.Decimal // zero means "fetch it"
	PriceOffset   decimal.Decimal
	HedgeSettle   time.Duration // wait before reading back a hedge order
	TradeCSVPath  string
	MasterBase    string
	SlaveBase     string
	UsdtMyrMarket string
}

// LoadDotEnv reads KEY=VALUE lines from path into the process environment
// without overriding variables that are already set. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, set := os.LookupEnv(k); !set {
			if err := os.Setenv(k, v); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// Load builds a Config from the environment (os.Getenv) and validates it.
func Load() (Config, error) {
	c := Config{
		Master:        Creds{os.Getenv("LUNO_KEY"), os.Getenv("LUNO_SECRET")},
		Kraken:        Creds{os.Getenv("KRAKEN_KEY"), os.Getenv("KRAKEN_SECRET")},
		Binance:       Creds{os.Getenv("BINANCE_KEY"), os.Getenv("BINANCE_SECRET")},
		MasterBase:    "MYR",
		SlaveBase:     "USDT",
		UsdtMyrMarket: "USDT/MYR",
		HedgeSettle:   time.Second,
		TradeCSVPath:  "trade_data.csv",
	}
	for _, a := range strings.Split(os.Getenv("ASSETS"), ",") {
		if a = strings.ToUpper(strings.TrimSpace(a)); a != "" {
			c.Assets = append(c.Assets, a)
		}
	}
	var err error
	if c.OrderSizeMYR, err = reqDec("ORDER_SIZE_MYR"); err != nil {
		return c, err
	}
	if c.SafeGapPct, err = reqDec("SAFE_GAP_PERCENT"); err != nil {
		return c, err
	}
	ms, err := strconv.Atoi(os.Getenv("CYCLE_TIME_MS"))
	if err != nil || ms < 0 {
		return c, errors.New("CYCLE_TIME_MS must be a non-negative integer")
	}
	c.CycleTime = time.Duration(ms) * time.Millisecond
	c.PostOnly = os.Getenv("POST_ONLY") == "true"
	if v := os.Getenv("USDTMYR"); v != "" {
		if c.FixedUSDTMYR, err = decimal.NewFromString(v); err != nil || !c.FixedUSDTMYR.IsPositive() {
			return c, errors.New("USDTMYR must be a positive number")
		}
	}
	c.PriceOffset = decimal.RequireFromString("0.0001")
	if v := os.Getenv("PRICE_OFFSET"); v != "" {
		if c.PriceOffset, err = decimal.NewFromString(v); err != nil || c.PriceOffset.IsNegative() {
			return c, errors.New("PRICE_OFFSET must be a non-negative number")
		}
	}
	return c, c.Validate()
}

// Validate checks invariants shared with the Node bot.
func (c Config) Validate() error {
	if c.SafeGapPct.LessThan(MinSafeGapPercent) {
		return fmt.Errorf("SAFE_GAP_PERCENT (%s) should not be less than %s", c.SafeGapPct, MinSafeGapPercent)
	}
	if len(c.Assets) == 0 {
		return errors.New("ASSETS is required")
	}
	for _, a := range c.Assets {
		if _, ok := Decimals[a]; !ok {
			return fmt.Errorf("unsupported asset %q (supported: BTC, BCH, ETH, XRP, LTC)", a)
		}
	}
	if !c.OrderSizeMYR.IsPositive() {
		return errors.New("ORDER_SIZE_MYR must be positive")
	}
	if c.Master.Key == "" || c.Master.Secret == "" {
		return errors.New("LUNO_KEY and LUNO_SECRET (master exchange) are required")
	}
	if c.Kraken.Key == "" && c.Binance.Key == "" {
		return errors.New("at least one slave exchange key (KRAKEN_KEY or BINANCE_KEY) is required")
	}
	return nil
}

func reqDec(name string) (decimal.Decimal, error) {
	v := os.Getenv(name)
	if v == "" {
		return decimal.Zero, fmt.Errorf("%s is required", name)
	}
	d, err := decimal.NewFromString(v)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}
