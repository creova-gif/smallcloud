package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
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
		"--label smallcloud.org=acme",
		"--label smallcloud.app=billing",
		"--label smallcloud.release=rel-42",
		"--cpus 0.75",
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
