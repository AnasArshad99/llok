package guard

import (
	"testing"
	"time"
)

type fakeMachine struct{ paused, resumed, stopped int }

func (m *fakeMachine) Pause() error  { m.paused++; return nil }
func (m *fakeMachine) Resume() error { m.resumed++; return nil }
func (m *fakeMachine) Stop() error   { m.stopped++; return nil }

func TestMatch(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{".git/hooks/*", ".git/hooks/pre-commit", true},
		{".git/hooks/*", ".git/config", false},
		{".github/workflows/*", ".github/workflows/ci.yml", true},
		{"src/**", "src/a/b/c.go", true},
		{"src/**", "srcs/a.go", false},
		{"src/**", "src", true},
		{"api.github.com", "api.github.com", true},
	}
	for _, c := range cases {
		if got := match(c.pat, c.s); got != c.want {
			t.Errorf("match(%q, %q) = %v", c.pat, c.s, got)
		}
	}
}

func TestMergeAndValidate(t *testing.T) {
	rs := Merge(Defaults(), []Rule{
		{Name: "git-hooks", On: KindFile, Path: ".git/hooks/*", Action: ActNotify},
		{Name: "mass-delete", Action: ActOff},
		{Name: "lockfile", On: KindFile, Path: "package-lock.json", Action: ActNotify},
	})
	byName := map[string]Rule{}
	for _, r := range rs {
		byName[r.Name] = r
	}
	if byName["git-hooks"].Action != ActNotify {
		t.Error("override should replace the default")
	}
	if _, ok := byName["mass-delete"]; ok {
		t.Error("off should remove the default")
	}
	if _, ok := byName["lockfile"]; !ok {
		t.Error("new rule should be appended")
	}
	if err := Validate(rs); err != nil {
		t.Error(err)
	}
	if err := Validate([]Rule{{Name: "x", On: KindFile, Action: "hold"}}); err == nil {
		t.Error("unknown action should be rejected")
	}
}

func TestIndexedCandidates(t *testing.T) {
	// Many exact rules must not be evaluated for an unrelated path.
	var rs []Rule
	for i := 0; i < 100000; i++ {
		rs = append(rs, Rule{Name: "r", On: KindFile, Path: "vendor/pkg" + string(rune('a'+i%26)) + "/file", Action: ActNotify})
	}
	rs = append(rs, Defaults()...)
	e := New(rs)
	c := e.candidates(Event{Kind: KindFile, Path: "README.md"})
	if len(c) > 5 {
		t.Errorf("expected only glob/any candidates, got %d", len(c))
	}
	c = e.candidates(Event{Kind: KindFile, Path: "vendor/pkga/file"})
	found := false
	for _, i := range c {
		if e.rules[i].Path == "vendor/pkga/file" {
			found = true
		}
	}
	if !found {
		t.Error("exact rule should be a candidate for its own path")
	}
}

func TestGitHookPauseThenKill(t *testing.T) {
	m := &fakeMachine{}
	e := New(Defaults())
	e.Machine = func() Machine { return m }
	answered := make(chan struct{}, 1)
	e.Ask = func(q string, opts []Option) byte { answered <- struct{}{}; return 'K' }
	e.Observe(Event{Kind: KindFile, Op: "write", Path: ".git/hooks/pre-commit"})
	select {
	case <-answered:
	case <-time.After(time.Second):
		t.Fatal("hook write should pause and ask")
	}
	time.Sleep(20 * time.Millisecond)
	if m.paused != 1 || m.stopped != 1 {
		t.Errorf("paused=%d stopped=%d", m.paused, m.stopped)
	}
}

func TestBurstFiresOnceAndResumes(t *testing.T) {
	m := &fakeMachine{}
	e := New(Defaults())
	e.Machine = func() Machine { return m }
	answered := make(chan struct{}, 1)
	e.Ask = func(q string, opts []Option) byte { answered <- struct{}{}; return 'r' }
	base := time.Now()
	for i := 0; i < 49; i++ {
		e.Observe(Event{Kind: KindFile, Op: "delete", Path: "f", Time: base.Add(time.Duration(i) * time.Millisecond)})
	}
	if m.paused != 0 {
		t.Fatal("49 deletes must not pause")
	}
	e.Observe(Event{Kind: KindFile, Op: "delete", Path: "f", Time: base.Add(50 * time.Millisecond)})
	select {
	case <-answered:
	case <-time.After(time.Second):
		t.Fatal("50th delete should pause")
	}
	time.Sleep(20 * time.Millisecond)
	if m.paused != 1 || m.resumed != 1 {
		t.Errorf("paused=%d resumed=%d", m.paused, m.resumed)
	}
	// Deletes older than the window do not count.
	e.Observe(Event{Kind: KindFile, Op: "delete", Path: "f", Time: base.Add(30 * time.Second)})
	if m.paused != 1 {
		t.Error("a lone delete after the window must not pause")
	}
}

func TestHeadlessPauseStops(t *testing.T) {
	m := &fakeMachine{}
	e := New(Defaults())
	e.Machine = func() Machine { return m }
	e.Observe(Event{Kind: KindFile, Op: "create", Path: ".git/hooks/post-checkout"})
	time.Sleep(20 * time.Millisecond)
	if m.stopped != 1 {
		t.Errorf("with no one to ask, pause should stop the vm; stopped=%d", m.stopped)
	}
}
