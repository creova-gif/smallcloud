package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type recordedCommand struct {
	name string
	args []string
}

func TestNoopRuntimeStartAndStop(t *testing.T) {
	var rt ContainerRuntime = NoopRuntime{}
	ctx := context.Background()

	upstream, err := rt.Start(ctx, ContainerSpec{
		OrgID:         "acme",
		AppName:       "billing",
		ReleaseID:     "rel-1",
		AppDir:        "/tmp/app",
		Port:          18001,
		CPUMillicores: 500,
		MemoryMB:      256,
	})
	if err != nil {
		t.Fatalf("NoopRuntime.Start failed: %v", err)
	}
	if upstream != "127.0.0.1:18001" {
		t.Fatalf("expected upstream 127.0.0.1:18001, got %q", upstream)
	}

	if err := rt.Stop(ctx, "acme", "billing", "rel-1"); err != nil {
		t.Fatalf("NoopRuntime.Stop failed: %v", err)
	}
}

func TestNoopRuntimeValidation(t *testing.T) {
	rt := NoopRuntime{}
	ctx := context.Background()

	if _, err := rt.Start(ctx, ContainerSpec{
		OrgID:   "INVALID_ORG",
		AppName: "billing",
		Port:    8000,
	}); err == nil {
		t.Fatal("expected error for invalid org_id")
	}

	if _, err := rt.Start(ctx, ContainerSpec{
		OrgID:   "acme",
		AppName: "../escape",
		Port:    8000,
	}); err == nil {
		t.Fatal("expected error for invalid app_name")
	}

	if _, err := rt.Start(ctx, ContainerSpec{
		OrgID:   "acme",
		AppName: "billing",
		Port:    0,
	}); err == nil {
		t.Fatal("expected error for invalid port")
	}

	if err := rt.Stop(ctx, "../bad", "billing", "rel-1"); err == nil {
		t.Fatal("expected error for invalid org_id on Stop")
	}

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := rt.Start(canceledCtx, ContainerSpec{
		OrgID:   "acme",
		AppName: "billing",
		Port:    8000,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDockerRuntimeStartAndStop(t *testing.T) {
	var calls []recordedCommand
	rt := DockerRuntime{
		Binary: "podman",
		CommandRunner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, recordedCommand{
				name: name,
				args: append([]string(nil), args...),
			})
			return []byte("ok"), nil
		},
	}

	spec := ContainerSpec{
		OrgID:         "acme",
		AppName:       "billing",
		ReleaseID:     "rel-42",
		AppDir:        "/srv/apps/billing",
		Port:          18042,
		CPUMillicores: 750,
		MemoryMB:      512,
		SQLitePath:    "/data/billing/app.db",
	}

	upstream, err := rt.Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("DockerRuntime.Start failed: %v", err)
	}
	if upstream != "127.0.0.1:18042" {
		t.Fatalf("expected upstream 127.0.0.1:18042, got %q", upstream)
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 commands (build + run), got %d", len(calls))
	}

	buildCall := calls[0]
	if buildCall.name != "podman" {
		t.Fatalf("expected binary podman, got %q", buildCall.name)
	}
	expectedBuild := []string{"build", "-t", "smallcloud-acme-billing:rel-42", "/srv/apps/billing"}
	if !slices.Equal(buildCall.args, expectedBuild) {
		t.Fatalf("unexpected build args: got %v, want %v", buildCall.args, expectedBuild)
	}

	runCall := calls[1]
	if runCall.name != "podman" {
		t.Fatalf("expected binary podman, got %q", runCall.name)
	}
	runStr := strings.Join(runCall.args, " ")
	for _, want := range []string{
		"--name sc-acme-billing-rel-42",
		"--label smallcloud.managed=true",
		"--label smallcloud.org=acme",
		"--label smallcloud.app=billing",
		"--label smallcloud.release=rel-42",
		"--cpus 0.750",
		"--memory 512m",
		"-p 127.0.0.1:18042:18042",
		"-v /data/billing:/data/billing",
		"-e SMALLCLOUD_SQLITE_PATH=/data/billing/app.db",
		"smallcloud-acme-billing:rel-42",
	} {
		if !strings.Contains(runStr, want) {
			t.Fatalf("expected docker run args %q to contain %q", runStr, want)
		}
	}

	if err := rt.Stop(context.Background(), "acme", "billing", "rel-42"); err != nil {
		t.Fatalf("DockerRuntime.Stop failed: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 commands total, got %d", len(calls))
	}
	expectedStop := []string{"rm", "-f", "sc-acme-billing-rel-42"}
	if !slices.Equal(calls[2].args, expectedStop) {
		t.Fatalf("unexpected stop args: got %v, want %v", calls[2].args, expectedStop)
	}
}

