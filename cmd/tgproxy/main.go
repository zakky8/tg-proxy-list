// Command tgproxy collects public Telegram MTProto proxies, verifies them, geo-locates
// and latency-ranks them, and publishes clean multi-format lists plus the website's JSON.
//
// Usage:
//
//	tgproxy [flags]
//
// It runs the full pipeline: collect -> verify -> geo -> publish.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"time"

	"github.com/zakky8/mtproto-proxy-pro/internal/geo"
	"github.com/zakky8/mtproto-proxy-pro/internal/model"
	"github.com/zakky8/mtproto-proxy-pro/internal/publish"
	"github.com/zakky8/mtproto-proxy-pro/internal/reach"
	"github.com/zakky8/mtproto-proxy-pro/internal/source"
	"github.com/zakky8/mtproto-proxy-pro/internal/verify"
)

func main() {
	var (
		sourcesPath  = flag.String("sources", "sources.txt", "path to feed list")
		outDir       = flag.String("out", ".", "directory for canonical output lists")
		docsDir      = flag.String("docs", "docs", "directory for the website (gets proxies.json)")
		geoPath      = flag.String("geo", "data/dbip-country-ipv4.csv", "path to IP->country CSV (downloaded if absent)")
		statePath    = flag.String("state", ".state/history.json", "path to uptime history")
		concurrency  = flag.Int("concurrency", 150, "parallel verification workers")
		timeout      = flag.Duration("timeout", 6*time.Second, "per-proxy verification timeout")
		limit        = flag.Int("limit", 0, "cap candidates (0 = no cap; useful for quick local runs)")
		minHandshake = flag.Bool("handshake-only", false, "publish only proxies that passed a protocol handshake")
		reachOn      = flag.Bool("reach", false, "test top FakeTLS proxies for reachability from inside censored countries (check-host.net)")
		reachSample  = flag.Int("reach-sample", 80, "how many top FakeTLS proxies to in-country test when --reach is set")
	)
	flag.Parse()

	cfg := config{
		sourcesPath: *sourcesPath, outDir: *outDir, docsDir: *docsDir, geoPath: *geoPath,
		statePath: *statePath, concurrency: *concurrency, timeout: *timeout, limit: *limit,
		handshakeOnly: *minHandshake, reachOn: *reachOn, reachSample: *reachSample,
	}
	if err := run(cfg); err != nil {
		log.Fatalf("tgproxy: %v", err)
	}
}

type config struct {
	sourcesPath, outDir, docsDir, geoPath, statePath string
	concurrency                                      int
	timeout                                          time.Duration
	limit                                            int
	handshakeOnly                                    bool
	reachOn                                          bool
	reachSample                                      int
}

func run(cfg config) error {
	sourcesPath, outDir, docsDir, geoPath, statePath := cfg.sourcesPath, cfg.outDir, cfg.docsDir, cfg.geoPath, cfg.statePath
	concurrency, timeout, limit, handshakeOnly := cfg.concurrency, cfg.timeout, cfg.limit, cfg.handshakeOnly
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	// 1. Collect.
	urls := source.Load(sourcesPath)
	log.Printf("collecting from %d sources...", len(urls))
	candidates, results := source.Collect(ctx, urls)
	for _, r := range results {
		if r.Err != nil {
			log.Printf("  source error: %s: %v", r.URL, r.Err)
		} else {
			log.Printf("  %4d  %s", r.Count, r.URL)
		}
	}
	log.Printf("collected %d unique candidates", len(candidates))
	if limit > 0 && limit < len(candidates) {
		candidates = candidates[:limit]
		log.Printf("limited to %d candidates", len(candidates))
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no candidates collected")
	}

	// 2. Geo dataset (best-effort).
	geoDB, err := geo.Load(ctx, geoPath)
	if err != nil {
		log.Printf("geo: %v (continuing without country data)", err)
	}

	// 3. Verify.
	log.Printf("verifying %d candidates (concurrency=%d, timeout=%s)...", len(candidates), concurrency, timeout)
	var lastPct int
	res := verify.Many(ctx, candidates, verify.Options{Concurrency: concurrency, Timeout: timeout}, func(done, total int) {
		if pct := done * 100 / total; pct >= lastPct+10 {
			lastPct = pct - pct%10
			log.Printf("  verified %d/%d (%d%%)", done, total, pct)
		}
	})

	// 4. Merge results, update history, build the verified set.
	hist := publish.LoadHistory(statePath)
	var verified []model.Proxy
	ipByKey := map[string]net.IP{}
	for i, p := range candidates {
		r := res[i]
		hist.Record(p.Key(), r.OK, now)
		if !r.OK {
			continue
		}
		if handshakeOnly && r.Status != model.StatusHandshakeOK {
			continue
		}
		p.Status = r.Status
		p.LatencyMS = r.LatencyMS
		p.LastChecked = now
		p.Country = geoDB.LookupIP(r.IP)
		p.UptimePct = hist.Pct(p.Key())
		p.Link = p.HTTPSLink()
		ipByKey[p.Key()] = r.IP
		verified = append(verified, p)
	}
	log.Printf("verified OK: %d / %d (%.1f%%)", len(verified), len(candidates), pct(len(verified), len(candidates)))

	if len(verified) == 0 {
		return fmt.Errorf("no proxies passed verification")
	}

	// 4b. In-country reachability: test the most resistant proxies from inside
	// censored networks (Iran/Russia/…). Best-effort and non-fatal.
	if cfg.reachOn {
		runReach(ctx, verified, ipByKey, cfg.reachSample)
	}

	// 4c. Resilience score for every proxy (after reach, so it counts).
	for i := range verified {
		verified[i].ComputeResilience()
	}

	// 5. Publish + persist state.
	if err := publish.Write(outDir, docsDir, verified, now); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	if err := hist.Save(statePath); err != nil {
		log.Printf("warning: could not save history: %v", err)
	}

	printSummary(verified)
	return nil
}

