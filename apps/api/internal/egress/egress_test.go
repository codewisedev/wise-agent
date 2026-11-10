package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAllowlist(t *testing.T) {
	var a Allowlist
	a.Set([]string{"registry.npmjs.org", "*.pythonhosted.org"})
	for h, want := range map[string]bool{
		"registry.npmjs.org": true, "REGISTRY.npmjs.org.": true, "files.pythonhosted.org": true,
		"pythonhosted.org": false, "evil-npmjs.org": false, "npmjs.org": false, "10.0.0.1": false, "": false,
		"pythonhosted.org.evil.com": false,
	} {
		if got := a.Allows(h); got != want {
			t.Errorf("Allows(%q) = %v", h, got)
		}
	}
	for ip, want := range map[string]bool{"8.8.8.8": true, "10.1.2.3": false, "127.0.0.1": false, "169.254.169.254": false,
		"172.17.0.1": false, "100.64.1.1": false, "::1": false, "fd00::1": false, "::ffff:10.0.0.1": false} {
		if got := public(netip.MustParseAddr(ip)); got != want {
			t.Errorf("public(%s) = %v", ip, got)
		}
	}
}

func TestProxyAllowsAndDenies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello from upstream") }))
	defer upstream.Close()
	_, port, _ := net.SplitHostPort(upstream.Listener.Addr().String())

	var a Allowlist
	a.Set([]string{"allowed.test"})
	p := &Proxy{Allow: &a, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Ports: map[int]bool{mustAtoi(port): true},
		public: func(netip.Addr) bool { return true }}
	p.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	srv := httptest.NewServer(p)
	defer srv.Close()

	// CONNECT to an allowed host tunnels through.
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT allowed.test:%s HTTP/1.1\r\nHost: allowed.test:%s\r\n\r\n", port, port)
	br := bufio.NewReader(conn)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "200") {
		t.Fatalf("CONNECT: %q", line)
	}
	br.ReadString('\n')
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: allowed.test\r\nConnection: close\r\n\r\n")
	body, _ := io.ReadAll(br)
	if !strings.Contains(string(body), "hello from upstream") {
		t.Fatalf("tunnel: %q", body)
	}
	conn.Close()

	// Denied host, denied port, private address.
	for _, target := range []string{"denied.test:" + port, "allowed.test:22"} {
		c, _ := net.Dial("tcp", srv.Listener.Addr().String())
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		l, _ := bufio.NewReader(c).ReadString('\n')
		if !strings.Contains(l, "403") {
			t.Errorf("CONNECT %s: %q", target, l)
		}
		c.Close()
	}
	p.public = nil // real check: 127.0.0.1 is private
	c, _ := net.Dial("tcp", srv.Listener.Addr().String())
	fmt.Fprintf(c, "CONNECT allowed.test:%s HTTP/1.1\r\nHost: allowed.test\r\n\r\n", port)
	if l, _ := bufio.NewReader(c).ReadString('\n'); !strings.Contains(l, "403") {
		t.Errorf("private address allowed: %q", l)
	}
	c.Close()
}

func mustAtoi(s string) int {
	var n int
	fmt.Sscan(s, &n)
	return n
}

func TestRunnerGrants(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer upstream.Close()
	_, port, _ := net.SplitHostPort(upstream.Listener.Addr().String())
	var a Allowlist
	a.Set([]string{"registry.npmjs.org"})
	live := map[string]Grant{"tok-1": {Hosts: []string{"api.anthropic.com"}, Expires: time.Now().Add(time.Hour)}}
	var lookups int
	p := &Proxy{Allow: &a, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Ports: map[int]bool{mustAtoi(port): true},
		public: func(netip.Addr) bool { return true },
		Grants: func(_ context.Context, tok string) (Grant, bool) { lookups++; g, ok := live[tok]; return g, ok }}
	p.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	srv := httptest.NewServer(p)
	defer srv.Close()
	connect := func(host, auth string) string {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "CONNECT %s:%s HTTP/1.1\r\nHost: %s\r\n", host, port, host)
		if auth != "" {
			fmt.Fprintf(c, "Proxy-Authorization: Basic %s\r\n", base64.StdEncoding.EncodeToString([]byte("run:"+auth)))
		}
		fmt.Fprint(c, "\r\n")
		l, _ := bufio.NewReader(c).ReadString('\n')
		return l
	}
	if l := connect("api.anthropic.com", ""); !strings.Contains(l, "403") {
		t.Errorf("vendor host without a grant: %q", l)
	}
	if l := connect("api.anthropic.com", "tok-1"); !strings.Contains(l, "200") {
		t.Errorf("vendor host with the run's grant: %q", l)
	}
	if l := connect("evil.example.com", "tok-1"); !strings.Contains(l, "403") {
		t.Errorf("a grant only adds its hosts: %q", l)
	}
	if l := connect("registry.npmjs.org", "tok-2"); !strings.Contains(l, "200") {
		t.Errorf("the global allowlist applies to every runner: %q", l)
	}
	if l := connect("api.anthropic.com", "tok-2"); !strings.Contains(l, "403") {
		t.Errorf("unknown token: %q", l)
	}
	// Cached briefly: a second request does not look the grant up again.
	n := lookups
	connect("api.anthropic.com", "tok-1")
	if lookups != n {
		t.Errorf("grant looked up again within the cache period")
	}
	// Revoked grants stop working once the cache entry expires.
	delete(live, "tok-1")
	p.cacheMu.Lock()
	for k, c := range p.cache {
		c.until = time.Now().Add(-time.Second)
		p.cache[k] = c
	}
	p.cacheMu.Unlock()
	if l := connect("api.anthropic.com", "tok-1"); !strings.Contains(l, "403") {
		t.Errorf("revoked grant: %q", l)
	}
	// Private addresses stay refused even with a grant.
	p.public = nil
	live["tok-3"] = Grant{Hosts: []string{"api.anthropic.com"}, Expires: time.Now().Add(time.Hour)}
	if l := connect("api.anthropic.com", "tok-3"); !strings.Contains(l, "403") {
		t.Errorf("private address with a grant: %q", l)
	}
}
