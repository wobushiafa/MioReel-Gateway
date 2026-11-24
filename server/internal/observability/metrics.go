package observability

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
)

var durationBucketsMs = []float64{50, 100, 250, 500, 1000, 2500, 5000, 10000, 15000, 30000}

// Metrics tracks gateway counters, histograms, and gauges per docs/01-architecture.md §8.2.
type Metrics struct {
	mu               sync.RWMutex
	resolveTotal     map[int]uint64
	upstreamRequests map[string]uint64 // key: route + "\x00" + code
	bucketCounts     []uint64
	durationSumMs    float64
	durationCount    uint64

	upstreamFallbackTotal atomic.Uint64
	profileRotatedTs      atomic.Int64
	cacheHits             atomic.Uint64
	cacheMisses           atomic.Uint64
}

func NewMetrics() *Metrics {
	return &Metrics{
		resolveTotal:     make(map[int]uint64),
		upstreamRequests: make(map[string]uint64),
		bucketCounts:     make([]uint64, len(durationBucketsMs)+1),
	}
}

func (m *Metrics) ObserveResolve(code int, durationMs float64, cached bool) {
	m.mu.Lock()
	m.resolveTotal[code]++
	m.durationSumMs += durationMs
	m.durationCount++
	placed := false
	for i, bound := range durationBucketsMs {
		if durationMs <= bound {
			m.bucketCounts[i]++
			placed = true
			break
		}
	}
	if !placed {
		m.bucketCounts[len(durationBucketsMs)]++
	}
	m.mu.Unlock()

	if cached {
		m.cacheHits.Add(1)
	} else {
		m.cacheMisses.Add(1)
	}
}

func (m *Metrics) IncUpstreamRequest(route, code string) {
	m.mu.Lock()
	m.upstreamRequests[route+"\x00"+code]++
	m.mu.Unlock()
}

func (m *Metrics) IncUpstreamFallback() {
	m.upstreamFallbackTotal.Add(1)
}

func (m *Metrics) SetProfileRotatedTimestamp(unixSec int64) {
	m.profileRotatedTs.Store(unixSec)
}

func (m *Metrics) CacheHitRatio() float64 {
	hits := m.cacheHits.Load()
	misses := m.cacheMisses.Load()
	total := hits + misses
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// WritePrometheus writes all metrics in Prometheus text exposition format.
func (m *Metrics) WritePrometheus(w io.Writer) {
	m.mu.RLock()
	codes := make([]int, 0, len(m.resolveTotal))
	for c := range m.resolveTotal {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	resolveSnap := make(map[int]uint64, len(codes))
	for _, c := range codes {
		resolveSnap[c] = m.resolveTotal[c]
	}

	upKeys := make([]string, 0, len(m.upstreamRequests))
	for k := range m.upstreamRequests {
		upKeys = append(upKeys, k)
	}
	sort.Strings(upKeys)
	upSnap := make(map[string]uint64, len(upKeys))
	for _, k := range upKeys {
		upSnap[k] = m.upstreamRequests[k]
	}

	buckets := append([]uint64(nil), m.bucketCounts...)
	durSum := m.durationSumMs
	durCnt := m.durationCount
	m.mu.RUnlock()

	fmt.Fprintln(w, "# TYPE resolve_total counter")
	for _, c := range codes {
		fmt.Fprintf(w, "resolve_total{code=\"%d\"} %d\n", c, resolveSnap[c])
	}

	fmt.Fprintln(w, "# TYPE resolve_duration_ms histogram")
	var cumulative uint64
	for i, bound := range durationBucketsMs {
		cumulative += buckets[i]
		fmt.Fprintf(w, "resolve_duration_ms_bucket{le=\"%.0f\"} %d\n", bound, cumulative)
	}
	cumulative += buckets[len(durationBucketsMs)]
	fmt.Fprintf(w, "resolve_duration_ms_bucket{le=\"+Inf\"} %d\n", cumulative)
	fmt.Fprintf(w, "resolve_duration_ms_sum %.3f\n", durSum)
	fmt.Fprintf(w, "resolve_duration_ms_count %d\n", durCnt)

	fmt.Fprintln(w, "# TYPE upstream_request_total counter")
	for _, k := range upKeys {
		var route, code string
		for i := 0; i < len(k); i++ {
			if k[i] == 0 {
				route, code = k[:i], k[i+1:]
				break
			}
		}
		fmt.Fprintf(w, "upstream_request_total{route=%q,code=%q} %d\n", route, code, upSnap[k])
	}

	fmt.Fprintln(w, "# TYPE upstream_fallback_total counter")
	fmt.Fprintf(w, "upstream_fallback_total %d\n", m.upstreamFallbackTotal.Load())

	fmt.Fprintln(w, "# TYPE profile_rotated_timestamp gauge")
	fmt.Fprintf(w, "profile_rotated_timestamp %d\n", m.profileRotatedTs.Load())

	fmt.Fprintln(w, "# TYPE cache_hit_ratio gauge")
	fmt.Fprintf(w, "cache_hit_ratio %.6f\n", m.CacheHitRatio())
}
