// Package model defines the core proxy record shared across the pipeline.
package model

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Secret type tags.
const (
	TypePlain = "plain" // 16-byte hex secret (legacy obfuscated)
	TypeDD    = "dd"    // dd-prefixed, random-padded
	TypeEE    = "ee"    // ee-prefixed, FakeTLS (embeds an SNI domain)
)

// Verification statuses, ordered weakest to strongest.
const (
	StatusReachable   = "reachable"    // DNS + TCP connect succeeded
	StatusHandshakeOK = "handshake_ok" // additionally passed a protocol probe
)

// Proxy is one MTProto proxy and everything we learned about it.
type Proxy struct {
	Server      string `json:"server"`
	Port        int    `json:"port"`
	Secret      string `json:"secret"`
	Type        string `json:"type"`
	Country     string `json:"country"`
	LatencyMS   int    `json:"latency_ms"`
	Status      string `json:"status"`
	LastChecked string `json:"last_checked_utc"`
	UptimePct   int    `json:"uptime_pct,omitempty"`
	Link        string `json:"link"`

	// ReachableFrom lists ISO-3166 codes of censored countries from which this
	// proxy tested TCP-reachable (via in-country probes). Empty means untested
	// from inside censored networks — NOT that it is blocked.
	ReachableFrom []string `json:"reachable_from,omitempty"`
	// Resilience is a 0-100 heuristic for how likely the proxy is to survive
	// DPI-based censorship (FakeTLS on 443, valid SNI, in-country reachability).
	Resilience int `json:"resilience"`
}

// CensoredCountries are the ISO codes we treat as actively blocking/throttling
// Telegram (2026), used for in-country reachability testing and UI guidance.
// Grounded in OONI/Access Now/Freedom House reporting (see docs).
var CensoredCountries = []string{"IR", "RU", "CN", "TM", "VN", "VE", "PK", "BY", "UZ", "MM"}

// IsCensored reports whether cc is in CensoredCountries.
func IsCensored(cc string) bool {
	for _, c := range CensoredCountries {
		if c == cc {
			return true
		}
	}
	return false
}

// Key uniquely identifies a proxy for dedup purposes.
func (p Proxy) Key() string {
	return strings.ToLower(p.Server) + ":" + strconv.Itoa(p.Port) + ":" + strings.ToLower(p.Secret)
}

// TGLink renders the tg:// deep link (Telegram desktop/clients).
func (p Proxy) TGLink() string {
	return "tg://proxy?" + p.query()
}

// HTTPSLink renders the https://t.me/proxy link (works in browsers and chats on every platform).
func (p Proxy) HTTPSLink() string {
	return "https://t.me/proxy?" + p.query()
}

// query builds the proxy query string in the canonical server→port→secret order
// that Telegram clients expect. The secret is hex (safe unescaped); only the host
// is escaped. url.Values is intentionally avoided because it alphabetizes keys,
// which some Telegram clients fail to parse.
func (p Proxy) query() string {
	return "server=" + url.QueryEscape(p.Server) +
		"&port=" + strconv.Itoa(p.Port) +
		"&secret=" + p.Secret
}

// FakeTLSDomain decodes the SNI domain embedded in an ee (FakeTLS) secret, if present.
// ee secret = "ee" + 32 hex chars (16 bytes) + hex-encoded domain.
func (p Proxy) FakeTLSDomain() string {
	s := strings.ToLower(p.Secret)
	if !strings.HasPrefix(s, "ee") || len(s) <= 34 {
		return ""
	}
	hexDomain := s[34:]
	b, err := decodeHex(hexDomain)
	if err != nil {
		return ""
	}
	d := strings.TrimRight(string(b), "\x00")
	if !isPlausibleDomain(d) {
		return ""
	}
	return d
}

// Validate checks the proxy has the minimum viable fields.
func (p Proxy) Validate() error {
	if p.Server == "" {
		return fmt.Errorf("empty server")
	}
	if p.Port <= 0 || p.Port > 65535 {
		return fmt.Errorf("invalid port %d", p.Port)
	}
	if !validHost(p.Server) {
		return fmt.Errorf("invalid server %q", p.Server)
	}
	if !validSecret(p.Secret) {
		return fmt.Errorf("invalid secret %q", p.Secret)
	}
	return nil
}

// validHost accepts an IP literal or a hostname containing only DNS-safe
// characters. It rejects anything with whitespace or HTML/URL metacharacters,
// which is the only way junk from a poisoned source feed could otherwise reach
// the published lists (and, via them, a consumer that fails to escape it).
func validHost(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if len(s) < 1 || len(s) > 253 {
		return false
	}
	for _, c := range s {
		if !(c == '.' || c == '-' || c == '_' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func validSecret(s string) bool {
	s = strings.ToLower(s)
	if len(s) < 32 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ClassifySecret returns the secret type tag.
func ClassifySecret(secret string) string {
	s := strings.ToLower(secret)
	switch {
	case strings.HasPrefix(s, "ee"):
		return TypeEE
	case strings.HasPrefix(s, "dd"):
		return TypeDD
	default:
		return TypePlain
	}
}

func decodeHex(s string) ([]byte, error) {
	if len(s)%2 != 0 {
		return nil, fmt.Errorf("odd length")
	}
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok1 := hexVal(s[i*2])
		lo, ok2 := hexVal(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("bad hex")
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func isPlausibleDomain(d string) bool {
	if len(d) < 3 || len(d) > 253 || !strings.Contains(d, ".") {
		return false
	}
	for _, c := range d {
		if !(c == '.' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// ComputeResilience sets Resilience from the proxy's type, port, SNI, status, and
// observed in-country reachability. It is a heuristic, not a guarantee.
func (p *Proxy) ComputeResilience() {
	score := 0
	switch p.Type {
	case TypeEE:
		score += 55 // FakeTLS — the only DPI-survivable mode
	case TypeDD:
		score += 25
	default:
		score += 8 // plain — fingerprinted by entropy analysis
	}
	if p.Type == TypeEE && p.FakeTLSDomain() != "" {
		score += 15 // carries a real SNI to front behind
	}
	if p.Port == 443 {
		score += 12 // blends with normal HTTPS
	}
	if p.Status == StatusHandshakeOK {
		score += 8
	}
	score += len(p.ReachableFrom) * 6 // proven reachable from inside censored networks
	if score > 100 {
		score = 100
	}
	p.Resilience = score
}

// IsCensorshipResistant reports whether the proxy is a good candidate for
// blocked countries: proven reachable from a censored network, or structurally
// resistant (FakeTLS on 443 with a real SNI domain).
func (p Proxy) IsCensorshipResistant() bool {
	if len(p.ReachableFrom) > 0 {
		return true
	}
	return p.Type == TypeEE && p.Port == 443 && p.FakeTLSDomain() != ""
}

// SortByResilience returns proxies ordered most-resilient first (latency breaks ties).
func SortByResilience(in []Proxy) []Proxy {
	out := append([]Proxy(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Resilience != out[j].Resilience {
			return out[i].Resilience > out[j].Resilience
		}
		return out[i].LatencyMS < out[j].LatencyMS
	})
	return out
}

// SortByLatency returns proxies ordered fastest-first (handshake_ok preferred on ties).
func SortByLatency(in []Proxy) []Proxy {
	out := append([]Proxy(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LatencyMS != out[j].LatencyMS {
			return out[i].LatencyMS < out[j].LatencyMS
		}
		return statusRank(out[i].Status) > statusRank(out[j].Status)
	})
	return out
}

func statusRank(s string) int {
	if s == StatusHandshakeOK {
		return 1
	}
	return 0
}