func TestDockerRuntimeDefaultBinaryAndErrors(t *testing.T) {
	t.Run("default binary and build error", func(t *testing.T) {
		var binaryUsed string
		rt := DockerRuntime{
			CommandRunner: func(_ context.Context, name string, args ...string) ([]byte, error) {
				binaryUsed = name
				return []byte("syntax error"), errors.New("exit status 1")
			},
		}

		_, err := rt.Start(context.Background(), ContainerSpec{
			OrgID:         "acme",
			AppName:       "billing",
			ReleaseID:     "rel-1",
			AppDir:        "/tmp/app",
			Port:          8080,
			CPUMillicores: 500,
			MemoryMB:      256,
		})
		if err == nil {
			t.Fatal("expected build error")
		}
		if binaryUsed != "docker" {
			t.Fatalf("expected default binary docker, got %q", binaryUsed)
		}
		if !strings.Contains(err.Error(), "docker build") || !strings.Contains(err.Error(), "syntax error") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("run error", func(t *testing.T) {
		step := 0
		rt := DockerRuntime{
			CommandRunner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				step++
				if step == 1 {
					return []byte("built"), nil
				}
				return []byte("port already allocated"), errors.New("exit status 125")
			},
		}

		_, err := rt.Start(context.Background(), ContainerSpec{
			OrgID:         "acme",
			AppName:       "billing",
			ReleaseID:     "rel-2",
			AppDir:        "/tmp/app",
			Port:          8080,
			CPUMillicores: 1000,
			MemoryMB:      128,
		})
		if err == nil {
			t.Fatal("expected run error")
		}
		if !strings.Contains(err.Error(), "docker run") || !strings.Contains(err.Error(), "port already allocated") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("stop error", func(t *testing.T) {
		rt := DockerRuntime{
			CommandRunner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				return []byte("permission denied"), errors.New("exit status 1")
			},
		}

		err := rt.Stop(context.Background(), "acme", "billing", "rel-2")
		if err == nil {
			t.Fatal("expected stop error")
		}
		if !strings.Contains(err.Error(), "docker rm") {
			t.Fatalf("unexpected error message: %v", err)
		}
	})

	t.Run("validation errors", func(t *testing.T) {
		called := false
		rt := DockerRuntime{
			CommandRunner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
				called = true
				return nil, nil
			},
		}

		cases := []ContainerSpec{
			{OrgID: "BAD", AppName: "billing", ReleaseID: "rel-1", AppDir: "/tmp", Port: 8000, CPUMillicores: 500, MemoryMB: 256},
			{OrgID: "acme", AppName: "BAD", ReleaseID: "rel-1", AppDir: "/tmp", Port: 8000, CPUMillicores: 500, MemoryMB: 256},
			{OrgID: "acme", AppName: "billing", ReleaseID: "", AppDir: "/tmp", Port: 8000, CPUMillicores: 500, MemoryMB: 256},
			{OrgID: "acme", AppName: "billing", ReleaseID: "rel 1", AppDir: "/tmp", Port: 8000, CPUMillicores: 500, MemoryMB: 256},
			{OrgID: "acme", AppName: "billing", ReleaseID: "rel-1", AppDir: "", Port: 8000, CPUMillicores: 500, MemoryMB: 256},
			{OrgID: "acme", AppName: "billing", ReleaseID: "rel-1", AppDir: "/tmp", Port: 70000, CPUMillicores: 500, MemoryMB: 256},
			{OrgID: "acme", AppName: "billing", ReleaseID: "rel-1", AppDir: "/tmp", Port: 8000, CPUMillicores: 50, MemoryMB: 256},
			{OrgID: "acme", AppName: "billing", ReleaseID: "rel-1", AppDir: "/tmp", Port: 8000, CPUMillicores: 500, MemoryMB: 32},
		}

		for i, tc := range cases {
			if _, err := rt.Start(context.Background(), tc); err == nil {
				t.Fatalf("case %d: expected validation error", i)
			}
		}

		if err := rt.Stop(context.Background(), "BAD", "billing", "rel-1"); err == nil {
			t.Fatal("expected Stop validation error for orgID")
		}
		if err := rt.Stop(context.Background(), "acme", "billing", ""); err == nil {
			t.Fatal("expected Stop validation error for releaseID")
		}
		if called {
			t.Fatal("CommandRunner should not be called when validation fails")
		}
	})
}

func TestDockerRuntimeReadinessProbeSuccess(t *testing.T) {
	var attempts atomic.Int32
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse server addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	var calls []recordedCommand
	rt := DockerRuntime{
		HealthProbeTimeout:  time.Second,
		HealthProbeInterval: 10 * time.Millisecond,
		CommandRunner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, recordedCommand{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}

	upstream, err := rt.Start(context.Background(), ContainerSpec{
		OrgID:         "acme",
		AppName:       "billing",
		ReleaseID:     "rel-ready",
		AppDir:        "/tmp/app",
		Port:          port,
		CPUMillicores: 500,
		MemoryMB:      256,
		Healthcheck:   "/readyz",
	})
	if err != nil {
		t.Fatalf("expected Start to succeed after readiness probe, got %v", err)
	}
	if upstream != "127.0.0.1:"+portStr {
		t.Fatalf("expected upstream 127.0.0.1:%s, got %q", portStr, upstream)
	}
	if gotPath != "/readyz" {
		t.Fatalf("expected probe path /readyz, got %q", gotPath)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 commands (build + run), got %d", len(calls))
	}
}

func TestDockerRuntimeReadinessProbeFailureCleansUpContainer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parse server addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	var calls []recordedCommand
	rt := DockerRuntime{
		HealthProbeTimeout:  50 * time.Millisecond,
		HealthProbeInterval: 10 * time.Millisecond,
		CommandRunner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, recordedCommand{name: name, args: append([]string(nil), args...)})
			return []byte("ok"), nil
		},
	}

	_, err = rt.Start(context.Background(), ContainerSpec{
		OrgID:         "acme",
		AppName:       "billing",
		ReleaseID:     "rel-fail",
		AppDir:        "/tmp/app",
		Port:          port,
		CPUMillicores: 500,
		MemoryMB:      256,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 commands (build + run + rm), got %d", len(calls))
	}
	expectedStop := []string{"rm", "-f", "sc-acme-billing-rel-fail"}
	if !slices.Equal(calls[2].args, expectedStop) {
		t.Fatalf("expected cleanup command %v, got %v", expectedStop, calls[2].args)
	}
}

