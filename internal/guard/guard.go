// Package guard is llok's supervisor: deterministic rules evaluated on the
// host over what the host can see for itself, which files in the repo the
// guest changed and which hosts the broker was asked for. It never inspects
// request contents or what runs inside the guest. Actions are limited to
// notifying, pausing the VM for a decision, or stopping it.
package guard

import (
	"fmt"
	"path"
	"strings"
	"sync"
	"time"
)

// Kind is the sensor an event came from.
type Kind string

const (
	KindFile Kind = "file" // a file in the repo changed (host-side watcher)
	KindNet  Kind = "net"  // the broker decided on a destination
)

// Event is one observation. Metadata only.
type Event struct {
	Time     time.Time `json:"time"`
	Kind     Kind      `json:"kind"`
	Path     string    `json:"path,omitempty"`     // file: relative to the repo
	Op       string    `json:"op,omitempty"`       // file: create | write | delete | rename | storm
	Host     string    `json:"host,omitempty"`     // net: destination host
	Decision string    `json:"decision,omitempty"` // net: allow | deny
}

// Action is what a matching rule does.
type Action string

const (
	ActNotify Action = "notify" // panel line + ledger
	ActPause  Action = "pause"  // freeze the VM and ask: resume or kill
	ActKill   Action = "kill"   // stop the VM
	ActOff    Action = "off"    // disables a default rule of the same name
)

// Burst fires when at least Count matching events happen within Seconds.
type Burst struct {
	Count   int `yaml:"count" json:"count"`
	Seconds int `yaml:"seconds" json:"seconds"`
}

// Rule is one "when this, then that". Path and Host are globs in the
// path.Match sense; a trailing "/**" matches a whole subtree.
type Rule struct {
	Name    string `yaml:"name" json:"name"`
	On      Kind   `yaml:"on" json:"on"`
	Path    string `yaml:"path,omitempty" json:"path,omitempty"`
	Op      string `yaml:"op,omitempty" json:"op,omitempty"`
	Host    string `yaml:"host,omitempty" json:"host,omitempty"`
	Burst   *Burst `yaml:"burst,omitempty" json:"burst,omitempty"`
	Action  Action `yaml:"action" json:"action"`
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
}

// Defaults are the rules every session gets unless llok.yaml changes them.
func Defaults() []Rule {
	return []Rule{
		{Name: "git-hooks", On: KindFile, Path: ".git/hooks/*", Action: ActPause,
			Message: "a git hook was written; hooks run on your Mac at the next git command"},
		{Name: "ci-config", On: KindFile, Path: ".github/workflows/*", Action: ActNotify,
			Message: "CI workflow changed"},
		{Name: "mass-delete", On: KindFile, Op: "delete", Burst: &Burst{Count: 50, Seconds: 10}, Action: ActPause,
			Message: "many files deleted in a short time"},
		// macOS reports a burst too large to list as one notice; the
		// individual events are gone, so treat the notice itself as a burst.
		{Name: "event-storm", On: KindFile, Op: "storm", Action: ActPause,
			Message: "too many file changes at once for macOS to list them individually"},
		// The policy file itself: read-only in the guest, and any write that
		// slips through (or creates one where none existed) stops the session.
		{Name: "policy-file", On: KindFile, Path: "llok.yaml", Action: ActPause,
			Message: "llok.yaml was changed from inside the VM"},
		// Files git itself executes or consults on your Mac at the next
		// git command: hooksPath, fsmonitor, filters, external diff/merge.
		{Name: "git-config", On: KindFile, Path: ".git/config", Action: ActPause,
			Message: ".git/config was changed; it can point git at commands that run on your Mac"},
		{Name: "git-attributes", On: KindFile, Path: ".gitattributes", Action: ActNotify,
			Message: ".gitattributes changed (filters, diff drivers)"},
		{Name: "git-info", On: KindFile, Path: ".git/info/*", Action: ActNotify,
			Message: ".git/info changed (attributes, excludes)"},
		// Tools that run repo files automatically when you open the folder.
		{Name: "direnv", On: KindFile, Path: ".envrc", Action: ActPause,
			Message: ".envrc was written; direnv runs it when you cd into the repo"},
		{Name: "pre-commit", On: KindFile, Path: ".pre-commit-config.yaml", Action: ActPause,
			Message: ".pre-commit-config.yaml was written; git runs its hooks on your Mac at the next commit"},
		{Name: "editor-tasks", On: KindFile, Path: ".vscode/*", Action: ActNotify,
			Message: "VS Code settings/tasks changed (can auto-run commands)"},
		{Name: "idea-config", On: KindFile, Path: ".idea/*", Action: ActNotify,
			Message: "JetBrains project config changed"},
	}
}

