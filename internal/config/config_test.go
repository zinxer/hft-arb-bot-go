package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"LUNO_KEY", "LUNO_SECRET", "KRAKEN_KEY", "KRAKEN_SECRET", "BINANCE_KEY", "BINANCE_SECRET", "ASSETS", "ORDER_SIZE_MYR", "SAFE_GAP_PERCENT", "CYCLE_TIME_MS", "POST_ONLY", "USDTMYR", "PRICE_OFFSET"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func base() map[string]string {
	return map[string]string{"LUNO_KEY": "k", "LUNO_SECRET": "s", "BINANCE_KEY": "k", "BINANCE_SECRET": "s",
		"ASSETS": "btc, ETH", "ORDER_SIZE_MYR": "100", "SAFE_GAP_PERCENT": "0.8", "CYCLE_TIME_MS": "5000", "POST_ONLY": "true"}
}

func TestLoadOK(t *testing.T) {
	setEnv(t, base())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Assets) != 2 || c.Assets[0] != "BTC" || !c.PostOnly || c.CycleTime.Milliseconds() != 5000 {
		t.Fatalf("bad config: %+v", c)
	}
	if c.PriceOffset.String() != "0.0001" {
		t.Fatalf("default offset: %s", c.PriceOffset)
	}
}

func TestSafeGapFloor(t *testing.T) {
	m := base()
	m["SAFE_GAP_PERCENT"] = "0.49"
	setEnv(t, m)
	if _, err := Load(); err == nil {
		t.Fatal("expected error below 0.5")
	}
	m["SAFE_GAP_PERCENT"] = "0.5"
	setEnv(t, m)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestRequiresSlaveAndAssets(t *testing.T) {
	m := base()
	delete(m, "BINANCE_KEY")
	setEnv(t, m)
	if _, err := Load(); err == nil {
		t.Fatal("expected missing slave error")
	}
	m = base()
	m["ASSETS"] = "DOGE"
	setEnv(t, m)
	if _, err := Load(); err == nil {
		t.Fatal("expected unsupported asset error")
	}
}

func TestDotEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	_ = os.WriteFile(p, []byte("# c\nFOO_TEST_X=\"bar\"\nFOO_TEST_Y=1\n"), 0o600)
	t.Setenv("FOO_TEST_Y", "keep")
	os.Unsetenv("FOO_TEST_X")
	if err := LoadDotEnv(p); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("FOO_TEST_X")
	if os.Getenv("FOO_TEST_X") != "bar" || os.Getenv("FOO_TEST_Y") != "keep" {
		t.Fatal("dotenv semantics wrong")
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatal(err)
	}
}
