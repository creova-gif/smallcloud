package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/creova-gif/smallcloud/pkg/core"
)

type CanaryDecision struct {
	OrgID           string `json:"org_id"`
	AppName         string `json:"app_name"`
	StandbyUpstream string `json:"standby_upstream"`
	Healthy         bool   `json:"healthy"`
	Action          string `json:"action"`
	Error           string `json:"error,omitempty"`
}

func EvaluateAndReconcileCanary(ctx context.Context, d *Daemon, orgID, appName, healthPath string, interval, timeout time.Duration) (CanaryDecision, error) {
	if d == nil {
		return CanaryDecision{}, errors.New("daemon is required")
	}

	route, ok := d.Router().GetRoute(orgID, appName)
	if !ok {
		return CanaryDecision{}, fmt.Errorf("%w for %s/%s", ErrNoRouteFound, orgID, appName)
	}

	if route.CanaryWeight <= 0 || route.StandbyUpstream == "" {
		return CanaryDecision{
			OrgID:           route.OrgID,
			AppName:         route.AppName,
			StandbyUpstream: route.StandbyUpstream,
			Action:          "noop",
		}, nil
	}

	probeErr := core.ProbeHTTPReady(ctx, "http://"+route.StandbyUpstream, healthPath, interval, timeout)
	if probeErr == nil {
		promoted, err := d.Router().SwapRoute(route.OrgID, route.AppName, route.Domain, route.StandbyReleaseID, route.StandbyUpstream)
		if err != nil {
			return CanaryDecision{}, err
		}

		d.Audit().Record(AuditEvent{
			Type:      "canary_promoted",
			OrgID:     route.OrgID,
			AppName:   route.AppName,
			ReleaseID: promoted.ActiveReleaseID,
			Detail:    fmt.Sprintf("upstream=%s", route.StandbyUpstream),
		})

		return CanaryDecision{
			OrgID:           route.OrgID,
			AppName:         route.AppName,
			StandbyUpstream: route.StandbyUpstream,
			Healthy:         true,
			Action:          "promoted",
		}, nil
	}

	if _, err := d.Router().SetCanaryWeight(orgID, appName, 0); err != nil {
		return CanaryDecision{}, err
	}

	d.Audit().Record(AuditEvent{
		Type:      "canary_rollback",
		OrgID:     route.OrgID,
		AppName:   route.AppName,
		ReleaseID: route.StandbyReleaseID,
		Detail:    probeErr.Error(),
	})

	return CanaryDecision{
		OrgID:           route.OrgID,
		AppName:         route.AppName,
		StandbyUpstream: route.StandbyUpstream,
		Healthy:         false,
		Action:          "rolled_back",
		Error:           probeErr.Error(),
	}, nil
}
