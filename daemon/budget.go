package daemon

import (
	"errors"
	"fmt"
	"sync"
)

// ErrBudgetExceeded is returned when an app reaches 100% of its max_monthly_usd cap.
var ErrBudgetExceeded = errors.New("monthly spend cap reached: circuit breaker tripped")

type BudgetStatus string

const (
	BudgetOK       BudgetStatus = "ok"
	BudgetWarning  BudgetStatus = "warning"
	BudgetHardStop BudgetStatus = "hard_stop"
)

type AppBudgetState struct {
	OrgID         string       `json:"org_id"`
	AppName       string       `json:"app_name"`
	MaxMonthlyUSD float64      `json:"max_monthly_usd"`
	AlertAtPct    float64      `json:"alert_at_pct"`
	CurrentUSD    float64      `json:"current_usd"`
	Status        BudgetStatus `json:"status"`
	Halted        bool         `json:"halted"`
}

// BudgetTracker enforces 100% hard-stop monthly spend caps per (org_id, app_name).
type BudgetTracker struct {
	mu     sync.RWMutex
	states map[string]*AppBudgetState
	onHalt func(orgID, appName string)
}

func NewBudgetTracker(onHalt func(orgID, appName string)) *BudgetTracker {
	return &BudgetTracker{
		states: make(map[string]*AppBudgetState),
		onHalt: onHalt,
	}
}

func tenantKey(orgID, appName string) string {
	return orgID + "/" + appName
}

func (b *BudgetTracker) RegisterApp(orgID, appName string, maxMonthlyUSD, alertAtPct float64) error {
	if orgID == "" || appName == "" {
		return errors.New("org_id and app_name are required")
	}
	if maxMonthlyUSD <= 0 {
		return errors.New("max_monthly_usd must be positive")
	}
	if alertAtPct <= 0 || alertAtPct >= 100 {
		alertAtPct = 80.0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	key := tenantKey(orgID, appName)
	existing, ok := b.states[key]
	if !ok {
		b.states[key] = &AppBudgetState{
			OrgID:         orgID,
			AppName:       appName,
			MaxMonthlyUSD: maxMonthlyUSD,
			AlertAtPct:    alertAtPct,
			Status:        BudgetOK,
		}
		return nil
	}

	existing.MaxMonthlyUSD = maxMonthlyUSD
	existing.AlertAtPct = alertAtPct
	b.recomputeLocked(existing)
	return nil
}

// RecordSpend adds accrued compute/egress spend and trips the hard-stop circuit breaker if >= max_monthly_usd.
func (b *BudgetTracker) RecordSpend(orgID, appName string, deltaUSD float64) (AppBudgetState, error) {
	if deltaUSD < 0 {
		return AppBudgetState{}, errors.New("deltaUSD cannot be negative")
	}

	b.mu.Lock()
	key := tenantKey(orgID, appName)
	st, ok := b.states[key]
	if !ok {
		b.mu.Unlock()
		return AppBudgetState{}, fmt.Errorf("app %q not registered for org %q", appName, orgID)
	}

	wasHalted := st.Halted
	st.CurrentUSD += deltaUSD
	b.recomputeLocked(st)
	snapshot := *st
	shouldCallHalt := !wasHalted && st.Halted && b.onHalt != nil
	b.mu.Unlock()

	if shouldCallHalt {
		b.onHalt(orgID, appName)
	}

	if snapshot.Halted {
		return snapshot, ErrBudgetExceeded
	}
	return snapshot, nil
}

// CheckAllowed returns ErrBudgetExceeded if the app's circuit breaker is tripped.
func (b *BudgetTracker) CheckAllowed(orgID, appName string) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	st, ok := b.states[tenantKey(orgID, appName)]
	if !ok {
		return fmt.Errorf("app %q not registered for org %q", appName, orgID)
	}
	if st.Halted {
		return ErrBudgetExceeded
	}
	return nil
}

func (b *BudgetTracker) State(orgID, appName string) (AppBudgetState, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	st, ok := b.states[tenantKey(orgID, appName)]
	if !ok {
		return AppBudgetState{}, false
	}
	return *st, true
}

func (b *BudgetTracker) recomputeLocked(st *AppBudgetState) {
	pct := (st.CurrentUSD / st.MaxMonthlyUSD) * 100.0
	if st.CurrentUSD >= st.MaxMonthlyUSD || pct >= 100.0 {
		st.Status = BudgetHardStop
		st.Halted = true
	} else if pct >= st.AlertAtPct {
		st.Status = BudgetWarning
		st.Halted = false
	} else {
		st.Status = BudgetOK
		st.Halted = false
	}
}
