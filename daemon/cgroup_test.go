package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCgroupManagerApplySuccess(t *testing.T) {
	cases := []struct {
		name          string
		orgID         string
		appName       string
		cpuMillicores int
		memoryMB      int
		wantQuotaUs   int
		wantCPUMax    string
		wantMemoryB   int64
		wantMemoryMax string
	}{
		{
			name:          "500m_256mb",
			orgID:         "acme",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      256,
			wantQuotaUs:   50000,
			wantCPUMax:    "50000 100000\n",
			wantMemoryB:   256 * 1024 * 1024,
			wantMemoryMax: "268435456\n",
		},
		{
			name:          "2000m_1024mb",
			orgID:         "beta-org",
			appName:       "api-v2",
			cpuMillicores: 2000,
			memoryMB:      1024,
			wantQuotaUs:   200000,
			wantCPUMax:    "200000 100000\n",
			wantMemoryB:   1024 * 1024 * 1024,
			wantMemoryMax: "1073741824\n",
		},
		{
			name:          "minimum_bounds",
			orgID:         "org1",
			appName:       "app1",
			cpuMillicores: 100,
			memoryMB:      64,
			wantQuotaUs:   10000,
			wantCPUMax:    "10000 100000\n",
			wantMemoryB:   64 * 1024 * 1024,
			wantMemoryMax: "67108864\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			mgr := NewCgroupManager(rootDir)

			limits, err := mgr.Apply(tc.orgID, tc.appName, tc.cpuMillicores, tc.memoryMB)
			if err != nil {
				t.Fatalf("Apply() unexpected error: %v", err)
			}

			wantPath := filepath.Join(rootDir, "smallcloud", tc.orgID, tc.appName)
			if limits.OrgID != tc.orgID {
				t.Errorf("OrgID = %q, want %q", limits.OrgID, tc.orgID)
			}
			if limits.AppName != tc.appName {
				t.Errorf("AppName = %q, want %q", limits.AppName, tc.appName)
			}
			if limits.CPUQuotaUs != tc.wantQuotaUs {
				t.Errorf("CPUQuotaUs = %d, want %d", limits.CPUQuotaUs, tc.wantQuotaUs)
			}
			if limits.CPUPeriod != cgroupPeriodMicros {
				t.Errorf("CPUPeriod = %d, want %d", limits.CPUPeriod, cgroupPeriodMicros)
			}
			if limits.MemoryMaxB != tc.wantMemoryB {
				t.Errorf("MemoryMaxB = %d, want %d", limits.MemoryMaxB, tc.wantMemoryB)
			}
			if limits.CgroupPath != wantPath {
				t.Errorf("CgroupPath = %q, want %q", limits.CgroupPath, wantPath)
			}

			cpuBytes, err := os.ReadFile(filepath.Join(wantPath, "cpu.max"))
			if err != nil {
				t.Fatalf("read cpu.max: %v", err)
			}
			if string(cpuBytes) != tc.wantCPUMax {
				t.Errorf("cpu.max = %q, want %q", string(cpuBytes), tc.wantCPUMax)
			}

			memBytes, err := os.ReadFile(filepath.Join(wantPath, "memory.max"))
			if err != nil {
				t.Fatalf("read memory.max: %v", err)
			}
			if string(memBytes) != tc.wantMemoryMax {
				t.Errorf("memory.max = %q, want %q", string(memBytes), tc.wantMemoryMax)
			}
		})
	}
}

func TestCgroupManagerApplyValidationErrors(t *testing.T) {
	cases := []struct {
		name          string
		orgID         string
		appName       string
		cpuMillicores int
		memoryMB      int
	}{
		{
			name:          "cpu_below_minimum",
			orgID:         "acme",
			appName:       "web",
			cpuMillicores: 99,
			memoryMB:      256,
		},
		{
			name:          "cpu_zero",
			orgID:         "acme",
			appName:       "web",
			cpuMillicores: 0,
			memoryMB:      256,
		},
		{
			name:          "cpu_negative",
			orgID:         "acme",
			appName:       "web",
			cpuMillicores: -100,
			memoryMB:      256,
		},
		{
			name:          "memory_below_minimum",
			orgID:         "acme",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      63,
		},
		{
			name:          "memory_zero",
			orgID:         "acme",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      0,
		},
		{
			name:          "memory_negative",
			orgID:         "acme",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      -64,
		},
		{
			name:          "empty_org_id",
			orgID:         "",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      256,
		},
		{
			name:          "empty_app_name",
			orgID:         "acme",
			appName:       "",
			cpuMillicores: 500,
			memoryMB:      256,
		},
		{
			name:          "traversal_org_id",
			orgID:         "../etc",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      256,
		},
		{
			name:          "traversal_app_name",
			orgID:         "acme",
			appName:       "../../passwd",
			cpuMillicores: 500,
			memoryMB:      256,
		},
		{
			name:          "dotdot_org_id",
			orgID:         "..",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      256,
		},
		{
			name:          "slash_in_app_name",
			orgID:         "acme",
			appName:       "web/sub",
			cpuMillicores: 500,
			memoryMB:      256,
		},
		{
			name:          "uppercase_org_id",
			orgID:         "ACME",
			appName:       "web",
			cpuMillicores: 500,
			memoryMB:      256,
		},
		{
			name:          "space_in_app_name",
			orgID:         "acme",
			appName:       "web app",
			cpuMillicores: 500,
			memoryMB:      256,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rootDir := t.TempDir()
			mgr := NewCgroupManager(rootDir)

			limits, err := mgr.Apply(tc.orgID, tc.appName, tc.cpuMillicores, tc.memoryMB)
			if err == nil {
				t.Fatalf("Apply() expected error, got nil (limits=%+v)", limits)
			}
			if limits != nil {
				t.Fatalf("Apply() expected nil limits on error, got %+v", limits)
			}

			entries, readErr := os.ReadDir(rootDir)
			if readErr != nil {
				t.Fatalf("ReadDir(%q): %v", rootDir, readErr)
			}
			if len(entries) != 0 {
				t.Errorf("expected no files created in %q on validation error, found %d entries", rootDir, len(entries))
			}
		})
	}
}