func TestDockerRuntimeStartSortedEnv(t *testing.T) {
	var calls []recordedCommand
	rt := DockerRuntime{
		CommandRunner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, recordedCommand{
				name: name,
				args: append([]string(nil), args...),
			})
			return []byte("ok"), nil
		},
	}

	spec := ContainerSpec{
		OrgID:         "acme",
		AppName:       "billing",
		ReleaseID:     "rel-env",
		AppDir:        "/srv/apps/billing",
		Port:          18042,
		CPUMillicores: 500,
		MemoryMB:      256,
		Env: map[string]string{
			"ZEBRA":  "last",
			"ALPHA":  "first",
			"MIDDLE": "mid",
		},
	}

	if _, err := rt.Start(context.Background(), spec); err != nil {
		t.Fatalf("DockerRuntime.Start failed: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 commands (build + run), got %d", len(calls))
	}

	runArgs := calls[1].args
	expectedTail := []string{
		"-e", "ALPHA=first",
		"-e", "MIDDLE=mid",
		"-e", "ZEBRA=last",
		"smallcloud-acme-billing:rel-env",
	}
	if len(runArgs) < len(expectedTail) {
		t.Fatalf("run args too short: %v", runArgs)
	}
	gotTail := runArgs[len(runArgs)-len(expectedTail):]
	if !slices.Equal(gotTail, expectedTail) {
		t.Fatalf("expected sorted env flags before image arg %v, got %v (full args: %v)", expectedTail, gotTail, runArgs)
	}
}

