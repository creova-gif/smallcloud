package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	validSlugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-_]{1,62}$`)
	validDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	unsafeEnvKeys = map[string]struct{}{
		"LD_PRELOAD":            {},
		"LD_LIBRARY_PATH":       {},
		"DYLD_INSERT_LIBRARIES": {},
		"DYLD_LIBRARY_PATH":     {},
		"LD_AUDIT":              {},
	}
)

func IsValidSlug(s string) bool {
	return validSlugRe.MatchString(s)
}

type Config struct {
	App       AppConfig         `json:"app"`
	Resources ResourcesConfig   `json:"resources"`
	Budget    BudgetConfig      `json:"budget"`
	Storage   StorageConfig     `json:"storage"`
	Env       map[string]string `toml:"env" json:"env,omitempty"`
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

func stripCommentOutsideQuotes(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			quote = ch
			continue
		}
		if ch == '#' {
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}

func hasUnsafeCaddyToken(s string) bool {
	return strings.ContainsAny(s, " \t\r\n\"'{};#")
}

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

	scanner := bufio.NewScanner(r)
	section := ""
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := stripCommentOutsideQuotes(strings.TrimSpace(scanner.Text()))
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
		rawKey := strings.TrimSpace(parts[0])
		key := strings.ToLower(rawKey)
		if section == "env" {
			key = rawKey
		}
		rawVal := strings.TrimSpace(parts[1])

		if err := assignField(cfg, section, key, rawVal); err != nil {
			return nil, fmt.Errorf("line %d (%s.%s): %w", lineNum, section, key, err)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func assignField(cfg *Config, section, key, rawVal string) error {
	if section == "env" {
		val, err := parseString(rawVal)
		if err != nil {
			return err
		}
		if cfg.Env == nil {
			cfg.Env = make(map[string]string)
		}
		cfg.Env[key] = val
		return nil
	}

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

func (c *Config) Validate() error {
	if !IsValidSlug(c.App.Name) {
		return errors.New("app.name is required and must be a lowercase alphanumeric slug (2-63 chars)")
	}
	if !IsValidSlug(c.App.OrgID) {
		return errors.New("app.org_id is required for tenant scoping and must be a lowercase alphanumeric slug")
	}
	domain := strings.ToLower(strings.TrimSpace(c.App.Domain))
	if domain == "" || len(domain) > 253 || !validDomainRe.MatchString(domain) {
		return errors.New("app.domain is required and must be a valid RFC-1123 hostname")
	}
	if c.App.Port < 1 || c.App.Port > 65535 {
		return fmt.Errorf("app.port must be between 1 and 65535, got %d", c.App.Port)
	}
	if !strings.HasPrefix(c.App.Healthcheck, "/") || hasUnsafeCaddyToken(c.App.Healthcheck) {
		return errors.New("app.healthcheck must start with '/' and contain no whitespace or Caddyfile tokens")
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
	if math.IsNaN(c.Budget.AlertAtPct) || math.IsInf(c.Budget.AlertAtPct, 0) || c.Budget.AlertAtPct <= 0 || c.Budget.AlertAtPct >= 100 {
		return errors.New("budget.alert_at_pct must be between 0 and 100 (exclusive)")
	}
	if !c.Budget.HardStop {
		return errors.New("budget.hard_stop cannot be disabled: SmallCloud requires 100% hard-stop enforcement at max_monthly_usd")
	}
	sqlPath := strings.TrimSpace(c.Storage.SQLitePath)
	if sqlPath == "" || !strings.HasPrefix(sqlPath, "/") || strings.Contains(sqlPath, "..") || filepath.Clean(sqlPath) != sqlPath {
		return errors.New("storage.sqlite_path must be a clean absolute path without '..'")
	}
	replicaURL := strings.TrimSpace(c.Storage.ReplicaURL)
	if replicaURL != "" {
		if (!strings.HasPrefix(replicaURL, "s3://") &&
			!strings.HasPrefix(replicaURL, "gs://") &&
			!strings.HasPrefix(replicaURL, "file://")) || strings.ContainsAny(replicaURL, " \t\r\n\"'{};") {
			return errors.New("storage.replica_url must use s3://, gs://, or file:// scheme with no unsafe characters")
		}
	}
	if c.Storage.SyncMs < 50 || c.Storage.SyncMs > 60000 {
		return fmt.Errorf("storage.sync_ms must be between 50 and 60000, got %d", c.Storage.SyncMs)
	}
	if len(c.Env) > 0 {
		keys := make([]string, 0, len(c.Env))
		for k := range c.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "" {
				return errors.New("env key cannot be empty")
			}
			if strings.ContainsRune(k, '=') || strings.IndexFunc(k, unicode.IsSpace) >= 0 {
				return fmt.Errorf("env key %q contains invalid characters", k)
			}
			if _, unsafe := unsafeEnvKeys[strings.ToUpper(k)]; unsafe {
				return fmt.Errorf("env key %q is forbidden for security reasons", k)
			}
			if strings.ContainsRune(c.Env[k], '\x00') {
				return fmt.Errorf("env value for %q contains NUL byte", k)
			}
		}
	}
	return nil
}
