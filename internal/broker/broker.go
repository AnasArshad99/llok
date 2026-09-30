// Package broker is the only way out of a session VM. It listens on a vsock
// port the guest forwards to, and decides per request what may leave:
//
//   - requests addressed to the broker itself (origin-form, e.g. POST
//     /v1/messages) are the agent's provider API calls. They are rewritten
//     to https://api.anthropic.com with the real credential injected; the
//     guest only ever holds a placeholder.
//   - CONNECT and absolute-form proxy requests go through only if the host
//     is on the allowlist from llok.yaml. Nothing is injected into those.
//
// Every decision is reported as an Event and appended to the audit log.
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Credential is the real secret for the provider, kept on the host.
type Credential struct {
	Kind   string // "apikey" or "oauth"
	Secret string
}

// Policy is what llok.yaml allows beyond the provider API.
type Policy struct {
	Allow []string // hostnames; "*.example.com" allows subdomains
}

// Event is one audited decision.
type Event struct {
	Time     time.Time     `json:"time"`
	Project  string        `json:"project"`
	Kind     string        `json:"kind"` // api | connect | http
	Method   string        `json:"method"`
	Host     string        `json:"host"`
	Path     string        `json:"path,omitempty"`
	Decision string        `json:"decision"` // allow | deny | error
	Status   int           `json:"status,omitempty"`
	Duration time.Duration `json:"duration"`
	Error    string        `json:"error,omitempty"`
}

// Line renders an event for the panel.
func (e Event) Line() string {
	d := e.Duration.Round(100 * time.Millisecond)
	switch {
	case e.Decision == "deny":
		return fmt.Sprintf("✗ %s %s denied", e.Method, e.Host)
	case e.Kind == "api":
		return fmt.Sprintf("→ %s %s %d %s", e.Method, e.Path, e.Status, d)
	case e.Kind == "connect":
		if e.Error != "" {
			return fmt.Sprintf("✗ %s %s", e.Host, e.Error)
		}
		return fmt.Sprintf("↔ %s %s", e.Host, d)
	default:
		return fmt.Sprintf("→ %s %s %d %s", e.Method, e.Host, e.Status, d)
	}
}

// Broker serves the policy on a listener.
type Broker struct {
	Project  string
	Policy   Policy
	Cred     Credential
	Upstream *url.URL // provider API base, default https://api.anthropic.com
	OnEvent  func(Event)

	audit   *os.File
	auditMu sync.Mutex
	api     *httputil.ReverseProxy
	fwd     *httputil.ReverseProxy
	srv     *http.Server
}

const anthropicHost = "api.anthropic.com"

// New builds a broker writing its audit log to auditPath (append).
func New(project string, pol Policy, cred Credential, auditPath string) (*Broker, error) {
	b := &Broker{Project: project, Policy: pol, Cred: cred}
	b.Upstream = &url.URL{Scheme: "https", Host: anthropicHost}
	if auditPath != "" {
		f, err := os.OpenFile(auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		b.audit = f
	}
	tr := &http.Transport{
		Proxy:                 nil, // never chain through a host proxy
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute, // long model responses
		IdleConnTimeout:       90 * time.Second,
	}
	b.api = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(b.Upstream)
			pr.Out.Host = b.Upstream.Host
			pr.Out.Header.Del("X-Forwarded-For")
			// Swap the guest's placeholder for the real credential.
			pr.Out.Header.Del("X-Api-Key")
			pr.Out.Header.Del("Authorization")
			switch b.Cred.Kind {
			case "oauth":
				pr.Out.Header.Set("Authorization", "Bearer "+b.Cred.Secret)
			default:
				pr.Out.Header.Set("X-Api-Key", b.Cred.Secret)
			}
		},
		Transport:     tr,
		FlushInterval: -1, // stream SSE tokens as they arrive
		ErrorHandler:  b.proxyError,
	}
	b.fwd = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = pr.In.URL
			pr.Out.Host = pr.In.URL.Host
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("Proxy-Connection")
		},
		Transport:     tr,
		FlushInterval: -1,
		ErrorHandler:  b.proxyError,
	}
	return b, nil
}

