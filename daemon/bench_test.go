package daemon

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

func BenchmarkBudgetCheckAllowed(b *testing.B) {
	bt := NewBudgetTracker(nil)
	if err := bt.RegisterApp("org-alpha", "app-one", 25.0, 80.0); err != nil {
		b.Fatalf("RegisterApp: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bt.CheckAllowed("org-alpha", "app-one"); err != nil {
			b.Fatalf("CheckAllowed: %v", err)
		}
	}
}

func BenchmarkCaddyRouterSwapAndRender(b *testing.B) {
	router := NewCaddyRouter()
	var rendered string

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry, err := router.SwapRoute("org-alpha", "app-one", "app.smallcloud.dev", fmt.Sprintf("v%d", i), "127.0.0.1:9001")
		if err != nil {
			b.Fatalf("SwapRoute: %v", err)
		}
		rendered = RenderCaddyfile(entry, "/healthz")
	}
	if rendered == "" {
		b.Fatal("expected non-empty rendered Caddyfile")
	}
}

func BenchmarkRateLimiterAllow(b *testing.B) {
	rl := NewRateLimiter(1_000_000_000, time.Minute)
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !rl.Allow("127.0.0.1", now) {
			b.Fatal("expected Allow to return true")
		}
	}
}

func BenchmarkConfigParseAndValidate(b *testing.B) {
	raw := `
[app]
name = "invoice-micro"
org_id = "org-indie-42"
domain = "invoices.smallcloud.dev"
port = 8080
healthcheck = "/healthz"

[resources]
cpu_millicores = 1000
memory_mb = 512

[budget]
max_monthly_usd = 5.00
alert_at_pct = 80.0
hard_stop = true

[storage]
sqlite_path = "/data/invoices.db"
replica_url = "gs://smallcloud-replicas/org-indie-42/invoice-micro"
sync_ms = 500
`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg, err := config.Parse(strings.NewReader(raw))
		if err != nil {
			b.Fatalf("Parse: %v", err)
		}
		if err := cfg.Validate(); err != nil {
			b.Fatalf("Validate: %v", err)
		}
	}
}
