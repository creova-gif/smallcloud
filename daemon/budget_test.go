package daemon

import (
	"sync"
	"testing"
	"time"
)

func TestBudgetRegisterInitializesBillingMonth(t *testing.T) {
	bt := NewBudgetTracker(nil)
	if err := bt.RegisterApp("org-alpha", "app-one", 10.0, 80.0); err != nil {
		t.Fatalf("RegisterApp failed: %v", err)
	}
	st, ok := bt.State("org-alpha", "app-one")
	if !ok {
		t.Fatalf("expected state to exist")
	}
	wantMonth := time.Now().UTC().Format("2006-01")
	if st.BillingMonth != wantMonth {
		t.Fatalf("expected BillingMonth %q, got %q", wantMonth, st.BillingMonth)
	}
	if len(st.History) != 0 {
		t.Fatalf("expected empty History, got %d items", len(st.History))
	}
}

func TestBudgetRolloverIfNewMonthSameMonthNoop(t *testing.T) {
	unhaltCalls := 0
	bt := NewBudgetTrackerWithCallbacks(nil, func(orgID, appName string) {
		unhaltCalls++
	})
	if err := bt.RegisterApp("org-alpha", "app-one", 10.0, 80.0); err != nil {
		t.Fatalf("RegisterApp failed: %v", err)
	}
	if _, err := bt.RecordSpend("org-alpha", "app-one", 3.5); err != nil {
		t.Fatalf("RecordSpend failed: %v", err)
	}

	now := time.Now().UTC()
	st, rolled, err := bt.RolloverIfNewMonth("org-alpha", "app-one", now)
	if err != nil {
		t.Fatalf("RolloverIfNewMonth failed: %v", err)
	}
	if rolled {
		t.Fatalf("expected rolled=false in same month")
	}
	if st.CurrentUSD != 3.5 {
		t.Fatalf("expected CurrentUSD=3.5, got %v", st.CurrentUSD)
	}
	if len(st.History) != 0 {
		t.Fatalf("expected empty History, got %d", len(st.History))
	}
	if unhaltCalls != 0 {
		t.Fatalf("expected 0 unhaltCalls, got %d", unhaltCalls)
	}
}

