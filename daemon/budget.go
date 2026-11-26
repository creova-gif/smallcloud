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

type MonthlyArchive struct {
	BillingMonth  string    `json:"billing_month"`
	TotalUSD      float64   `json:"total_usd"`
	MaxMonthlyUSD float64   `json:"max_monthly_usd"`
	ArchivedAt    time.Time `json:"archived_at"`
}

type AppBudgetState struct {
	OrgID         string           `json:"org_id"`
	AppName       string           `json:"app_name"`
	MaxMonthlyUSD float64          `json:"max_monthly_usd"`
	AlertAtPct    float64          `json:"alert_at_pct"`
	CurrentUSD    float64          `json:"current_usd"`
	BillingMonth  string           `json:"billing_month"`
	Status        BudgetStatus     `json:"status"`
	Halted        bool             `json:"halted"`
	AlertSent     bool             `json:"alert_sent,omitempty"`
	Events        []BudgetEvent    `json:"events,omitempty"`
	History       []MonthlyArchive `json:"history,omitempty"`
}

type BudgetTracker struct {
	mu       sync.RWMutex
	states   map[string]*AppBudgetState
	onHalt   func(orgID, appName string)
	onUnhalt func(orgID, appName string)
	onAlert  func(orgID, appName string, state AppBudgetState)
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

func (b *BudgetTracker) SetAlertCallback(fn func(orgID, appName string, state AppBudgetState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.onAlert = fn
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
		now := time.Now().UTC()
		b.states[key] = &AppBudgetState{
			OrgID:         orgID,
			AppName:       appName,
			MaxMonthlyUSD: maxMonthlyUSD,
			AlertAtPct:    alertAtPct,
			BillingMonth:  now.Format("2006-01"),
			Status:        BudgetOK,
			Events: []BudgetEvent{{
				Timestamp:     now,
				Status:        BudgetOK,
				CurrentUSD:    0,
				MaxMonthlyUSD: maxMonthlyUSD,
			}},
		}
		b.mu.Unlock()
		return nil
	}

	wasHalted := existing.Halted
	wasAlertSent := existing.AlertSent
	if existing.BillingMonth == "" {
		existing.BillingMonth = time.Now().UTC().Format("2006-01")
	}
	existing.MaxMonthlyUSD = maxMonthlyUSD
	existing.AlertAtPct = alertAtPct
	b.recomputeLocked(existing, wasHalted)
	shouldUnhalt := wasHalted && !existing.Halted && b.onUnhalt != nil
	shouldAlert := !wasAlertSent && existing.AlertSent && b.onAlert != nil
	var alertSnapshot AppBudgetState
	if shouldAlert {
		alertSnapshot = cloneBudgetState(existing)
	}
	b.mu.Unlock()

	if shouldUnhalt {
		b.onUnhalt(orgID, appName)
	}
	if shouldAlert {
		b.onAlert(orgID, appName, alertSnapshot)
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
	wasAlertSent := st.AlertSent
	st.MaxMonthlyUSD = maxMonthlyUSD
	b.recomputeLocked(st, wasHalted)
	snapshot := cloneBudgetState(st)
	shouldHalt := !wasHalted && st.Halted && b.onHalt != nil
	shouldUnhalt := wasHalted && !st.Halted && b.onUnhalt != nil
	shouldAlert := !wasAlertSent && st.AlertSent && b.onAlert != nil
	b.mu.Unlock()

	if shouldHalt {
		b.onHalt(orgID, appName)
	} else if shouldUnhalt {
		b.onUnhalt(orgID, appName)
	}
	if shouldAlert {
		b.onAlert(orgID, appName, snapshot)
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
	wasAlertSent := st.AlertSent
	st.CurrentUSD += deltaUSD
	b.recomputeLocked(st, wasHalted)
	snapshot := cloneBudgetState(st)
	shouldCallHalt := !wasHalted && st.Halted && b.onHalt != nil
	shouldAlert := !wasAlertSent && st.AlertSent && b.onAlert != nil
	b.mu.Unlock()

	if shouldCallHalt {
		b.onHalt(orgID, appName)
	}
	if shouldAlert {
		b.onAlert(orgID, appName, snapshot)
	}

	if snapshot.Halted {
		return snapshot, ErrBudgetExceeded
	}
	return snapshot, nil
}

type unhaltTarget struct {
	orgID   string
	appName string
}

func (b *BudgetTracker) RolloverIfNewMonth(orgID, appName string, now time.Time) (AppBudgetState, bool, error) {
	if err := validateTenantSlugs(orgID, appName); err != nil {
		return AppBudgetState{}, false, err
	}

	nowUTC := now.UTC()
	targetMonth := nowUTC.Format("2006-01")

	b.mu.Lock()
	st, ok := b.states[tenantKey(orgID, appName)]
	if !ok {
		b.mu.Unlock()
		return AppBudgetState{}, false, fmt.Errorf("app %q not registered for org %q", appName, orgID)
	}

	if st.BillingMonth == targetMonth {
		snapshot := cloneBudgetState(st)
		b.mu.Unlock()
		return snapshot, false, nil
	}

	shouldUnhalt := b.rolloverLocked(st, targetMonth, nowUTC)
	snapshot := cloneBudgetState(st)
	b.mu.Unlock()

	if shouldUnhalt {
		b.onUnhalt(orgID, appName)
	}
	return snapshot, true, nil
}

func (b *BudgetTracker) RolloverAll(now time.Time) int {
	nowUTC := now.UTC()
	targetMonth := nowUTC.Format("2006-01")

	b.mu.Lock()
	rolledOver := 0
	var toUnhalt []unhaltTarget
	for _, st := range b.states {
		if st.BillingMonth == targetMonth {
			continue
		}
		rolledOver++
		if b.rolloverLocked(st, targetMonth, nowUTC) {
			toUnhalt = append(toUnhalt, unhaltTarget{orgID: st.OrgID, appName: st.AppName})
		}
	}
	b.mu.Unlock()

	for _, target := range toUnhalt {
		b.onUnhalt(target.orgID, target.appName)
	}
	return rolledOver
}

func (b *BudgetTracker) rolloverLocked(st *AppBudgetState, targetMonth string, archivedAt time.Time) bool {
	wasHalted := st.Halted
	st.History = append(st.History, MonthlyArchive{
		BillingMonth:  st.BillingMonth,
		TotalUSD:      st.CurrentUSD,
		MaxMonthlyUSD: st.MaxMonthlyUSD,
		ArchivedAt:    archivedAt,
	})
	st.CurrentUSD = 0
	st.AlertSent = false
	st.BillingMonth = targetMonth
	b.recomputeLocked(st, wasHalted)
	return wasHalted && !st.Halted && b.onUnhalt != nil
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
	if len(st.History) > 0 {
		cp.History = append([]MonthlyArchive(nil), st.History...)
	}
	return cp
}

func (b *BudgetTracker) recomputeLocked(st *AppBudgetState, wasHalted bool) {
	prevStatus := st.Status
	pct := (st.CurrentUSD / st.MaxMonthlyUSD) * 100.0
	if pct >= st.AlertAtPct && !st.AlertSent {
		st.AlertSent = true
	}
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