func TestNoopRuntimeLogs(t *testing.T) {
	var rt ContainerRuntime = NoopRuntime{}
	ctx := context.Background()

	out, err := rt.Logs(ctx, "acme", "billing", "rel-1", 50)
	if err != nil {
		t.Fatalf("NoopRuntime.Logs failed: %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty noop logs, got %q", out)
	}

	if _, err := rt.Logs(ctx, "INVALID", "billing", "rel-1", 10); err == nil {
		t.Fatal("expected error for invalid orgID")
	}
	if _, err := rt.Logs(ctx, "acme", "../bad", "rel-1", 10); err == nil {
		t.Fatal("expected error for invalid appName")
	}
	if _, err := rt.Logs(ctx, "acme", "billing", "", 10); err == nil {
		t.Fatal("expected error for empty releaseID")
	}

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := rt.Logs(canceledCtx, "acme", "billing", "rel-1", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestDockerRuntimeLogs(t *testing.T) {
	var calls []recordedCommand
	rt := DockerRuntime{
		Binary: "podman",
		CommandRunner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, recordedCommand{
				name: name,
				args: append([]string(nil), args...),
			})
			return []byte("line1\nline2\n"), nil
		},
	}

	out, err := rt.Logs(context.Background(), "acme", "billing", "rel-42", 0)
	if err != nil {
		t.Fatalf("DockerRuntime.Logs default tail failed: %v", err)
	}
	if out != "line1\nline2\n" {
		t.Fatalf("unexpected logs output: %q", out)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 command, got %d", len(calls))
	}
	if calls[0].name != "podman" {
		t.Fatalf("expected binary podman, got %q", calls[0].name)
	}
	wantDefault := []string{"logs", "--tail", "100", "sc-acme-billing-rel-42"}
	if !slices.Equal(calls[0].args, wantDefault) {
		t.Fatalf("unexpected default tail args: got %v, want %v", calls[0].args, wantDefault)
	}

	out, err = rt.Logs(context.Background(), "acme", "billing", "rel-42", 25)
	if err != nil {
		t.Fatalf("DockerRuntime.Logs explicit tail failed: %v", err)
	}
	if out != "line1\nline2\n" {
		t.Fatalf("unexpected logs output: %q", out)
	}
	wantExplicit := []string{"logs", "--tail", "25", "sc-acme-billing-rel-42"}
	if !slices.Equal(calls[1].args, wantExplicit) {
		t.Fatalf("unexpected explicit tail args: got %v, want %v", calls[1].args, wantExplicit)
	}

	errRT := DockerRuntime{
		CommandRunner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("no such container"), errors.New("exit status 1")
		},
	}
	if _, err := errRT.Logs(context.Background(), "acme", "billing", "rel-42", 10); err == nil || !strings.Contains(err.Error(), "docker logs") {
		t.Fatalf("expected docker logs error, got %v", err)
	}

	called := false
	valRT := DockerRuntime{
		CommandRunner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			called = true
			return nil, nil
		},
	}
	if _, err := valRT.Logs(context.Background(), "BAD", "billing", "rel-1", 10); err == nil {
		t.Fatal("expected validation error for orgID")
	}
	if _, err := valRT.Logs(context.Background(), "acme", "billing", "", 10); err == nil {
		t.Fatal("expected validation error for releaseID")
	}
	if called {
		t.Fatal("CommandRunner should not be called when validation fails")
	}
}

func TestDockerRuntimeStartResourceLimitsAndLabels(t *testing.T) {
	tests := []struct {
		name          string
		cpuMillicores int
		memoryMB      int
		wantCPUs      string
		wantMemory    string
	}{
		{
			name:          "fractional cpu",
			cpuMillicores: 500,
			memoryMB:      256,
			wantCPUs:      "0.500",
			wantMemory:    "256m",
		},
		{
			name:          "whole cpu",
			cpuMillicores: 1000,
			memoryMB:      1024,
			wantCPUs:      "1.000",
			wantMemory:    "1024m",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var runArgs []string
			rt := DockerRuntime{
				CommandRunner: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if len(args) > 0 && args[0] == "run" {
						runArgs = append([]string(nil), args...)
					}
					return []byte("ok"), nil
				},
			}

			spec := ContainerSpec{
				OrgID:         "acme",
				AppName:       "api",
				ReleaseID:     "rel-99",
				AppDir:        "/srv/apps/api",
				Port:          19000,
				CPUMillicores: tc.cpuMillicores,
				MemoryMB:      tc.memoryMB,
			}

			if _, err := rt.Start(context.Background(), spec); err != nil {
				t.Fatalf("DockerRuntime.Start failed: %v", err)
			}

			wantImage := "smallcloud-acme-api:rel-99"
			if len(runArgs) == 0 || runArgs[len(runArgs)-1] != wantImage {
				t.Fatalf("expected image %q as last arg, got %v", wantImage, runArgs)
			}

			flagArgs := runArgs[:len(runArgs)-1]
			requiredPairs := [][2]string{
				{"--label", "smallcloud.managed=true"},
				{"--label", "smallcloud.org=acme"},
				{"--label", "smallcloud.app=api"},
				{"--label", "smallcloud.release=rel-99"},
				{"--cpus", tc.wantCPUs},
				{"--memory", tc.wantMemory},
			}

			for _, pair := range requiredPairs {
				found := false
				for i := 0; i+1 < len(flagArgs); i++ {
					if flagArgs[i] == pair[0] && flagArgs[i+1] == pair[1] {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected %s %s before image arg in %v", pair[0], pair[1], runArgs)
				}
			}
		})
	}
}