// runReach tests the most censorship-resistant proxies (FakeTLS, fastest first)
// for reachability from inside censored countries and records the result.
func runReach(ctx context.Context, proxies []model.Proxy, ipByKey map[string]net.IP, sample int) {
	var cands []model.Proxy
	for _, p := range proxies {
		if p.Type == model.TypeEE {
			cands = append(cands, p)
		}
	}
	cands = model.SortByLatency(cands)
	if sample > 0 && len(cands) > sample {
		cands = cands[:sample]
	}

	var targets []reach.Target
	for _, p := range cands {
		if ip := ipByKey[p.Key()]; ip != nil {
			targets = append(targets, reach.Target{Key: p.Key(), IP: ip.String(), Port: p.Port})
		}
	}
	if len(targets) == 0 {
		return
	}
	log.Printf("reach: in-country testing %d FakeTLS proxies (rate-limited, may take ~%dm)...",
		len(targets), len(targets)*17/60+1)

	results := reach.Check(ctx, targets, reach.Options{Log: log.Printf})
	byKey := map[string][]string{}
	for _, r := range results {
		if len(r.Reachable) > 0 {
			byKey[r.Key] = r.Reachable
		}
	}
	for i := range proxies {
		if cc, ok := byKey[proxies[i].Key()]; ok {
			proxies[i].ReachableFrom = cc
		}
	}
}

func printSummary(proxies []model.Proxy) {
	byCountry := map[string]int{}
	handshake, resistant := 0, 0
	reachBy := map[string]int{}
	for _, p := range proxies {
		byCountry[p.Country]++
		if p.Status == model.StatusHandshakeOK {
			handshake++
		}
		if p.IsCensorshipResistant() {
			resistant++
		}
		for _, cc := range p.ReachableFrom {
			reachBy[cc]++
		}
	}
	type kv struct {
		cc string
		n  int
	}
	var top []kv
	for cc, n := range byCountry {
		top = append(top, kv{cc, n})
	}
	sort.Slice(top, func(i, j int) bool { return top[i].n > top[j].n })
	if len(top) > 10 {
		top = top[:10]
	}
	fmt.Fprintf(os.Stderr, "\npublished %d proxies (%d handshake_ok, %d censorship-resistant)\n", len(proxies), handshake, resistant)
	fmt.Fprintf(os.Stderr, "top countries: ")
	for _, e := range top {
		fmt.Fprintf(os.Stderr, "%s=%d ", e.cc, e.n)
	}
	fmt.Fprintln(os.Stderr)
	if len(reachBy) > 0 {
		fmt.Fprintf(os.Stderr, "reachable from inside: ")
		for cc, n := range reachBy {
			fmt.Fprintf(os.Stderr, "%s=%d ", cc, n)
		}
		fmt.Fprintln(os.Stderr)
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100.0
}