// Serve runs until the listener closes.
func (b *Broker) Serve(l net.Listener) error {
	b.srv = &http.Server{Handler: b, ReadHeaderTimeout: 30 * time.Second}
	err := b.srv.Serve(l)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Close stops serving and closes the audit log.
func (b *Broker) Close() {
	if b.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		b.srv.Shutdown(ctx)
	}
	if b.audit != nil {
		b.audit.Close()
	}
}

func (b *Broker) allowed(host string) bool {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	h = strings.ToLower(h)
	if h == anthropicHost {
		return true
	}
	for _, a := range b.Policy.Allow {
		a = strings.ToLower(a)
		switch {
		case strings.HasPrefix(a, "*."):
			if h == a[2:] || strings.HasSuffix(h, a[1:]) {
				return true
			}
		case h == a:
			return true
		}
	}
	return false
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ev := Event{Time: start, Project: b.Project, Method: r.Method}

	switch {
	case r.Method == http.MethodConnect:
		ev.Kind, ev.Host = "connect", r.Host
		if !b.allowed(r.Host) {
			ev.Decision = "deny"
			b.deny(w, r.Host)
			b.emit(ev, start)
			return
		}
		ev.Decision = "allow"
		ev.Error = b.tunnel(w, r)
		b.emit(ev, start)

	case r.URL.IsAbs():
		ev.Kind, ev.Host, ev.Path = "http", r.URL.Host, r.URL.Path
		if !b.allowed(r.URL.Host) {
			ev.Decision = "deny"
			b.deny(w, r.URL.Host)
			b.emit(ev, start)
			return
		}
		ev.Decision = "allow"
		rec := &statusRecorder{ResponseWriter: w}
		b.fwd.ServeHTTP(rec, r)
		ev.Status = rec.status
		b.emit(ev, start)

	default:
		// Origin-form: the agent talking to its provider through us.
		ev.Kind, ev.Host, ev.Path, ev.Decision = "api", b.Upstream.Host, r.URL.Path, "allow"
		rec := &statusRecorder{ResponseWriter: w}
		b.api.ServeHTTP(rec, r)
		ev.Status = rec.status
		b.emit(ev, start)
	}
}

func (b *Broker) deny(w http.ResponseWriter, host string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusForbidden)
	fmt.Fprintf(w, "llok: %s is not allowed by llok.yaml (network.allow)\n", host)
}

func (b *Broker) proxyError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusBadGateway)
	fmt.Fprintf(w, "llok broker: %v\n", err)
}

// tunnel handles CONNECT: dial the target from the host and splice bytes.
func (b *Broker) tunnel(w http.ResponseWriter, r *http.Request) string {
	up, err := net.DialTimeout("tcp", r.Host, 10*time.Second)
	if err != nil {
		http.Error(w, "llok broker: "+err.Error(), http.StatusBadGateway)
		return err.Error()
	}
	defer up.Close()
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "llok broker: cannot hijack", http.StatusInternalServerError)
		return "hijack unsupported"
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return err.Error()
	}
	defer conn.Close()
	io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	if n := buf.Reader.Buffered(); n > 0 {
		b, _ := buf.Reader.Peek(n)
		up.Write(b)
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, conn); done <- struct{}{} }()
	go func() { io.Copy(conn, up); done <- struct{}{} }()
	<-done
	return ""
}

func (b *Broker) emit(ev Event, start time.Time) {
	ev.Duration = time.Since(start)
	if b.audit != nil {
		line, _ := json.Marshal(ev)
		b.auditMu.Lock()
		b.audit.Write(append(line, '\n'))
		b.auditMu.Unlock()
	}
	if b.OnEvent != nil {
		b.OnEvent(ev)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

// Flush lets the reverse proxy stream through us.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
