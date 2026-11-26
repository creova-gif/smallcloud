package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type ContainerSpec struct {
	OrgID         string `json:"org_id"`
	AppName       string `json:"app_name"`
	ReleaseID     string `json:"release_id"`
	AppDir        string `json:"app_dir"`
	Port          int    `json:"port"`
	CPUMillicores int    `json:"cpu_millicores"`
	MemoryMB      int    `json:"memory_mb"`
	SQLitePath    string `json:"sqlite_path"`
}

type ContainerRuntime interface {
	Start(ctx context.Context, spec ContainerSpec) (upstream string, err error)
	Stop(ctx context.Context, orgID, appName, releaseID string) error
}

type NoopRuntime struct{}

func (NoopRuntime) Start(ctx context.Context, spec ContainerSpec) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if err := validateTenantSlugs(spec.OrgID, spec.AppName); err != nil {
		return "", err
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535, got %d", spec.Port)
	}
	return fmt.Sprintf("127.0.0.1:%d", spec.Port), nil
}

func (NoopRuntime) Stop(ctx context.Context, orgID, appName, releaseID string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return err
	}
	return nil
}

type DockerRuntime struct {
	Binary        string
	CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (r DockerRuntime) binaryName() string {
	if strings.TrimSpace(r.Binary) != "" {
		return r.Binary
	}
	return "docker"
}

func (r DockerRuntime) runCmd(ctx context.Context, args ...string) ([]byte, error) {
	if r.CommandRunner != nil {
		return r.CommandRunner(ctx, r.binaryName(), args...)
	}
	return exec.CommandContext(ctx, r.binaryName(), args...).CombinedOutput()
}

func validateReleaseID(releaseID string) error {
	if strings.TrimSpace(releaseID) == "" {
		return errors.New("release_id is required")
	}
	if strings.ContainsAny(releaseID, " \t\r\n/") {
		return errors.New("release_id contains invalid characters")
	}
	return nil
}

func containerName(orgID, appName, releaseID string) string {
	return fmt.Sprintf("sc-%s-%s-%s", orgID, appName, releaseID)
}

func imageTag(orgID, appName, releaseID string) string {
	return fmt.Sprintf("smallcloud-%s-%s:%s", orgID, appName, releaseID)
}

func (r DockerRuntime) Start(ctx context.Context, spec ContainerSpec) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateTenantSlugs(spec.OrgID, spec.AppName); err != nil {
		return "", err
	}
	if err := validateReleaseID(spec.ReleaseID); err != nil {
		return "", err
	}
	if strings.TrimSpace(spec.AppDir) == "" {
		return "", errors.New("app_dir is required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535, got %d", spec.Port)
	}
	if spec.CPUMillicores < 100 {
		return "", fmt.Errorf("cpuMillicores must be >= 100, got %d", spec.CPUMillicores)
	}
	if spec.MemoryMB < 64 {
		return "", fmt.Errorf("memoryMB must be >= 64, got %d", spec.MemoryMB)
	}

	tag := imageTag(spec.OrgID, spec.AppName, spec.ReleaseID)
	out, err := r.runCmd(ctx, "build", "-t", tag, spec.AppDir)
	if err != nil {
		return "", fmt.Errorf("docker build %q: %w: %s", tag, err, strings.TrimSpace(string(out)))
	}

	name := containerName(spec.OrgID, spec.AppName, spec.ReleaseID)
	cpus := strconv.FormatFloat(float64(spec.CPUMillicores)/1000.0, 'f', -1, 64)
	memory := fmt.Sprintf("%dm", spec.MemoryMB)
	portMap := fmt.Sprintf("127.0.0.1:%d:%d", spec.Port, spec.Port)

	runArgs := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--label", "smallcloud.org=" + spec.OrgID,
		"--label", "smallcloud.app=" + spec.AppName,
		"--label", "smallcloud.release=" + spec.ReleaseID,
		"--cpus", cpus,
		"--memory", memory,
		"-p", portMap,
	}

	if sqlitePath := strings.TrimSpace(spec.SQLitePath); sqlitePath != "" {
		sqliteDir := filepath.Dir(sqlitePath)
		runArgs = append(runArgs, "-v", fmt.Sprintf("%s:%s", sqliteDir, sqliteDir))
		runArgs = append(runArgs, "-e", "SMALLCLOUD_SQLITE_PATH="+sqlitePath)
	}

	runArgs = append(runArgs, tag)

	out, err = r.runCmd(ctx, runArgs...)
	if err != nil {
		return "", fmt.Errorf("docker run %q: %w: %s", name, err, strings.TrimSpace(string(out)))
	}

	return fmt.Sprintf("127.0.0.1:%d", spec.Port), nil
}

func (r DockerRuntime) Stop(ctx context.Context, orgID, appName, releaseID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return err
	}
	if err := validateReleaseID(releaseID); err != nil {
		return err
	}

	name := containerName(orgID, appName, releaseID)
	out, err := r.runCmd(ctx, "rm", "-f", name)
	if err != nil {
		return fmt.Errorf("docker rm %q: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
