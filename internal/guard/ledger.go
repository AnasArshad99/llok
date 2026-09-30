package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Ledger is the per-session timeline: every event and every rule outcome,
// one JSON line each, under <dir>/<project>/<timestamp>.jsonl.
type Ledger struct {
	mu   sync.Mutex
	f    *os.File
	Path string
}

type line struct {
	Event
	Rule   string `json:"rule,omitempty"`
	Action string `json:"action,omitempty"`
}

// OpenLedger creates a new ledger file for project under dir.
func OpenLedger(dir, project string) (*Ledger, error) {
	d := filepath.Join(dir, project)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(d, time.Now().Format("2006-01-02T15-04-05")+".jsonl")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Ledger{f: f, Path: p}, nil
}

// Write appends one line.
func (l *Ledger) Write(ev Event, rule, action string) {
	b, err := json.Marshal(line{Event: ev, Rule: rule, Action: action})
	if err != nil {
		return
	}
	l.mu.Lock()
	l.f.Write(append(b, '\n'))
	l.mu.Unlock()
}

// Close closes the ledger file.
func (l *Ledger) Close() error { return l.f.Close() }
