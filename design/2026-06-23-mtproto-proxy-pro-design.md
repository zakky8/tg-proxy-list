# mtproto-proxy-pro — Design Spec

**Date:** 2026-06-23
**Status:** Approved
**Owner:** zakky8

## Goal

A bot-driven repository that collects free, public Telegram **MTProto proxies** from many
sources, **verifies** them (real TCP + best-effort MTProto/FakeTLS probe, measured latency),
geo-locates and latency-ranks them, and publishes clean multi-format lists **plus a polished
one-click "Connect in Telegram" website**. Auto-refreshed every 6 hours via GitHub Actions and
SEO-tuned to be discoverable.

Inspired by `SoliSpirit/mtproto` (a single flat `all_proxies.txt`, ~1.7k stars), but
substantially better on verification, metadata, formats, connect UX, refresh cadence, and SEO.

## Non-Goals (v1 / YAGNI)

- No self-hosted proxy server.
- No user accounts, paid tiers, or backend API.
- Website is English (with i18n hooks); READMEs are multi-language.
- No scraping of private data, no traffic interception. We only re-publish reachability of
  already-public proxies.

## Honesty Principle

We never claim "100% working." Status reflects exactly what we tested:
- `reachable` — DNS resolved and TCP connected within timeout (latency measured).
- `handshake_ok` — additionally passed a best-effort protocol probe (TLS ClientHello for
  `ee`/FakeTLS secrets). Stronger signal that it is a live MTProto FakeTLS proxy.
Dead candidates are dropped. Every record carries `last_checked_utc`.

## Architecture — Go engine, single binary, minimal deps

Pipeline: `collect → verify → geo → publish`, runnable as one command.

- **collect** (`internal/source`): pull candidates from a configurable `sources.yaml`
  (public raw GitHub lists + no-key endpoints). Parse `tg://proxy?...`,
  `https://t.me/proxy?...`, and `host:port:secret`. Normalize + dedupe by `(server,port,secret)`.
- **parse** (`internal/parse`): tolerant link/secret parsing. Detect secret type:
  `plain` (32 hex), `dd` (random-padded), `ee` (FakeTLS, embeds SNI domain).
- **verify** (`internal/verify`): bounded worker pool (semaphore). DNS → TCP dial with timeout →
  measure RTT → for `ee` secrets attempt a TLS handshake to the embedded SNI (best-effort) →
  assign status. Context deadlines throughout. Tunable `--concurrency`, `--timeout`, `--limit`.
- **geo** (`internal/geo`): IP→country via an embedded/loaded public-domain IPv4→country dataset
  (CC0, sapics/ip-location-db). Binary search over sorted ranges; downloaded if absent. No API key.
- **publish** (`internal/publish`): writes
  - `all_proxies.txt` (reference-compatible, one `tg://proxy?...` per line)
  - `proxies.json` (structured records — drives the website)
  - `by_country/<CC>.txt`
  - `sorted_by_latency.txt`
  - copies `proxies.json` into `docs/` for the site
  - `.state/history.json` (uptime tracking across runs)

### Data model (`internal/model`)

```go
type Proxy struct {
    Server      string `json:"server"`
    Port        int    `json:"port"`
    Secret      string `json:"secret"`
    Type        string `json:"type"`          // plain | dd | ee
    Country     string `json:"country"`        // ISO-3166 alpha-2 or "??"
    LatencyMS   int    `json:"latency_ms"`
    Status      string `json:"status"`         // reachable | handshake_ok
    LastChecked string `json:"last_checked_utc"`
    UptimePct   int    `json:"uptime_pct,omitempty"`
    Link        string `json:"link"`           // https://t.me/proxy?... deep link
}
```

## Website (`docs/`, GitHub Pages, vanilla — no framework)

`index.html` + `app.js` + `style.css`. Loads `proxies.json`. Searchable/filterable/sortable
table: country flag, latency badge, type, **Copy** + **Connect in Telegram**
(`https://t.me/proxy?server=&port=&secret=` deep link, works on all platforms). Dark mode,
mobile-first, fast. Live "last updated" + total count. Built with the `ui-ux-pro-max` skill.

## SEO layer

Built with `ai-seo`, `schema`, `seo-audit` skills:
- README: keyword-rich title, badges (count/last-updated/build), structured headings,
  EN/RU/FA/CN translations.
- Site `<meta>` description/keywords, Open Graph + Twitter cards, canonical.
- JSON-LD: `WebSite` + `Dataset`/`ItemList`.
- `sitemap.xml`, `robots.txt`, `llms.txt`.
- Repo topics + About + homepage → Pages URL.
- Target keywords: free telegram proxy, mtproto proxy, telegram proxy list 2026,
  working telegram proxies, bypass telegram censorship.

## Automation

`.github/workflows/update.yml`: cron every 6h + `workflow_dispatch` → setup Go → build →
run pipeline → commit changed lists → deploy Pages. Badges read from committed outputs.

## Repo layout

```
cmd/tgproxy/main.go
internal/{model,parse,source,verify,geo,publish}/
data/                       # geoip dataset (downloaded/cached)
docs/                       # site + proxies.json + sitemap/robots/llms + spec
all_proxies.txt  proxies.json  by_country/  sorted_by_latency.txt
sources.yaml  .github/workflows/update.yml
README.md (+ README_RU/FA/CN.md)  LICENSE  .gitignore
```

## Testing

- Unit tests for `parse` (all three link formats + secret types) and `geo` (range lookup).
- `go vet` + `go build` clean.
- End-to-end: run the pipeline against real sources and confirm non-empty verified output.
```
