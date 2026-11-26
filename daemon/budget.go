package daemon

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/creova-gif/smallcloud/pkg/config"
)

var ErrBudgetExceeded = errors.New("monthly spend cap reached: circuit breaker tripped")

type BudgetStatus string

const (
	BudgetOK       BudgetStatus = "ok"
	BudgetWarning  BudgetStatus = "warning"
	BudgetHardStop BudgetStatus = "hard_stop"
	BudgetResumed  BudgetStatus = "resumed"
)

type BudgetEvent struct {
	Timestamp     time.Time    `json:"timestamp"`
	Status        BudgetStatus `json:"status"`
	CurrentUSD    float64      `json:"current_usd"`
	MaxMonthlyUSD float64      `json:"max_monthly_usd"`
}

type AppBudgetState struct {
	OrgID         string        `json:"org_id"`
	AppName       string        `json:"app_name"`
	MaxMonthlyUSD float64       `json:"max_monthly_usd"`
	AlertAtPct    float64       `json:"alert_at_pct"`
	CurrentUSD    float64       `json:"current_usd"`
	Status        BudgetStatus  `json:"status"`
	Halted        bool          `json:"halted"`
	Events        []BudgetEvent `json:"events,omitempty"`
}

type BudgetTracker struct {
	mu       sync.RWMutex
	states   map[string]*AppBudgetState
	onHalt   func(orgID, appName string)
	onUnhalt func(orgID, appName string)
}

func NewBudgetTracker(onHalt func(orgID, appName string)) *BudgetTracker {
	return NewBudgetTrackerWithCallbacks(onHalt, nil)
}

func NewBudgetTrackerWithCallbacks(onHalt, onUnhalt func(orgID, appName string)) *BudgetTracker {
	return &BudgetTracker{
		states:   make(map[string]*AppBudgetState),
		onHalt:   onHalt,
		onUnhalt: onUnhalt,
	}
}

func tenantKey(orgID, appName string) string {
	return orgID + "/" + appName
}

func validateTenantSlugs(orgID, appName string) error {
	if !config.IsValidSlug(orgID) || !config.IsValidSlug(appName) {
		return errors.New("org_id and app_name must be valid lowercase alphanumeric slugs")
	}
	return nil
}

func (b *BudgetTracker) RegisterApp(orgID, appName string, maxMonthlyUSD, alertAtPct float64) error {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return err
	}
	if math.IsNaN(maxMonthlyUSD) || math.IsInf(maxMonthlyUSD, 0) || maxMonthlyUSD <= 0 {
		return errors.New("max_monthly_usd must be a finite positive number")
	}
	if math.IsNaN(alertAtPct) || math.IsInf(alertAtPct, 0) || alertAtPct <= 0 || alertAtPct >= 100 {
		alertAtPct = 80.0
	}

	b.mu.Lock()
	key := tenantKey(orgID, appName)
	existing, ok := b.states[key]
	if !ok {
		b.states[key] = &AppBudgetState{
			OrgID:         orgID,
			AppName:       appName,
			MaxMonthlyUSD: maxMonthlyUSD,
			AlertAtPct:    alertAtPct,
			Status:        BudgetOK,
			Events: []BudgetEvent{{
				Timestamp:     time.Now().UTC(),
				Status:        BudgetOK,
				CurrentUSD:    0,
				MaxMonthlyUSD: maxMonthlyUSD,
			}},
		}
		b.mu.Unlock()
		return nil
	}

	wasHalted := existing.Halted
	existing.MaxMonthlyUSD = maxMonthlyUSD
	existing.AlertAtPct = alertAtPct
	b.recomputeLocked(existing, wasHalted)
	shouldUnhalt := wasHalted && !existing.Halted && b.onUnhalt != nil
	b.mu.Unlock()

	if shouldUnhalt {
		b.onUnhalt(orgID, appName)
	}
	return nil
}

func (b *BudgetTracker) SetMaxBudget(orgID, appName string, maxMonthlyUSD float64) (AppBudgetState, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return AppBudgetState{}, err
	}
	if math.IsNaN(maxMonthlyUSD) || math.IsInf(maxMonthlyUSD, 0) || maxMonthlyUSD <= 0 {
		return AppBudgetState{}, errors.New("max_monthly_usd must be a finite positive number")
	}

	b.mu.Lock()
	st, ok := b.states[tenantKey(orgID, appName)]
	if !ok {
		b.mu.Unlock()
		return AppBudgetState{}, fmt.Errorf("app %q not registered for org %q", appName, orgID)
	}
	wasHalted := st.Halted
	st.MaxMonthlyUSD = maxMonthlyUSD
	b.recomputeLocked(st, wasHalted)
	snapshot := cloneBudgetState(st)
	shouldHalt := !wasHalted && st.Halted && b.onHalt != nil
	shouldUnhalt := wasHalted && !st.Halted && b.onUnhalt != nil
	b.mu.Unlock()

	if shouldHalt {
		b.onHalt(orgID, appName)
	} else if shouldUnhalt {
		b.onUnhalt(orgID, appName)
	}
	return snapshot, nil
}

func (b *BudgetTracker) RecordSpend(orgID, appName string, deltaUSD float64) (AppBudgetState, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return AppBudgetState{}, err
	}
	if math.IsNaN(deltaUSD) || math.IsInf(deltaUSD, 0) || deltaUSD < 0 {
		return AppBudgetState{}, errors.New("deltaUSD must be a finite non-negative number")
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
	b.recomputeLocked(st, wasHalted)
	snapshot := cloneBudgetState(st)
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

func (b *BudgetTracker) CheckAllowed(orgID, appName string) error {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return err
	}
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
	return cloneBudgetState(st), true
}

func (b *BudgetTracker) SnapshotStates() map[string]*AppBudgetState {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make(map[string]*AppBudgetState, len(b.states))
	for k, v := range b.states {
		cp := cloneBudgetState(v)
		out[k] = &cp
	}
	return out
}

func (b *BudgetTracker) RestoreStates(states map[string]*AppBudgetState) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.states = make(map[string]*AppBudgetState, len(states))
	for k, v := range states {
		if v == nil {
			continue
		}
		cp := cloneBudgetState(v)
		b.states[k] = &cp
	}
}

func cloneBudgetState(st *AppBudgetState) AppBudgetState {
	cp := *st
	if len(st.Events) > 0 {
		cp.Events = append([]BudgetEvent(nil), st.Events...)
	}
	return cp
}

func (b *BudgetTracker) recomputeLocked(st *AppBudgetState, wasHalted bool) {
	prevStatus := st.Status
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

	if wasHalted && !st.Halted {
		st.Events = append(st.Events, BudgetEvent{
			Timestamp:     time.Now().UTC(),
			Status:        BudgetResumed,
			CurrentUSD:    st.CurrentUSD,
			MaxMonthlyUSD: st.MaxMonthlyUSD,
		})
	} else if st.Status != prevStatus {
		st.Events = append(st.Events, BudgetEvent{
			Timestamp:     time.Now().UTC(),
			Status:        st.Status,
			CurrentUSD:    st.CurrentUSD,
			MaxMonthlyUSD: st.MaxMonthlyUSD,
		})
	}
}
