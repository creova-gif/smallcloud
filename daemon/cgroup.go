package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const cgroupPeriodMicros = 100000

type CgroupLimits struct {
	OrgID      string `json:"org_id"`
	AppName    string `json:"app_name"`
	CPUQuotaUs int    `json:"cpu_quota_us"`
	CPUPeriod  int    `json:"cpu_period_us"`
	MemoryMaxB int64  `json:"memory_max_bytes"`
	CgroupPath string `json:"cgroup_path"`
}

type CgroupManager struct {
	rootDir string
}

func NewCgroupManager(rootDir string) *CgroupManager {
	return &CgroupManager{rootDir: rootDir}
}

func (m *CgroupManager) Apply(orgID, appName string, cpuMillicores, memoryMB int) (*CgroupLimits, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return nil, err
	}
	if cpuMillicores < 100 {
		return nil, fmt.Errorf("cpuMillicores must be >= 100, got %d", cpuMillicores)
	}
	if memoryMB < 64 {
		return nil, fmt.Errorf("memoryMB must be >= 64, got %d", memoryMB)
	}

	cgPath := filepath.Join(m.rootDir, "smallcloud", orgID, appName)
	if err := os.MkdirAll(cgPath, 0o750); err != nil {
		return nil, fmt.Errorf("create cgroup path %q: %w", cgPath, err)
	}

	quotaUs := (cpuMillicores * cgroupPeriodMicros) / 1000
	cpuMaxContent := fmt.Sprintf("%d %d\n", quotaUs, cgroupPeriodMicros)
	if err := os.WriteFile(filepath.Join(cgPath, "cpu.max"), []byte(cpuMaxContent), 0o600); err != nil {
		return nil, fmt.Errorf("write cpu.max: %w", err)
	}

	memBytes := int64(memoryMB) * 1024 * 1024
	memMaxContent := strconv.FormatInt(memBytes, 10) + "\n"
	if err := os.WriteFile(filepath.Join(cgPath, "memory.max"), []byte(memMaxContent), 0o600); err != nil {
		return nil, fmt.Errorf("write memory.max: %w", err)
	}

	return &CgroupLimits{
		OrgID:      orgID,
		AppName:    appName,
		CPUQuotaUs: quotaUs,
		CPUPeriod:  cgroupPeriodMicros,
		MemoryMaxB: memBytes,
		CgroupPath: cgPath,
	}, nil
}
