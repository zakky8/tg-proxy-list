// Package geo maps an IPv4 address to an ISO-3166 alpha-2 country code using the
// public DB-IP lite dataset (redistributed by sapics/ip-location-db, CC-BY-4.0).
//
// The dataset is a CSV of "start_ip,end_ip,country" rows sorted by start_ip; we
// load it into a sorted slice and answer lookups with a binary search.
package geo

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DatasetURL is the upstream CSV (downloaded once if the local copy is missing).
const DatasetURL = "https://raw.githubusercontent.com/sapics/ip-location-db/main/dbip-country/dbip-country-ipv4.csv"

const unknown = "??"

type rng struct {
	start, end uint32
	cc         string
}

// DB is an in-memory IPv4 → country lookup table.
type DB struct {
	ranges []rng
}

// Load returns a DB from path, downloading the dataset to path first if absent.
// On any failure it returns a non-nil but empty DB whose Lookup always yields "??",
// so geo is best-effort and never fatal to the pipeline.
func Load(ctx context.Context, path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		if derr := download(ctx, DatasetURL, path); derr != nil {
			return &DB{}, fmt.Errorf("geo dataset unavailable: %w", derr)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return &DB{}, err
	}
	defer f.Close()
	return parse(f)
}

func parse(r io.Reader) (*DB, error) {
	db := &DB{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		a := strings.Split(line, ",")
		if len(a) != 3 {
			continue
		}
		start, ok1 := ipToUint32(a[0])
		end, ok2 := ipToUint32(a[1])
		cc := strings.ToUpper(strings.TrimSpace(a[2]))
		if !ok1 || !ok2 || len(cc) != 2 {
			continue
		}
		db.ranges = append(db.ranges, rng{start, end, cc})
	}
	if err := sc.Err(); err != nil {
		return db, err
	}
	sort.Slice(db.ranges, func(i, j int) bool { return db.ranges[i].start < db.ranges[j].start })
	return db, nil
}

// LookupIP returns the country code for an IP, or "??" if unknown / not IPv4.
func (db *DB) LookupIP(ip net.IP) string {
	v4 := ip.To4()
	if v4 == nil || len(db.ranges) == 0 {
		return unknown
	}
	n := uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
	// Find the last range whose start <= n, then range-check end. This relies only
	// on ranges being sorted by start, so it stays correct even if a malformed
	// dataset contains overlapping or nested ranges.
	i := sort.Search(len(db.ranges), func(i int) bool { return db.ranges[i].start > n }) - 1
	if i >= 0 && n <= db.ranges[i].end {
		return db.ranges[i].cc
	}
	return unknown
}

func ipToUint32(s string) (uint32, bool) {
	ip := net.ParseIP(strings.TrimSpace(s)).To4()
	if ip == nil {
		return 0, false
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3]), true
}

func download(ctx context.Context, url, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}
