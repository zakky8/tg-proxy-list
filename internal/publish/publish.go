// Package publish writes the verified proxy set to every output format and tracks
// per-proxy uptime across runs.
package publish

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zakky8/mtproto-proxy-pro/internal/model"
)

// Dataset is the JSON document consumed by the website and any API client.
type Dataset struct {
	GeneratedAtUTC      string         `json:"generated_at_utc"`
	Count               int            `json:"count"`
	CensorshipResistant int            `json:"censorship_resistant"`
	ReachableFrom       map[string]int `json:"reachable_from"` // censored CC -> count proven reachable
	Countries           map[string]int `json:"countries"`
	Proxies             []model.Proxy  `json:"proxies"`
}

// Write emits all output files. rootDir holds the canonical lists; docsDir gets a
// copy of proxies.json for the static site.
func Write(rootDir, docsDir string, proxies []model.Proxy, generatedAt string) error {
	sorted := model.SortByLatency(proxies)

	countries := map[string]int{}
	reachableFrom := map[string]int{}
	resistantCount := 0
	for _, p := range sorted {
		countries[p.Country]++
		if p.IsCensorshipResistant() {
			resistantCount++
		}
		for _, cc := range p.ReachableFrom {
			reachableFrom[cc]++
		}
	}

	ds := Dataset{
		GeneratedAtUTC:      generatedAt,
		Count:               len(sorted),
		CensorshipResistant: resistantCount,
		ReachableFrom:       reachableFrom,
		Countries:           countries,
		Proxies:             sorted,
	}

	// proxies.json (root + docs copy)
	if err := writeJSON(filepath.Join(rootDir, "proxies.json"), ds); err != nil {
		return err
	}
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(docsDir, "proxies.json"), ds); err != nil {
		return err
	}

	// all_proxies.txt — reference-compatible flat list, fastest first.
	// Written to the repo root (canonical raw URL) and copied into docs/ so the
	// static site can link to it from its own origin.
	var b strings.Builder
	for _, p := range sorted {
		b.WriteString(p.HTTPSLink())
		b.WriteByte('\n')
	}
	allTxt := []byte(b.String())
	if err := os.WriteFile(filepath.Join(rootDir, "all_proxies.txt"), allTxt, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(docsDir, "all_proxies.txt"), allTxt, 0o644); err != nil {
		return err
	}

	// sorted_by_latency.txt — human-readable table.
	b.Reset()
	b.WriteString("# latency_ms  country  status        link\n")
	for _, p := range sorted {
		fmt.Fprintf(&b, "%-11d %-7s %-13s %s\n", p.LatencyMS, p.Country, p.Status, p.HTTPSLink())
	}
	if err := os.WriteFile(filepath.Join(rootDir, "sorted_by_latency.txt"), []byte(b.String()), 0o644); err != nil {
		return err
	}

	// by_country/<CC>.txt
	if err := writeByCountry(filepath.Join(rootDir, "by_country"), sorted); err != nil {
		return err
	}

	// censorship_resistant.txt — FakeTLS/443/in-country-reachable, most resistant first.
	var resistant []model.Proxy
	for _, p := range sorted {
		if p.IsCensorshipResistant() {
			resistant = append(resistant, p)
		}
	}
	resistant = model.SortByResilience(resistant)
	var rb strings.Builder
	rb.WriteString("# Censorship-resistant Telegram proxies (FakeTLS on 443, most resistant first).\n")
	rb.WriteString("# Best choice in countries that block Telegram. See README for per-country notes.\n")
	for _, p := range resistant {
		rb.WriteString(p.HTTPSLink())
		rb.WriteByte('\n')
	}
	resTxt := []byte(rb.String())
	if err := os.WriteFile(filepath.Join(rootDir, "censorship_resistant.txt"), resTxt, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(docsDir, "censorship_resistant.txt"), resTxt, 0o644); err != nil {
		return err
	}
	return nil
}

func writeByCountry(dir string, sorted []model.Proxy) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	groups := map[string][]model.Proxy{}
	for _, p := range sorted {
		cc := p.Country
		if cc == "" || cc == "??" {
			cc = "XX"
		}
		groups[cc] = append(groups[cc], p)
	}
	codes := make([]string, 0, len(groups))
	for cc := range groups {
		codes = append(codes, cc)
	}
	sort.Strings(codes)
	for _, cc := range codes {
		var b strings.Builder
		for _, p := range groups[cc] {
			b.WriteString(p.HTTPSLink())
			b.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, cc+".txt"), []byte(b.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeJSON writes v as indented JSON atomically (temp file + rename) so a reader
// never observes a half-written, unparseable file.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
