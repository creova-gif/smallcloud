package config

import (
	"strings"
	"testing"
)

func FuzzConfigParse(f *testing.F) {
	seeds := []string{
		"[app]\nname = \"my-app\"\norg_id = \"my-org\"\ndomain = \"app.example.com\"\nport = 8080\nhealthcheck = \"/healthz\"\nruntime = \"dockerfile\"\n\n[resources]\ncpu_millicores = 500\nmemory_mb = 256\n\n[budget]\nmax_monthly_usd = 25.0\nalert_at_pct = 80.0\nhard_stop = true\n\n[storage]\nsqlite_path = \"/data/app.db\"\nreplica_url = \"s3://bucket/app.db\"\nsync_ms = 1000\n\n[env]\nLOG_LEVEL = \"info\"\nPORT_STR = '8080'\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\n\n[budget]\nmax_monthly_usd = 0\nalert_at_pct = 100\nhard_stop = false\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\n\n[budget]\nmax_monthly_usd = NaN\nalert_at_pct = Inf\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\n\n[budget]\nmax_monthly_usd = 10001\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\nport = 0\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\nport = 65536\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\nport = notanumber\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"-bad..domain!\"\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"\"\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\n\n[env]\nLD_PRELOAD = \"/tmp/evil.so\"\nBAD KEY = \"v\"\n= \"empty\"\nKEY = \"val\x00\"\n",
		"[app]\nname = \"a1\"\norg_id = \"o1\"\ndomain = \"example.com\"\nhealthcheck = \"/health\t{}\"\n\n[storage]\nreplica_url = \"http://bad\"\nsqlite_path = \"/data/../etc/passwd\"\n",
		"\x00\x01\x02\x03\x1b[31m\r\n\t\x7f",
		"[app]\nname = \"a1\x00\"\norg_id = \"o1\"\ndomain = \"example.com\"\n# comment \"inside\"\n",
		"",
		"[app",
		"[app]\nname = \"unclosed",
		"port =",
		"[env]\nKEY",
		"# only a comment",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data string) {
		cfg, err := Parse(strings.NewReader(data))
		if err != nil {
			return
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Parse succeeded but Validate failed: %v", err)
		}
	})
}
