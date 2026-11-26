package daemon

import (
	"sync"
	"time"
)

const defaultAuditMaxEvents = 200

type AuditEvent struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	OrgID     string    `json:"org_id"`
	AppName   string    `json:"app_name"`
	ReleaseID string    `json:"release_id,omitempty"`
	Detail    string    `json:"detail,omitempty"`
}

type AuditLog struct {
	mu        sync.RWMutex
	maxEvents int
	nextID    int64
	events    []AuditEvent
}

func NewAuditLog(maxEvents int) *AuditLog {
	if maxEvents <= 0 {
		maxEvents = defaultAuditMaxEvents
	}
	return &AuditLog{
		maxEvents: maxEvents,
		nextID:    1,
	}
}

func (a *AuditLog) Record(event AuditEvent) AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()

	if event.ID <= 0 {
		event.ID = a.nextID
		a.nextID++
	} else if event.ID >= a.nextID {
		a.nextID = event.ID + 1
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	if len(a.events) == a.maxEvents {
		copy(a.events, a.events[1:])
		a.events[len(a.events)-1] = event
	} else {
		a.events = append(a.events, event)
	}
	return event
}

func (a *AuditLog) List(orgID, appName string, limit int) []AuditEvent {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var out []AuditEvent
	for i := len(a.events) - 1; i >= 0; i-- {
		ev := a.events[i]
		if orgID != "" && ev.OrgID != orgID {
			continue
		}
		if appName != "" && ev.AppName != appName {
			continue
		}
		out = append(out, ev)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if out == nil {
		return []AuditEvent{}
	}
	return out
}

func (a *AuditLog) Snapshot() []AuditEvent {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if len(a.events) == 0 {
		return nil
	}
	out := make([]AuditEvent, len(a.events))
	copy(out, a.events)
	return out
}

func (a *AuditLog) Restore(events []AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(events) > a.maxEvents {
		events = events[len(events)-a.maxEvents:]
	}
	a.events = make([]AuditEvent, len(events))
	copy(a.events, events)

	var maxID int64
	for _, ev := range a.events {
		if ev.ID > maxID {
			maxID = ev.ID
		}
	}
	a.nextID = maxID + 1
}
