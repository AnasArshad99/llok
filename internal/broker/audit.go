package broker

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"sort"
	"strings"
	"time"
)

// HostStat is what one host saw in the audit window.
type HostStat struct {
	Host     string
	Count    int
	Last     time.Time
	Statuses map[int]int // for allowed API/HTTP calls
}

// Summary is an aggregate over audit events.
type Summary struct {
	Project  string
	From, To time.Time
	Total    int
	Allowed  []HostStat
	Denied   []HostStat
	Projects []string // every project seen (when not filtering)
}

// Summarize aggregates audit events from r. project="" means all projects;
// since is the earliest event to include (zero = everything).
func Summarize(r io.Reader, project string, since time.Time) Summary {
	s := Summary{Project: project}
	allowed := map[string]*HostStat{}
	denied := map[string]*HostStat{}
	projects := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		projects[e.Project] = true
		if project != "" && e.Project != project {
			continue
		}
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		s.Total++
		if s.From.IsZero() || e.Time.Before(s.From) {
			s.From = e.Time
		}
		if e.Time.After(s.To) {
			s.To = e.Time
		}
		host := hostOnly(e.Host)
		m := allowed
		if e.Decision == "deny" {
			m = denied
		}
		st := m[host]
		if st == nil {
			st = &HostStat{Host: host, Statuses: map[int]int{}}
			m[host] = st
		}
		st.Count++
		if e.Time.After(st.Last) {
			st.Last = e.Time
		}
		if e.Status != 0 {
			st.Statuses[e.Status]++
		}
	}
	s.Allowed = sorted(allowed)
	s.Denied = sorted(denied)
	for p := range projects {
		s.Projects = append(s.Projects, p)
	}
	sort.Strings(s.Projects)
	return s
}

func sorted(m map[string]*HostStat) []HostStat {
	out := make([]HostStat, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// SuggestAllow renders a llok.yaml snippet allowing the denied hosts.
func (s Summary) SuggestAllow() string {
	if len(s.Denied) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("network:\n  allow:\n")
	for _, d := range s.Denied {
		h := d.Host
		if _, _, err := net.SplitHostPort(h); err == nil {
			h, _, _ = net.SplitHostPort(h)
		}
		b.WriteString("    - " + h + "\n")
	}
	return b.String()
}
