package publish

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
)

// Stat tracks how often a proxy was seen and how often it was reachable.
type Stat struct {
	Seen int    `json:"seen"`
	OK   int    `json:"ok"`
	Last string `json:"last"`
}

// History maps a proxy key to its observed Stat across runs.
type History map[string]Stat

// maxHistory caps the state file so it cannot grow without bound.
const maxHistory = 20000

// LoadHistory reads the state file, returning an empty History if it is absent.
func LoadHistory(path string) History {
	h := History{}
	data, err := os.ReadFile(path)
	if err != nil {
		return h
	}
	_ = json.Unmarshal(data, &h)
	return h
}

// Record updates a proxy's stats for the current run.
func (h History) Record(key string, ok bool, now string) {
	s := h[key]
	s.Seen++
	if ok {
		s.OK++
	}
	s.Last = now
	h[key] = s
}

// Pct returns the rounded uptime percentage for a key (0 if never seen).
func (h History) Pct(key string) int {
	s := h[key]
	if s.Seen == 0 {
		return 0
	}
	return int(math.Round(float64(s.OK) / float64(s.Seen) * 100.0))
}

// Save writes the state file, pruning the least-recently/least-seen entries when
// the table exceeds maxHistory.
func (h History) Save(path string) error {
	if len(h) > maxHistory {
		h = h.prune()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (h History) prune() History {
	type kv struct {
		k string
		s Stat
	}
	all := make([]kv, 0, len(h))
	for k, s := range h {
		all = append(all, kv{k, s})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].s.Last != all[j].s.Last {
			return all[i].s.Last > all[j].s.Last // most recent first
		}
		return all[i].s.Seen > all[j].s.Seen
	})
	out := make(History, maxHistory)
	for _, e := range all[:maxHistory] {
		out[e.k] = e.s
	}
	return out
}
