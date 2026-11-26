package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseValidConfig(t *testing.T) {
	raw := `
# SmallCloud sample configuration
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
replica_url = "file:///tmp/smallcloud-replicas/org#42/invoice" # inline comment
sync_ms = 500
`
	cfg, err := Parse(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
	if cfg.App.Name != "invoice-micro" {
		t.Errorf("expected app.name invoice-micro, got %q", cfg.App.Name)
	}
	if cfg.App.OrgID != "org-indie-42" {
		t.Errorf("expected app.org_id org-indie-42, got %q", cfg.App.OrgID)
	}
	if cfg.App.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.App.Port)
	}
	if cfg.Resources.CPUMillicores != 1000 || cfg.Resources.MemoryMB != 512 {
		t.Errorf("unexpected resources: %+v", cfg.Resources)
	}
	if cfg.Budget.MaxMonthlyUSD != 5.00 || !cfg.Budget.HardStop {
		t.Errorf("unexpected budget: %+v", cfg.Budget)
	}
	if cfg.Storage.ReplicaURL != "file:///tmp/smallcloud-replicas/org#42/invoice" {
		t.Errorf("unexpected replica_url: %q", cfg.Storage.ReplicaURL)
	}
}

func TestParseRejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantErr string
	}{
		{
			name: "missing max_monthly_usd",
			toml: `
[app]
name = "my-app"
org_id = "org-1"
domain = "my-app.smallcloud.dev"
`,
			wantErr: "budget.max_monthly_usd is required",
		},
		{
			name: "negative max_monthly_usd",
			toml: `
[app]
name = "my-app"
org_id = "org-1"
domain = "my-app.smallcloud.dev"
[budget]
max_monthly_usd = -1.50
`,
			wantErr: "budget.max_monthly_usd is required and must be greater than 0",
		},
		{
			name: "disabled hard_stop rejected",
			toml: `
[app]
name = "my-app"
org_id = "org-1"
domain = "my-app.smallcloud.dev"
[budget]
max_monthly_usd = 5.00
hard_stop = false
`,
			wantErr: "budget.hard_stop cannot be disabled",
		},
		{
			name: "missing org_id tenant scope",
			toml: `
[app]
name = "my-app"
domain = "my-app.smallcloud.dev"
[budget]
max_monthly_usd = 5.00
`,
			wantErr: "app.org_id is required",
		},
		{
			name: "invalid replica scheme",
			toml: `
[app]
name = "my-app"
org_id = "org-1"
domain = "my-app.smallcloud.dev"
[budget]
max_monthly_usd = 5.00
[storage]
replica_url = "http://insecure-bucket/db"
`,
			wantErr: "storage.replica_url must use s3://, gs://, or file:// scheme",
		},
		{
			name: "caddyfile injection in healthcheck",
			toml: `
[app]
name = "my-app"
org_id = "org-1"
domain = "my-app.smallcloud.dev"
healthcheck = "/healthz } respond /pwn 200 {"
[budget]
max_monthly_usd = 5.00
`,
			wantErr: "Caddyfile tokens",
		},
		{
			name: "invalid domain hostname",
			toml: `
[app]
name = "my-app"
org_id = "org-1"
domain = "bad_host!.local"
[budget]
max_monthly_usd = 5.00
`,
			wantErr: "RFC-1123 hostname",
		},
		{
			name: "path traversal in sqlite_path",
			toml: `
[app]
name = "my-app"
org_id = "org-1"
domain = "my-app.smallcloud.dev"
[budget]
max_monthly_usd = 5.00
[storage]
sqlite_path = "/data/../../etc/passwd"
`,
			wantErr: "clean absolute path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.toml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "smallcloud.toml")
	content := `[app]
name = "demo-saas"
org_id = "acme-org"
domain = "demo.smallcloud.dev"
[budget]
max_monthly_usd = 5.00
`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	cfg, err := LoadFile(p)
	if err != nil {
		t.Fatalf("LoadFile failed: %v", err)
	}
	if cfg.App.Name != "demo-saas" || cfg.Resources.MemoryMB != 256 {
		t.Fatalf("unexpected loaded config: %+v", cfg)
	}
}
