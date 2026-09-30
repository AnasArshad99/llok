package broker

import (
	"strings"
	"testing"
	"time"
)

const sampleLog = `{"time":"2026-09-30T22:42:56+01:00","project":"a","kind":"api","method":"GET","host":"api.anthropic.com","path":"/api/hello","decision":"allow","status":200,"duration":1}
{"time":"2026-09-30T22:44:23+01:00","project":"a","kind":"connect","method":"CONNECT","host":"raw.githubusercontent.com:443","decision":"deny","duration":1}
{"time":"2026-09-30T22:44:24+01:00","project":"a","kind":"connect","method":"CONNECT","host":"raw.githubusercontent.com:443","decision":"deny","duration":1}
{"time":"2026-09-30T22:44:25+01:00","project":"a","kind":"connect","method":"CONNECT","host":"http-intake.logs.datadoghq.com:443","decision":"deny","duration":1}
{"time":"2026-09-30T22:45:00+01:00","project":"b","kind":"api","method":"POST","host":"api.anthropic.com","path":"/v1/messages","decision":"allow","status":401,"duration":1}
not json
`

func TestSummarize(t *testing.T) {
	s := Summarize(strings.NewReader(sampleLog), "a", time.Time{})
	if s.Total != 4 {
		t.Fatalf("total = %d", s.Total)
	}
	if len(s.Allowed) != 1 || s.Allowed[0].Host != "api.anthropic.com" || s.Allowed[0].Statuses[200] != 1 {
		t.Errorf("allowed = %+v", s.Allowed)
	}
	if len(s.Denied) != 2 || s.Denied[0].Host != "raw.githubusercontent.com" || s.Denied[0].Count != 2 {
		t.Errorf("denied = %+v", s.Denied)
	}
	if want := "network:\n  allow:\n    - raw.githubusercontent.com\n    - http-intake.logs.datadoghq.com\n"; s.SuggestAllow() != want {
		t.Errorf("suggest = %q", s.SuggestAllow())
	}
	if strings.Join(s.Projects, ",") != "a,b" {
		t.Errorf("projects = %v", s.Projects)
	}

	all := Summarize(strings.NewReader(sampleLog), "", time.Time{})
	if all.Total != 5 {
		t.Errorf("all projects total = %d", all.Total)
	}
	since := Summarize(strings.NewReader(sampleLog), "", time.Date(2026, 9, 30, 21, 44, 30, 0, time.UTC))
	if since.Total != 1 { // only the 22:45+01:00 event is after 21:44:30Z
		t.Errorf("since total = %d", since.Total)
	}
}