// Merge applies overrides onto base: same name replaces, "off" removes,
// new names are appended.
func Merge(base, overrides []Rule) []Rule {
	out := append([]Rule(nil), base...)
	for _, o := range overrides {
		found := false
		for i := range out {
			if out[i].Name == o.Name {
				found = true
				if o.Action == ActOff {
					out = append(out[:i], out[i+1:]...)
				} else {
					out[i] = o
				}
				break
			}
		}
		if !found && o.Action != ActOff {
			out = append(out, o)
		}
	}
	return out
}

// Validate reports the first problem in a rule set.
func Validate(rs []Rule) error {
	for _, r := range rs {
		if r.Name == "" {
			return fmt.Errorf("rule without a name")
		}
		if r.On != KindFile && r.On != KindNet {
			return fmt.Errorf("rule %q: on must be file or net", r.Name)
		}
		switch r.Action {
		case ActNotify, ActPause, ActKill, ActOff:
		default:
			return fmt.Errorf("rule %q: action must be notify, pause, kill or off", r.Name)
		}
		if r.Burst != nil && (r.Burst.Count < 2 || r.Burst.Seconds < 1) {
			return fmt.Errorf("rule %q: burst needs count >= 2 and seconds >= 1", r.Name)
		}
	}
	return nil
}

// Machine is what the engine may do to the VM.
type Machine interface {
	Pause() error
	Resume() error
	Stop() error
}

// Option is one answer a prompt offers.
type Option struct {
	Key   byte
	Label string
}

// Engine evaluates rules over events. Matching is indexed so cost does not
// grow with the number of rules: exact paths and hosts go in maps, globs in
// short per-kind lists, and burst state is a bounded ring per rule.
type Engine struct {
	// Notify adds a line to the panel. Ask blocks for one of the option keys;
	// nil means no one is watching, in which case pause becomes kill.
	Notify func(string)
	Ask    func(question string, opts []Option) byte
	// Machine may be set once the VM is running.
	Machine func() Machine
	Ledger  *Ledger

	rules   []Rule
	exact   map[Kind]map[string][]int // kind -> exact path/host -> rule indexes
	globs   map[Kind][]int            // kind -> rule indexes with glob patterns
	any     map[Kind][]int            // kind -> rule indexes with no path/host
	mu      sync.Mutex
	bursts  map[int][]time.Time
	paused  bool
	stopped string // name/reason of the rule that stopped the VM, if any
}

// StoppedBy returns the reason the engine stopped the VM, or "".
func (e *Engine) StoppedBy() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopped
}

func (e *Engine) markStopped(reason string) {
	e.mu.Lock()
	if e.stopped == "" {
		e.stopped = reason
	}
	e.mu.Unlock()
}

// New builds an engine over rs.
func New(rs []Rule) *Engine {
	e := &Engine{
		rules:  rs,
		exact:  map[Kind]map[string][]int{},
		globs:  map[Kind][]int{},
		any:    map[Kind][]int{},
		bursts: map[int][]time.Time{},
	}
	for i, r := range rs {
		key := r.Path
		if r.On == KindNet {
			key = r.Host
		}
		switch {
		case key == "":
			e.any[r.On] = append(e.any[r.On], i)
		case strings.ContainsAny(key, "*?["):
			e.globs[r.On] = append(e.globs[r.On], i)
		default:
			if e.exact[r.On] == nil {
				e.exact[r.On] = map[string][]int{}
			}
			e.exact[r.On][key] = append(e.exact[r.On][key], i)
		}
	}
	return e
}

// Rules returns the active rule set.
func (e *Engine) Rules() []Rule { return e.rules }

