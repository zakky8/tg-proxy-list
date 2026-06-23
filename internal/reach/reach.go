// Package reach tests whether a proxy endpoint is TCP-reachable from *inside*
// censored countries, using the free check-host.net probe network.
//
// This closes the core honesty gap: a proxy can be perfectly alive yet IP-blocked
// in Iran or Russia. A single-location verifier cannot tell the difference; probes
// physically located in those countries can.
//
// Everything here is best-effort and non-fatal. A network error, a missing probe,
// or a rate-limit response yields "untested" — never a false "blocked" verdict.
package reach

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultNodes maps an ISO country code to candidate check-host.net probe nodes.
// Nodes come and go, so each list is a superset; nodes that are offline simply
// don't appear in a check's accepted-node set and are ignored.
var DefaultNodes = map[string][]string{
	"IR": {"ir5.node.check-host.net", "ir7.node.check-host.net", "ir8.node.check-host.net", "ir9.node.check-host.net", "ir2.node.check-host.net"},
	"RU": {"ru1.node.check-host.net", "ru2.node.check-host.net", "ru3.node.check-host.net"},
	"VN": {"vn1.node.check-host.net"},
}

// Target is a proxy endpoint to test.
type Target struct {
	Key  string
	IP   string
	Port int
}

// Result reports which censored countries a target was reachable from.
type Result struct {
	Key       string
	Reachable []string
}

// Options tunes a reachability run.
type Options struct {
	Nodes      map[string][]string
	PaceSec    int           // seconds between check starts (rate-limit safety)
	PollEvery  time.Duration // gap between result polls
	PerCheck   time.Duration // max time to wait for one check's results
	Budget     time.Duration // overall wall-clock budget for the whole run
	HTTPClient *http.Client
	Log        func(format string, args ...any)
}

func (o Options) withDefaults() Options {
	if o.Nodes == nil {
		o.Nodes = DefaultNodes
	}
	if o.PaceSec <= 0 {
		o.PaceSec = 17 // measured safe rate is ~1 check / 15s per source IP
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 3 * time.Second
	}
	if o.PerCheck <= 0 {
		o.PerCheck = 20 * time.Second
	}
	if o.Budget <= 0 {
		o.Budget = 25 * time.Minute
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 25 * time.Second}
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	return o
}

const apiBase = "https://check-host.net"

// Check tests each target serially (rate limits forbid parallelism) and returns
// per-target reachability. Targets are processed until the budget is exhausted.
func Check(ctx context.Context, targets []Target, opts Options) []Result {
	opts = opts.withDefaults()

	// Flatten the candidate node list once.
	var nodes []string
	for _, ns := range opts.Nodes {
		nodes = append(nodes, ns...)
	}
	sort.Strings(nodes)

	deadline := time.Now().Add(opts.Budget)
	out := make([]Result, 0, len(targets))
	tested, reachableCount := 0, 0

	for i, t := range targets {
		if time.Now().After(deadline) {
			opts.Log("reach: budget reached after %d/%d targets", i, len(targets))
			break
		}
		ccs := checkOne(ctx, opts, t, nodes)
		tested++
		if len(ccs) > 0 {
			reachableCount++
		}
		out = append(out, Result{Key: t.Key, Reachable: ccs})

		if i < len(targets)-1 {
			select {
			case <-ctx.Done():
				return out
			case <-time.After(time.Duration(opts.PaceSec) * time.Second):
			}
		}
	}
	opts.Log("reach: tested %d targets, %d reachable from >=1 censored country", tested, reachableCount)
	return out
}

// startResponse is the JSON returned when a check is created.
type startResponse struct {
	OK        json.Number         `json:"ok"`
	RequestID string              `json:"request_id"`
	Nodes     map[string][]string `json:"nodes"` // node -> [cc, country, city, ip, asn]
	Error     string              `json:"error"`
	Permanent string              `json:"permanent_link"`
}

func checkOne(ctx context.Context, opts Options, t Target, nodes []string) []string {
	host := fmt.Sprintf("%s:%d", t.IP, t.Port)

	// Start the check (retry once on rate-limit). sr is declared per-attempt so a
	// partial response can never carry fields over into the next attempt.
	var sr startResponse
	for attempt := 0; attempt < 2; attempt++ {
		var attemptSR startResponse
		q := url.Values{}
		q.Set("host", host)
		u := apiBase + "/check-tcp?" + q.Encode()
		for _, n := range nodes {
			u += "&node=" + n
		}
		body, err := getJSON(ctx, opts.HTTPClient, u)
		if err != nil {
			opts.Log("reach: start error for %s: %v", host, err)
			return nil
		}
		if strings.Contains(string(body), "limit_exceeded") {
			opts.Log("reach: rate-limited, backing off")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(20 * time.Second):
			}
			continue
		}
		if err := json.Unmarshal(body, &attemptSR); err != nil || attemptSR.RequestID == "" {
			opts.Log("reach: bad start response for %s", host)
			return nil
		}
		sr = attemptSR
		break
	}
	if sr.RequestID == "" {
		return nil
	}

	// Map accepted node -> country code.
	nodeCC := map[string]string{}
	for node, info := range sr.Nodes {
		if len(info) > 0 {
			nodeCC[node] = strings.ToUpper(info[0])
		}
	}
	if len(nodeCC) == 0 {
		return nil
	}

	// Poll for results until every accepted node has reported or we time out.
	resURL := apiBase + "/check-result/" + sr.RequestID
	checkDeadline := time.Now().Add(opts.PerCheck)
	connected := map[string]bool{} // cc -> reachable

	for time.Now().Before(checkDeadline) {
		select {
		case <-ctx.Done():
			return ccList(connected)
		case <-time.After(opts.PollEvery):
		}

		body, err := getJSON(ctx, opts.HTTPClient, resURL)
		if err != nil {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			continue
		}

		pending := false
		for node, cc := range nodeCC {
			val, ok := raw[node]
			if !ok || string(val) == "null" {
				pending = true
				continue
			}
			if nodeConnected(val) {
				connected[cc] = true
			}
		}
		if !pending {
			break
		}
	}
	return ccList(connected)
}

// nodeConnected reports whether a check-result node value indicates a successful
// TCP connect. The value is an array whose first element is either
// {"address":...,"time":...} (success) or {"error":...} (failure).
func nodeConnected(val json.RawMessage) bool {
	var arr []struct {
		Address string  `json:"address"`
		Time    float64 `json:"time"`
		Error   string  `json:"error"`
	}
	if err := json.Unmarshal(val, &arr); err != nil || len(arr) == 0 {
		return false
	}
	return arr[0].Error == "" && arr[0].Address != ""
}

func ccList(m map[string]bool) []string {
	var out []string
	for cc := range m {
		out = append(out, cc)
	}
	sort.Strings(out)
	return out
}

func getJSON(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json") // mandatory — else returns HTML
	req.Header.Set("User-Agent", "mtproto-proxy-pro/1.0 (+https://github.com/zakky8/mtproto-proxy-pro)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
