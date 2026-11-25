package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var validSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-_]{1,62}$`)

// Config represents the parsed and validated smallcloud.toml configuration.
type Config struct {
	App       AppConfig       `json:"app"`
	Resources ResourcesConfig `json:"resources"`
	Budget    BudgetConfig    `json:"budget"`
	Storage   StorageConfig   `json:"storage"`
}

type AppConfig struct {
	Name        string `json:"name"`
	OrgID       string `json:"org_id"`
	Domain      string `json:"domain"`
	Port        int    `json:"port"`
	Healthcheck string `json:"healthcheck"`
	Runtime     string `json:"runtime"`
}

type ResourcesConfig struct {
	CPUMillicores int `json:"cpu_millicores"`
	MemoryMB      int `json:"memory_mb"`
}

type BudgetConfig struct {
	MaxMonthlyUSD float64 `json:"max_monthly_usd"`
	AlertAtPct    float64 `json:"alert_at_pct"`
	HardStop      bool    `json:"hard_stop"`
}

type StorageConfig struct {
	SQLitePath string `json:"sqlite_path"`
	ReplicaURL string `json:"replica_url"`
	SyncMs     int    `json:"sync_ms"`
}

// LoadFile reads and parses a smallcloud.toml file from disk.
func LoadFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file %q: %w", path, err)
	}
	defer func() {
		_ = f.Close()
	}()
	return Parse(f)
}

// Parse parses a smallcloud.toml reader and validates all security and budget invariants.
func Parse(r io.Reader) (*Config, error) {
	cfg := &Config{
		App: AppConfig{
			Port:        8000,
			Healthcheck: "/healthz",
			Runtime:     "dockerfile",
		},
		Resources: ResourcesConfig{
			CPUMillicores: 500,
			MemoryMB:      256,
		},
		Budget: BudgetConfig{
			AlertAtPct: 80.0,
			HardStop:   true,
		},
		Storage: StorageConfig{
			SQLitePath: "/data/app.db",
			SyncMs:     1000,
		},
	}

	hardStopSet := false
	scanner := bufio.NewScanner(r)
	section := ""
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("line %d: invalid key=value syntax: %q", lineNum, line)
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		rawVal := strings.TrimSpace(parts[1])

		if err := assignField(cfg, section, key, rawVal, &hardStopSet); err != nil {
			return nil, fmt.Errorf("line %d (%s.%s): %w", lineNum, section, key, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan config: %w", err)
	}

	if err := cfg.Validate(hardStopSet); err != nil {
		return nil, err
	}

	return cfg, nil
}

func assignField(cfg *Config, section, key, rawVal string, hardStopSet *bool) error {
	fullKey := key
	if section != "" {
		fullKey = section + "." + key
	}

	switch fullKey {
	case "app.name", "name":
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		cfg.App.Name = val
	case "app.org_id", "org_id":
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		cfg.App.OrgID = val
	case "app.domain", "domain":
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		cfg.App.Domain = val
	case "app.port", "port":
		val, err := strconv.Atoi(rawVal)
		if err != nil {
			return fmt.Errorf("invalid integer %q: %w", rawVal, err)
		}
		cfg.App.Port = val
	case "app.healthcheck", "healthcheck":
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		cfg.App.Healthcheck = val
	case "app.runtime", "runtime":
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		cfg.App.Runtime = val
	case "resources.cpu_millicores", "cpu_millicores":
		val, err := strconv.Atoi(rawVal)
		if err != nil {
			return fmt.Errorf("invalid integer %q: %w", rawVal, err)
		}
		cfg.Resources.CPUMillicores = val
	case "resources.memory_mb", "memory_mb":
		val, err := strconv.Atoi(rawVal)
		if err != nil {
			return fmt.Errorf("invalid integer %q: %w", rawVal, err)
		}
		cfg.Resources.MemoryMB = val
	case "budget.max_monthly_usd", "max_monthly_usd":
		val, err := strconv.ParseFloat(rawVal, 64)
		if err != nil {
			return fmt.Errorf("invalid float %q: %w", rawVal, err)
		}
		cfg.Budget.MaxMonthlyUSD = val
	case "budget.alert_at_pct", "alert_at_pct":
		val, err := strconv.ParseFloat(rawVal, 64)
		if err != nil {
			return fmt.Errorf("invalid float %q: %w", rawVal, err)
		}
		cfg.Budget.AlertAtPct = val
	case "budget.hard_stop", "hard_stop":
		val, err := strconv.ParseBool(rawVal)
		if err != nil {
			return fmt.Errorf("invalid bool %q: %w", rawVal, err)
		}
		cfg.Budget.HardStop = val
		*hardStopSet = true
	case "storage.sqlite_path", "sqlite_path":
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		cfg.Storage.SQLitePath = val
	case "storage.replica_url", "replica_url":
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		cfg.Storage.ReplicaURL = val
	case "storage.sync_ms", "sync_ms":
		val, err := strconv.Atoi(rawVal)
		if err != nil {
			return fmt.Errorf("invalid integer %q: %w", rawVal, err)
		}
		cfg.Storage.SyncMs = val
	default:
		return fmt.Errorf("unknown configuration field %q", fullKey)
	}
	return nil
}

func parseString(raw string) (string, error) {
	if len(raw) >= 2 && ((raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'')) {
		return raw[1 : len(raw)-1], nil
	}
	return "", fmt.Errorf("string value must be quoted: %q", raw)
}

// Validate enforces SmallCloud's tenant isolation, resource bounds, and mandatory spend-cap rules.
func (c *Config) Validate(hardStopExplicit bool) error {
	if !validSlugRe.MatchString(c.App.Name) {
		return errors.New("app.name is required and must be a lowercase alphanumeric slug (2-63 chars)")
	}
	if !validSlugRe.MatchString(c.App.OrgID) {
		return errors.New("app.org_id is required for tenant scoping and must be a lowercase alphanumeric slug")
	}
	if strings.TrimSpace(c.App.Domain) == "" || strings.ContainsAny(c.App.Domain, " /\\") {
		return errors.New("app.domain is required and must be a valid hostname")
	}
	if c.App.Port < 1 || c.App.Port > 65535 {
		return fmt.Errorf("app.port must be between 1 and 65535, got %d", c.App.Port)
	}
	if !strings.HasPrefix(c.App.Healthcheck, "/") {
		return errors.New("app.healthcheck must start with '/'")
	}
	if c.Resources.CPUMillicores < 100 || c.Resources.CPUMillicores > 16000 {
		return fmt.Errorf("resources.cpu_millicores must be between 100 and 16000, got %d", c.Resources.CPUMillicores)
	}
	if c.Resources.MemoryMB < 64 || c.Resources.MemoryMB > 65536 {
		return fmt.Errorf("resources.memory_mb must be between 64 and 65536, got %d", c.Resources.MemoryMB)
	}
	if math.IsNaN(c.Budget.MaxMonthlyUSD) || math.IsInf(c.Budget.MaxMonthlyUSD, 0) || c.Budget.MaxMonthlyUSD <= 0 {
		return errors.New("budget.max_monthly_usd is required and must be greater than 0")
	}
	if c.Budget.MaxMonthlyUSD > 10000 {
		return errors.New("budget.max_monthly_usd exceeds maximum allowed safety cap of 10000 USD")
	}
	if c.Budget.AlertAtPct <= 0 || c.Budget.AlertAtPct >= 100 {
		return errors.New("budget.alert_at_pct must be between 0 and 100 (exclusive)")
	}
	if hardStopExplicit && !c.Budget.HardStop {
		return errors.New("budget.hard_stop cannot be disabled: SmallCloud requires 100% hard-stop enforcement at max_monthly_usd")
	}
	if strings.TrimSpace(c.Storage.SQLitePath) == "" || !strings.HasPrefix(c.Storage.SQLitePath, "/") {
		return errors.New("storage.sqlite_path must be an absolute path")
	}
	if strings.TrimSpace(c.Storage.ReplicaURL) != "" {
		if !strings.HasPrefix(c.Storage.ReplicaURL, "s3://") &&
			!strings.HasPrefix(c.Storage.ReplicaURL, "gs://") &&
			!strings.HasPrefix(c.Storage.ReplicaURL, "file://") {
			return errors.New("storage.replica_url must use s3://, gs://, or file:// scheme")
		}
	}
	if c.Storage.SyncMs < 50 || c.Storage.SyncMs > 60000 {
		return fmt.Errorf("storage.sync_ms must be between 50 and 60000, got %d", c.Storage.SyncMs)
	}
	return nil
}