func TestBudgetRolloverIfNewMonthArchivesResetsAndUnhalts(t *testing.T) {
	var mu sync.Mutex
	var halted, unhalted []string
	bt := NewBudgetTrackerWithCallbacks(
		func(orgID, appName string) {
			mu.Lock()
			halted = append(halted, orgID+"/"+appName)
			mu.Unlock()
		},
		func(orgID, appName string) {
			mu.Lock()
			unhalted = append(unhalted, orgID+"/"+appName)
			mu.Unlock()
		},
	)

	if err := bt.RegisterApp("org-alpha", "app-one", 10.0, 80.0); err != nil {
		t.Fatalf("RegisterApp failed: %v", err)
	}
	initialState, _ := bt.State("org-alpha", "app-one")
	prevMonth := initialState.BillingMonth

	if _, err := bt.RecordSpend("org-alpha", "app-one", 10.0); err != ErrBudgetExceeded {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}
	if err := bt.CheckAllowed("org-alpha", "app-one"); err != ErrBudgetExceeded {
		t.Fatalf("expected CheckAllowed ErrBudgetExceeded, got %v", err)
	}

	nextMonthTime := time.Date(2099, 12, 1, 0, 0, 0, 0, time.UTC)
	st, rolled, err := bt.RolloverIfNewMonth("org-alpha", "app-one", nextMonthTime)
	if err != nil {
		t.Fatalf("RolloverIfNewMonth failed: %v", err)
	}
	if !rolled {
		t.Fatalf("expected rolled=true")
	}
	if st.BillingMonth != "2099-12" {
		t.Fatalf("expected BillingMonth 2099-12, got %q", st.BillingMonth)
	}
	if st.CurrentUSD != 0 {
		t.Fatalf("expected CurrentUSD=0, got %v", st.CurrentUSD)
	}
	if st.Status != BudgetOK || st.Halted {
		t.Fatalf("expected BudgetOK and Halted=false, got status=%s halted=%v", st.Status, st.Halted)
	}
	if len(st.History) != 1 {
		t.Fatalf("expected 1 archive entry, got %d", len(st.History))
	}
	arch := st.History[0]
	if arch.BillingMonth != prevMonth || arch.TotalUSD != 10.0 || arch.MaxMonthlyUSD != 10.0 || !arch.ArchivedAt.Equal(nextMonthTime) {
		t.Fatalf("unexpected archive entry: %+v", arch)
	}
	if err := bt.CheckAllowed("org-alpha", "app-one"); err != nil {
		t.Fatalf("expected CheckAllowed nil after rollover, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(halted) != 1 || len(unhalted) != 1 || unhalted[0] != "org-alpha/app-one" {
		t.Fatalf("unexpected callbacks halted=%v unhalted=%v", halted, unhalted)
	}
}

func TestBudgetRolloverIfNewMonthErrors(t *testing.T) {
	bt := NewBudgetTracker(nil)
	if _, _, err := bt.RolloverIfNewMonth("BAD_ORG", "app", time.Now()); err == nil {
		t.Fatalf("expected validation error for invalid org")
	}
	if _, _, err := bt.RolloverIfNewMonth("org-alpha", "missing-app", time.Now()); err == nil {
		t.Fatalf("expected not-registered error")
	}
}

func TestBudgetRolloverAll(t *testing.T) {
	var mu sync.Mutex
	var unhalted []string
	bt := NewBudgetTrackerWithCallbacks(nil, func(orgID, appName string) {
		mu.Lock()
		unhalted = append(unhalted, orgID+"/"+appName)
		mu.Unlock()
	})

	if err := bt.RegisterApp("org-a", "app-1", 10.0, 80.0); err != nil {
		t.Fatalf("RegisterApp 1 failed: %v", err)
	}
	if err := bt.RegisterApp("org-a", "app-2", 20.0, 80.0); err != nil {
		t.Fatalf("RegisterApp 2 failed: %v", err)
	}

	if _, err := bt.RecordSpend("org-a", "app-1", 12.0); err != ErrBudgetExceeded {
		t.Fatalf("expected ErrBudgetExceeded for app-1, got %v", err)
	}
	if _, err := bt.RecordSpend("org-a", "app-2", 5.0); err != nil {
		t.Fatalf("RecordSpend app-2 failed: %v", err)
	}

	nextMonth := time.Date(2099, 5, 1, 2, 0, 0, 0, time.FixedZone("UTC+5", 5*3600))
	count := bt.RolloverAll(nextMonth)
	if count != 2 {
		t.Fatalf("expected 2 apps rolled over, got %d", count)
	}

	if again := bt.RolloverAll(nextMonth); again != 0 {
		t.Fatalf("expected 0 apps rolled over on repeat call, got %d", again)
	}

	st1, _ := bt.State("org-a", "app-1")
	st2, _ := bt.State("org-a", "app-2")
	if st1.BillingMonth != "2099-04" || st2.BillingMonth != "2099-04" {
		t.Fatalf("expected UTC month 2099-04, got %q and %q", st1.BillingMonth, st2.BillingMonth)
	}
	if st1.Halted || st1.Status != BudgetOK || st1.CurrentUSD != 0 {
		t.Fatalf("unexpected st1 after rollover: %+v", st1)
	}
	if st2.Halted || st2.Status != BudgetOK || st2.CurrentUSD != 0 {
		t.Fatalf("unexpected st2 after rollover: %+v", st2)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(unhalted) != 1 || unhalted[0] != "org-a/app-1" {
		t.Fatalf("expected only app-1 unhalted, got %v", unhalted)
	}
}

func TestBudgetAlertCallbackFiresOnceAndResetsOnRollover(t *testing.T) {
	var mu sync.Mutex
	var alerts []AppBudgetState

	bt := NewBudgetTracker(nil)
	bt.SetAlertCallback(func(orgID, appName string, state AppBudgetState) {
		mu.Lock()
		alerts = append(alerts, state)
		mu.Unlock()
	})

	if err := bt.RegisterApp("org-alpha", "app-one", 10.0, 80.0); err != nil {
		t.Fatalf("RegisterApp failed: %v", err)
	}

	st, err := bt.RecordSpend("org-alpha", "app-one", 5.0)
	if err != nil {
		t.Fatalf("RecordSpend below threshold failed: %v", err)
	}
	if st.AlertSent {
		t.Fatalf("expected AlertSent=false below threshold")
	}

	st, err = bt.RecordSpend("org-alpha", "app-one", 3.0)
	if err != nil {
		t.Fatalf("RecordSpend at threshold failed: %v", err)
	}
	if !st.AlertSent {
		t.Fatalf("expected AlertSent=true at threshold")
	}

	st, err = bt.RecordSpend("org-alpha", "app-one", 1.0)
	if err != nil {
		t.Fatalf("RecordSpend above threshold failed: %v", err)
	}
	if !st.AlertSent {
		t.Fatalf("expected AlertSent=true above threshold")
	}

	mu.Lock()
	if len(alerts) != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 alert before rollover, got %d", len(alerts))
	}
	if alerts[0].CurrentUSD != 8.0 || !alerts[0].AlertSent {
		mu.Unlock()
		t.Fatalf("unexpected first alert state: %+v", alerts[0])
	}
	mu.Unlock()

	nextMonth := time.Date(2099, 12, 1, 0, 0, 0, 0, time.UTC)
	st, rolled, err := bt.RolloverIfNewMonth("org-alpha", "app-one", nextMonth)
	if err != nil {
		t.Fatalf("RolloverIfNewMonth failed: %v", err)
	}
	if !rolled {
		t.Fatalf("expected rolled=true")
	}
	if st.AlertSent {
		t.Fatalf("expected AlertSent=false after rollover")
	}

	st, err = bt.RecordSpend("org-alpha", "app-one", 9.0)
	if err != nil {
		t.Fatalf("RecordSpend after rollover failed: %v", err)
	}
	if !st.AlertSent {
		t.Fatalf("expected AlertSent=true after crossing threshold in new month")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts total after rollover, got %d", len(alerts))
	}
	if alerts[1].CurrentUSD != 9.0 || !alerts[1].AlertSent {
		t.Fatalf("unexpected second alert state: %+v", alerts[1])
	}
}
