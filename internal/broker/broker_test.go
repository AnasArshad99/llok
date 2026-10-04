package broker

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func testRoutes(kind string) []Route {
	r := Route{Name: "anthropic", Hosts: []string{"api.anthropic.com", "platform.claude.com"},
		Upstream: &url.URL{Scheme: "https", Host: "api.anthropic.com"},
		Cred:     Credential{Kind: kind, Secret: "REAL-SECRET"}}
	if kind == "oauth" {
		r.Header, r.Format = "Authorization", "Bearer {secret}"
	} else {
		r.Header, r.Format = "X-Api-Key", "{secret}"
	}
	return []Route{r}
}

func newTestBroker(t *testing.T, kind string, allow ...string) *Broker {
	t.Helper()
	b, err := New("test", Policy{Allow: allow}, testRoutes(kind), "")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAllowed(t *testing.T) {
	b := newTestBroker(t, "apikey", "registry.npmjs.org", "*.github.com")
	cases := map[string]bool{
		"api.anthropic.com:443":    true, // provider host, always
		"platform.claude.com":      true,
		"registry.npmjs.org:443":   true,
		"REGISTRY.NPMJS.ORG":       true, // case-insensitive
		"api.github.com:443":       true, // wildcard
		"github.com":               true, // wildcard also matches the apex
		"evil.github.com.attacker": false,
		"www.google.com:443":       false,
		"npmjs.org":                false, // no wildcard for this one
		"registry.npmjs.org.evil":  false,
	}
	for host, want := range cases {
		if got := b.allowed(host); got != want {
			t.Errorf("allowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestInjectSwapsPlaceholderOnly(t *testing.T) {
	b := newTestBroker(t, "oauth")

	h := http.Header{}
	h.Set("Authorization", "Bearer llok-placeholder-token")
	if !b.inject(h, b.Routes[0], false) {
		t.Fatal("placeholder bearer should be injected")
	}
	if got := h.Get("Authorization"); got != "Bearer REAL-SECRET" {
		t.Errorf("Authorization = %q", got)
	}
	if h.Get("X-Api-Key") != "" {
		t.Error("x-api-key should be removed")
	}

	// A request without the placeholder is left alone on the intercept path.
	h = http.Header{}
	h.Set("Authorization", "Bearer someone-elses-token")
	if b.inject(h, b.Routes[0], false) {
		t.Error("foreign credential must not be replaced")
	}
	if h.Get("Authorization") != "Bearer someone-elses-token" {
		t.Error("foreign credential must be untouched")
	}

	// The agent's own API base route always injects.
	h = http.Header{}
	if !b.inject(h, b.Routes[0], true) || h.Get("Authorization") != "Bearer REAL-SECRET" {
		t.Error("forced inject should set the credential")
	}
}

func TestInjectAPIKeyKind(t *testing.T) {
	b := newTestBroker(t, "apikey")
	h := http.Header{}
	h.Set("X-Api-Key", "llok-placeholder-key")
	h.Set("Authorization", "Bearer llok-placeholder-token") // both placeholders present
	if !b.inject(h, b.Routes[0], false) {
		t.Fatal("expected injection")
	}
	if h.Get("X-Api-Key") != "REAL-SECRET" || h.Get("Authorization") != "" {
		t.Errorf("headers after inject: %v", h)
	}
}

func TestEventLine(t *testing.T) {
	cases := []struct {
		ev   Event
		want string
	}{
		{Event{Kind: "connect", Method: "CONNECT", Host: "x.com:443", Decision: "deny"}, "✗ CONNECT x.com:443 denied"},
		{Event{Kind: "api", Method: "POST", Path: "/v1/messages", Decision: "allow", Status: 200}, "→ POST /v1/messages 200 0s"},
		{Event{Kind: "connect", Host: "a.com:443", Decision: "allow"}, "↔ a.com:443 0s"},
		{Event{Kind: "connect", Host: "a.com:443", Decision: "allow", Error: "dial timeout"}, "✗ a.com:443 dial timeout"},
	}
	for _, c := range cases {
		if got := c.ev.Line(); got != c.want {
			t.Errorf("Line() = %q, want %q", got, c.want)
		}
	}
}

func TestLeafCertificates(t *testing.T) {
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := ca.Leaf("api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	if c1.Leaf.DNSNames[0] != "api.anthropic.com" {
		t.Errorf("SAN = %v", c1.Leaf.DNSNames)
	}
	if err := c1.Leaf.CheckSignatureFrom(ca.cert); err != nil {
		t.Errorf("leaf not signed by CA: %v", err)
	}
	c2, _ := ca.Leaf("api.anthropic.com")
	if c1 != c2 {
		t.Error("leaf should be cached per host")
	}
	// Reloading from disk yields the same CA.
	ca2, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ca2.cert.Equal(ca.cert) {
		t.Error("CA should be persisted and reloaded")
	}
}

func TestSafeDialRefusesLocalAddresses(t *testing.T) {
	for _, addr := range []string{"localhost:80", "127.0.0.1:80", "[::1]:80", "10.0.0.1:443", "192.168.1.1:443", "169.254.169.254:80"} {
		_, err := safeDialContext(context.Background(), "tcp", addr)
		if err == nil || !strings.Contains(err.Error(), "not a public address") {
			t.Errorf("safeDial(%q) should refuse a non-public address, got %v", addr, err)
		}
	}
}

func TestStatusRecorderSupportsHijack(t *testing.T) {
	var _ http.Hijacker = &statusRecorder{}
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	if _, _, err := rec.Hijack(); err == nil {
		t.Error("a non-hijackable writer should report an error, not panic")
	}
}

// A WebSocket upgrade must pass through the provider proxy end to end.
func TestProviderProxyWebSocketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "websocket" || r.Header.Get("Authorization") != "Bearer REAL-SECRET" {
			http.Error(w, "bad upgrade", http.StatusBadRequest)
			return
		}
		c, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf.Flush()
		line, _ := buf.ReadString('\n')
		buf.WriteString("echo:" + line)
		buf.Flush()
	}))
	defer upstream.Close()

	b := newTestBroker(t, "oauth")
	u, _ := url.Parse(upstream.URL)
	rt := b.Routes[0]
	rt.Upstream = u
	px := b.providerProxy("test-upstream", rt)
	px.Rewrite = func(pr *httputil.ProxyRequest) { // plain http upstream for the test
		pr.SetURL(u)
		pr.Out.Host = u.Host
		b.inject(pr.Out.Header, rt, false)
	}
	px.Transport = &http.Transport{} // the real one refuses loopback by design
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		px.ServeHTTP(&statusRecorder{ResponseWriter: w}, r)
	}))
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /v1/responses HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nAuthorization: Bearer llok-placeholder-token\r\n\r\n")
	rd := bufio.NewReader(conn)
	status, _ := rd.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("expected 101 through the proxy, got %q", status)
	}
	for { // skip headers
		l, _ := rd.ReadString('\n')
		if l == "\r\n" {
			break
		}
	}
	fmt.Fprint(conn, "hello\n")
	reply, _ := rd.ReadString('\n')
	if reply != "echo:hello\n" {
		t.Errorf("bytes after the upgrade did not flow: %q", reply)
	}
}
