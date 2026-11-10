// Package egress is the proxy that is the only way out of task sandboxes.
// It allows HTTP(S) to allowlisted hosts (package registries by default),
// never to private or local addresses, and logs what it denies. Its only
// credential reads the allowlist from the executor.
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Allowlist matches host names: "example.org" exactly, "*.example.org" any
// subdomain of it.
type Allowlist struct {
	mu    sync.RWMutex
	exact map[string]bool
	wild  []string
}

// Set replaces the entries.
func (a *Allowlist) Set(hosts []string) {
	exact := map[string]bool{}
	var wild []string
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if suffix, ok := strings.CutPrefix(h, "*."); ok {
			wild = append(wild, "."+suffix)
		} else if h != "" {
			exact[h] = true
		}
	}
	a.mu.Lock()
	a.exact, a.wild = exact, wild
	a.mu.Unlock()
}

// Allows reports whether host may be reached.
func (a *Allowlist) Allows(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false // names only
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.exact[host] {
		return true
	}
	for _, w := range a.wild {
		if strings.HasSuffix(host, w) {
			return true
		}
	}
	return false
}

// public reports addresses outside private, local and special ranges.
func public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

var blocked = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"),
}

// Proxy is the egress HTTP proxy.
type Proxy struct {
	Allow *Allowlist
	Log   *slog.Logger
	// lookup resolves names (tests override).
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Ports that may be reached (default 443 and 80).
	Ports map[int]bool
	// public decides whether an address may be dialled (tests override).
	public func(netip.Addr) bool
}

func (p *Proxy) isPublic(ip netip.Addr) bool {
	if p.public != nil {
		return p.public(ip)
	}
	return public(ip)
}

// dial connects to host:port after checking the allowlist and that the
// address is public (no DNS tricks into the private network).
func (p *Proxy) dial(ctx context.Context, hostport string) (net.Conn, error) {
	host, portS, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(portS)
	ports := p.Ports
	if ports == nil {
		ports = map[int]bool{443: true, 80: true}
	}
	if !ports[port] {
		return nil, fmt.Errorf("port %d is not allowed", port)
	}
	if !p.Allow.Allows(host) {
		return nil, fmt.Errorf("%s is not on the egress allowlist", host)
	}
	lookup := p.lookup
	if lookup == nil {
		lookup = func(ctx context.Context, h string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		}
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	d.Timeout = 15 * time.Second
	var last error = fmt.Errorf("%s resolves only to private addresses", host)
	for _, ip := range ips {
		if !p.isPublic(ip) {
			continue
		}
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), portS))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func (p *Proxy) deny(w http.ResponseWriter, r *http.Request, host string, err error) {
	p.Log.Warn("egress denied", "host", host, "method", r.Method, "reason", err.Error())
	http.Error(w, "wise egress: "+err.Error(), http.StatusForbidden)
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Host == "" {
		http.Error(w, "wise egress: this is a proxy", http.StatusBadRequest)
		return
	}
	if r.URL.Scheme != "http" {
		if !p.Allow.Allows(r.URL.Hostname()) {
			p.deny(w, r, r.URL.Hostname(), errors.New("not on the egress allowlist"))
			return
		}
		http.Error(w, "wise egress: use CONNECT for "+r.URL.Scheme, http.StatusBadRequest)
		return
	}
	hostport := r.URL.Host
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		hostport = net.JoinHostPort(r.URL.Hostname(), "80")
	}
	host, _, _ := net.SplitHostPort(hostport)
	if !p.Allow.Allows(host) {
		p.deny(w, r, host, errors.New("not on the egress allowlist"))
		return
	}
	tr := &http.Transport{
		DialContext:           func(ctx context.Context, _, addr string) (net.Conn, error) { return p.dial(ctx, addr) },
		ResponseHeaderTimeout: time.Minute,
	}
	defer tr.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	res, err := tr.RoundTrip(out)
	if err != nil {
		p.deny(w, r, host, err)
		return
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad CONNECT target", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	up, err := p.dial(ctx, r.Host)
	cancel()
	if err != nil {
		p.deny(w, r, host, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() {
		if buf.Reader.Buffered() > 0 {
			_, _ = io.CopyN(up, buf, int64(buf.Reader.Buffered()))
		}
		_, _ = io.Copy(up, conn)
		if c, ok := up.(*net.TCPConn); ok {
			_ = c.CloseWrite()
		}
	}()
	_, _ = io.Copy(conn, up)
	up.Close()
	conn.Close()
}

// Follow keeps the allowlist in sync with the executor until ctx is done.
func Follow(ctx context.Context, a *Allowlist, url, token string, log *slog.Logger) {
	c := &http.Client{Timeout: 10 * time.Second}
	var last string
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/egress", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := c.Do(req)
		if err == nil {
			var body struct {
				Hosts []string `json:"hosts"`
			}
			if res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&body) == nil {
				a.Set(body.Hosts)
				if key := strings.Join(body.Hosts, ","); key != last {
					last = key
					log.Info("egress allowlist updated", "hosts", len(body.Hosts))
				}
			}
			res.Body.Close()
		} else if ctx.Err() == nil {
			log.Warn("egress allowlist: executor unreachable", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}
