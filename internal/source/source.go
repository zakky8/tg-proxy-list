// Package source loads the list of upstream feeds and fetches proxy candidates from them.
package source

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zakky8/mtproto-proxy-pro/internal/model"
	"github.com/zakky8/mtproto-proxy-pro/internal/parse"
)

// DefaultSources are public, community-maintained MTProto proxy feeds. Used when no
// sources file is present. Verified live on 2026-06-23.
var DefaultSources = []string{
	"https://raw.githubusercontent.com/SoliSpirit/mtproto/master/all_proxies.txt",
	"https://raw.githubusercontent.com/whoahaow/rjsxrd/main/source/config/tg_proxies.txt",
	"https://raw.githubusercontent.com/kort0881/telegram-proxy-collector/main/verified/proxy_links_tme_clean.txt",
	"https://raw.githubusercontent.com/ALIILAPRO/MTProtoProxy/main/mtproto.txt",
	"https://raw.githubusercontent.com/Surfboardv2ray/TGProto/main/proxies.txt",
	"https://raw.githubusercontent.com/V2RAYCONFIGSPOOL/TELEGRAM_PROXY_SUB/main/telegram_proxy_no5.txt",
	"https://raw.githubusercontent.com/pengvench/MTProxyAutoSwitch/main/list/all_list.txt",
}

// Load reads feed URLs from path (one per line, '#' comments). Falls back to
// DefaultSources when the file is absent or empty.
func Load(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return DefaultSources
	}
	defer f.Close()

	var urls []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	if len(urls) == 0 {
		return DefaultSources
	}
	return urls
}

// Result reports what a single feed produced.
type Result struct {
	URL   string
	Count int
	Err   error
}

// Collect fetches every feed concurrently, parses candidates, and returns a
// deduped slice plus per-feed results for logging.
func Collect(ctx context.Context, urls []string) ([]model.Proxy, []Result) {
	client := &http.Client{Timeout: 30 * time.Second}

	var (
		mu      sync.Mutex
		all     []model.Proxy
		results = make([]Result, len(urls))
		seen    = map[string]bool{}
		wg      sync.WaitGroup
	)

	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			proxies, err := fetch(ctx, client, u)
			mu.Lock()
			defer mu.Unlock()
			results[i] = Result{URL: u, Count: len(proxies), Err: err}
			for _, p := range proxies {
				if k := p.Key(); !seen[k] {
					seen[k] = true
					all = append(all, p)
				}
			}
		}(i, u)
	}
	wg.Wait()
	return all, results
}

func fetch(ctx context.Context, client *http.Client, url string) ([]model.Proxy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mtproto-proxy-pro/1.0 (+https://github.com/zakky8/mtproto-proxy-pro)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if err != nil {
		return nil, err
	}
	return parse.Text(string(body)), nil
}
