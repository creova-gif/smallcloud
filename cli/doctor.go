package cli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

const (
	DoctorStatusPass = "pass"
	DoctorStatusWarn = "warn"
	DoctorStatusFail = "fail"
)

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type DoctorReport struct {
	Passed bool          `json:"passed"`
	Checks []DoctorCheck `json:"checks"`
}

func RunDoctor(appDir, stateDir string) (*DoctorReport, error) {
	checks := make([]DoctorCheck, 0, 6)

	tomlPath := filepath.Join(appDir, "smallcloud.toml")
	cfg, cfgErr := config.LoadFile(tomlPath)
	if cfgErr != nil {
		checks = append(checks, DoctorCheck{
			Name:   "smallcloud.toml",
			Status: DoctorStatusFail,
			Detail: cfgErr.Error(),
		})
	} else {
		checks = append(checks, DoctorCheck{
			Name:   "smallcloud.toml",
			Status: DoctorStatusPass,
			Detail: fmt.Sprintf("valid config for %s (%s)", cfg.App.Name, cfg.App.Domain),
		})
	}

	dockerfilePath := filepath.Join(appDir, "Dockerfile")
	dfBytes, dfErr := os.ReadFile(dockerfilePath)
	if dfErr != nil {
		checks = append(checks, DoctorCheck{
			Name:   "Dockerfile",
			Status: DoctorStatusFail,
			Detail: dfErr.Error(),
		})
	} else {
		checks = append(checks, DoctorCheck{
			Name:   "Dockerfile",
			Status: DoctorStatusPass,
			Detail: "Dockerfile present",
		})
		if cfg != nil {
			if ports := parseDockerfileExposePorts(string(dfBytes)); len(ports) > 0 {
				matched := false
				for _, p := range ports {
					if p == cfg.App.Port {
						matched = true
						break
					}
				}
				if matched {
					checks = append(checks, DoctorCheck{
						Name:   "Dockerfile EXPOSE",
						Status: DoctorStatusPass,
						Detail: fmt.Sprintf("EXPOSE matches app port %d", cfg.App.Port),
					})
				} else {
					checks = append(checks, DoctorCheck{
						Name:   "Dockerfile EXPOSE",
						Status: DoctorStatusWarn,
						Detail: fmt.Sprintf("EXPOSE ports %v do not match app port %d", ports, cfg.App.Port),
					})
				}
			}
		}
	}

	for _, name := range []string{"Caddyfile", "litestream.yml"} {
		checks = append(checks, checkOptionalFile(appDir, name))
	}

	checks = append(checks, checkCgroupsV2(stateDir))

	passed := true
	for _, c := range checks {
		if c.Status == DoctorStatusFail {
			passed = false
			break
		}
	}

	return &DoctorReport{
		Passed: passed,
		Checks: checks,
	}, nil
}

func RunDoctorLive(dir, stateDir, caddyAdminURL, daemonURL string) (*DoctorReport, error) {
	report, err := RunDoctor(dir, stateDir)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 2 * time.Second}
	if caddyAdminURL != "" {
		targetURL := strings.TrimRight(caddyAdminURL, "/") + "/config/"
		report.Checks = append(report.Checks, probeHTTPCheck(client, "caddy_admin", targetURL))
	}
	if daemonURL != "" {
		targetURL := strings.TrimRight(daemonURL, "/") + "/healthz"
		report.Checks = append(report.Checks, probeHTTPCheck(client, "daemon_healthz", targetURL))
	}

	report.Passed = true
	for _, c := range report.Checks {
		if c.Status == DoctorStatusFail {
			report.Passed = false
			break
		}
	}

	return report, nil
}

func probeHTTPCheck(client *http.Client, name, targetURL string) DoctorCheck {
	resp, err := client.Get(targetURL)
	if err != nil {
		return DoctorCheck{
			Name:   name,
			Status: DoctorStatusFail,
			Detail: err.Error(),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return DoctorCheck{
			Name:   name,
			Status: DoctorStatusPass,
			Detail: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, targetURL),
		}
	}

	return DoctorCheck{
		Name:   name,
		Status: DoctorStatusFail,
		Detail: fmt.Sprintf("HTTP %d from %s", resp.StatusCode, targetURL),
	}
}

func checkOptionalFile(appDir, name string) DoctorCheck {
	info, err := os.Stat(filepath.Join(appDir, name))
	if err != nil || info.IsDir() {
		return DoctorCheck{
			Name:   name,
			Status: DoctorStatusWarn,
			Detail: fmt.Sprintf("%s not found; will be generated on deploy", name),
		}
	}
	return DoctorCheck{
		Name:   name,
		Status: DoctorStatusPass,
		Detail: fmt.Sprintf("%s present", name),
	}
}

func checkCgroupsV2(stateDir string) DoctorCheck {
	const cgroupControllersPath = "/sys/fs/cgroup/cgroup.controllers"
	if _, err := os.Stat(cgroupControllersPath); err == nil {
		return DoctorCheck{
			Name:   "cgroups",
			Status: DoctorStatusPass,
			Detail: "native cgroups v2 available",
		}
	}
	detail := "using stateDir cgroups fallback"
	if stateDir != "" {
		detail = fmt.Sprintf("using stateDir cgroups fallback (%s)", filepath.Join(stateDir, "cgroups"))
	}
	return DoctorCheck{
		Name:   "cgroups",
		Status: DoctorStatusWarn,
		Detail: detail,
	}
}

func parseDockerfileExposePorts(content string) []int {
	var ports []int
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "EXPOSE") {
			continue
		}
		for _, token := range fields[1:] {
			portStr := strings.SplitN(token, "/", 2)[0]
			if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
				ports = append(ports, p)
			}
		}
	}
	return ports
}
