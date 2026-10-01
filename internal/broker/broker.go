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
	"crypto/tls"
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
	CA       *CA // when set, TLS to provider hosts is intercepted for injection

	audit   *os.File
	auditMu sync.Mutex
	tr      *http.Transport
	api     *httputil.ReverseProxy
	fwd     *httputil.ReverseProxy
	srv     *http.Server

	hostMu sync.Mutex
	hostPx map[string]*httputil.ReverseProxy
}

const anthropicHost = "api.anthropic.com"

// Placeholders are what the guest holds instead of a real credential. The
// guest init sets them; the broker swaps them on the way out.
const (
	PlaceholderAPIKey = "sk-ant-llok-placeholder"
	PlaceholderOAuth  = "llok-placeholder-token"
)

// providerHosts are Anthropic's own endpoints. TLS to them is terminated by
// the broker (with the guest-trusted llok CA) so the credential can be
// injected; everything the agent sends there is the provider's business.
var providerHosts = map[string]bool{
	"api.anthropic.com":     true,
	"platform.claude.com":   true,
	"console.anthropic.com": true,
}

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
		DialContext:           safeDialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute, // long model responses
		IdleConnTimeout:       90 * time.Second,
	}
	b.tr = tr
	b.hostPx = map[string]*httputil.ReverseProxy{}
	b.api = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(b.Upstream)
			pr.Out.Host = b.Upstream.Host
			pr.Out.Header.Del("X-Forwarded-For")
			b.inject(pr.Out.Header, true)
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

// inject swaps the guest's placeholder credential for the real one. With
// force it always sets the credential (the agent's own API base URL route);
// otherwise only requests that carry a placeholder are touched. Returns
// whether anything was injected.
func (b *Broker) inject(h http.Header, force bool) bool {
	auth := h.Get("Authorization")
	key := h.Get("X-Api-Key")
	hasPlaceholder := key == PlaceholderAPIKey || auth == "Bearer "+PlaceholderOAuth
	if !force && !hasPlaceholder {
		return false
	}
	h.Del("X-Api-Key")
	h.Del("Authorization")
	switch b.Cred.Kind {
	case "oauth":
		h.Set("Authorization", "Bearer "+b.Cred.Secret)
	default:
		h.Set("X-Api-Key", b.Cred.Secret)
	}
	return true
}

func hostOnly(host string) string {
	if hp, _, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(hp)
	}
	return strings.ToLower(host)
}

func (b *Broker) allowed(host string) bool {
	h := hostOnly(host)
	if providerHosts[h] {
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
		if b.CA != nil && providerHosts[hostOnly(r.Host)] {
			// Provider host: terminate TLS and inject; events per request.
			ev.Kind, ev.Decision = "intercept", "allow"
			ev.Error = b.intercept(w, r)
			if ev.Error != "" {
				b.emit(ev, start)
			}
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
		if providerHosts[hostOnly(r.URL.Host)] {
			// Never send the provider's traffic in the clear: upgrade to the
			// TLS route, which also injects the credential.
			ev.Kind = "api"
			b.providerProxy(hostOnly(r.URL.Host)).ServeHTTP(rec, r)
		} else {
			b.fwd.ServeHTTP(rec, r)
		}
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	up, err := safeDialContext(ctx, "tcp", r.Host)
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

// intercept answers a CONNECT to a provider host by terminating TLS with a
// llok-CA certificate for that host, serving HTTP on the decrypted stream,
// and reverse-proxying each request to the real host with the credential
// injected. The guest's TLS client sees a certificate chained to the llok CA.
func (b *Broker) intercept(w http.ResponseWriter, r *http.Request) string {
	host := hostOnly(r.Host)
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "llok broker: cannot hijack", http.StatusInternalServerError)
		return "hijack unsupported"
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return err.Error()
	}
	io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	tlsConn := tls.Server(conn, &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			// Always the CONNECT host: the guest does not get to choose
			// which name the llok CA signs for.
			return b.CA.Leaf(host)
		},
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
	})
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return "tls: " + err.Error()
	}

	px := b.providerProxy(host)
	done := make(chan struct{})
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			px.ServeHTTP(rec, req)
			b.emit(Event{Time: start, Project: b.Project, Kind: "api", Method: req.Method,
				Host: host, Path: req.URL.Path, Decision: "allow", Status: rec.status}, start)
		}),
		ConnState: func(_ net.Conn, st http.ConnState) {
			if st == http.StateClosed {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		},
	}
	go srv.Serve(&oneConnListener{c: tlsConn, addr: conn.LocalAddr()})
	<-done
	srv.Close()
	return ""
}

// providerProxy returns a reverse proxy to https://host that injects the
// credential into requests carrying a placeholder.
func (b *Broker) providerProxy(host string) *httputil.ReverseProxy {
	b.hostMu.Lock()
	defer b.hostMu.Unlock()
	if px, ok := b.hostPx[host]; ok {
		return px
	}
	up := &url.URL{Scheme: "https", Host: host}
	px := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(up)
			pr.Out.Host = host
			pr.Out.Header.Del("X-Forwarded-For")
			b.inject(pr.Out.Header, false)
		},
		Transport:     b.tr,
		FlushInterval: -1,
		ErrorHandler:  b.proxyError,
	}
	b.hostPx[host] = px
	return px
}

// oneConnListener hands a single already-open connection to http.Server.
type oneConnListener struct {
	c    net.Conn
	addr net.Addr
	once sync.Once
	done chan struct{}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	first := false
	l.once.Do(func() { c, first = l.c, true; l.done = make(chan struct{}) })
	if first {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.once.Do(func() { l.done = make(chan struct{}) })
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return l.addr }

// safeDialContext resolves the destination on the host and refuses any
// address that is loopback, private, link-local, or otherwise not a public
// unicast address. An allowlisted name must not become a door to services
// on the Mac or the local network, whether by configuration or by DNS
// rebinding.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	var last error
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
			last = fmt.Errorf("refusing to connect to %s (%s is not a public address)", host, ip)
			continue
		}
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		last = fmt.Errorf("no address for %s", host)
	}
	return nil, last
}