// Observe records an event and runs any matching rules. Actions that need
// a decision run asynchronously so sensors never block.
func (e *Engine) Observe(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	e.record(ev, "", "")
	for _, i := range e.candidates(ev) {
		r := e.rules[i]
		if !e.hit(i, r, ev) {
			continue
		}
		msg := r.Message
		if ev.Kind == KindFile {
			msg += " (" + ev.Op + " " + ev.Path + ")"
		} else {
			msg += " (" + ev.Host + ")"
		}
		switch r.Action {
		case ActNotify:
			e.record(ev, r.Name, "notify")
			e.notify("⚠ " + msg)
		case ActPause:
			e.record(ev, r.Name, "pause")
			go e.pause(msg)
		case ActKill:
			e.record(ev, r.Name, "kill")
			go e.kill(msg)
		}
	}
}

// candidates uses the indexes to find rules worth evaluating.
func (e *Engine) candidates(ev Event) []int {
	key := ev.Path
	if ev.Kind == KindNet {
		key = ev.Host
	}
	var out []int
	if m := e.exact[ev.Kind]; m != nil {
		out = append(out, m[key]...)
	}
	out = append(out, e.globs[ev.Kind]...)
	out = append(out, e.any[ev.Kind]...)
	return out
}

func (e *Engine) hit(i int, r Rule, ev Event) bool {
	switch ev.Kind {
	case KindFile:
		if r.Op != "" && r.Op != ev.Op {
			return false
		}
		if r.Path != "" && !match(r.Path, ev.Path) {
			return false
		}
	case KindNet:
		if r.Host != "" && !match(r.Host, ev.Host) {
			return false
		}
	}
	if r.Burst == nil {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	win := time.Duration(r.Burst.Seconds) * time.Second
	ts := append(e.bursts[i], ev.Time)
	cut := ev.Time.Add(-win)
	j := 0
	for j < len(ts) && ts[j].Before(cut) {
		j++
	}
	ts = ts[j:]
	if len(ts) > r.Burst.Count {
		ts = ts[len(ts)-r.Burst.Count:]
	}
	if len(ts) >= r.Burst.Count {
		e.bursts[i] = nil // fire once per burst
		return true
	}
	e.bursts[i] = ts
	return false
}

func match(pattern, s string) bool {
	if strings.HasSuffix(pattern, "/**") {
		p := strings.TrimSuffix(pattern, "/**")
		return s == p || strings.HasPrefix(s, p+"/")
	}
	ok, _ := path.Match(pattern, s)
	return ok || pattern == s
}

func (e *Engine) machine() Machine {
	if e.Machine == nil {
		return nil
	}
	return e.Machine()
}

func (e *Engine) pause(msg string) {
	e.mu.Lock()
	already := e.paused
	e.paused = true
	e.mu.Unlock()
	if already {
		return
	}
	defer func() {
		e.mu.Lock()
		e.paused = false
		e.mu.Unlock()
	}()
	m := e.machine()
	if e.Ask == nil {
		e.notify("✗ vm stopped (no one to ask): " + msg)
		e.markStopped(msg)
		e.do(m, "stop", Machine.Stop)
		return
	}
	e.do(m, "pause", Machine.Pause)
	e.notify("⏸ paused: " + msg)
	// Kill needs a capital K so an accidental keypress cannot end the session.
	k := e.Ask("VM paused: "+msg, []Option{{'r', "resume"}, {'K', "kill"}})
	if k == 'K' {
		e.notify("✗ vm killed by you")
		e.markStopped("killed by you: " + msg)
		e.do(m, "stop", Machine.Stop)
		return
	}
	e.do(m, "resume", Machine.Resume)
	e.notify("▶ resumed by you")
}

func (e *Engine) kill(msg string) {
	e.notify("✗ vm stopped: " + msg)
	e.markStopped(msg)
	e.do(e.machine(), "stop", Machine.Stop)
}

// do runs a machine operation and surfaces its failure in the panel.
func (e *Engine) do(m Machine, what string, op func(Machine) error) {
	if m == nil {
		e.notify("! cannot " + what + ": vm handle not available")
		return
	}
	if err := op(m); err != nil {
		e.notify("! " + what + " failed: " + err.Error())
	}
}

func (e *Engine) notify(s string) {
	if e.Notify != nil {
		e.Notify(s)
	}
}

func (e *Engine) record(ev Event, rule, action string) {
	if e.Ledger != nil {
		e.Ledger.Write(ev, rule, action)
	}
}
