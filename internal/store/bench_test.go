package store

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// BenchmarkUseAndAudit measures what a secret use adds once the vault's packages land: a
// credential check (here a token verification, the read the store already serves) and, in its own
// transaction, the audit record of it — record first, then give. The database is opened with the
// defaults a deployment keeps (WAL, synchronous FULL), so every commit is on the disk before it
// answers; the numbers are the machine's own.
func BenchmarkUseAndAudit(b *testing.B) {
	path := filepath.Join(b.TempDir(), "beacon.db")
	st, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	secret, _, err := st.CreateToken("bench", []string{"status:read"}, 24*time.Hour, "bench")
	if err != nil {
		b.Fatal(err)
	}
	// warm the caches the way a running service has them
	for range 10 {
		if _, ok := st.VerifyToken(secret); !ok {
			b.Fatal("the token does not verify")
		}
		if err := st.Audit(AuditEntry{Actor: "warmup", Via: "bench", Action: "use"}); err != nil {
			b.Fatal(err)
		}
	}
	lat := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for range b.N {
		start := time.Now()
		if _, ok := st.VerifyToken(secret); !ok {
			b.Fatal("the token does not verify")
		}
		if err := st.Audit(AuditEntry{Actor: "bench", Via: "bench", Action: "use", Target: "an-item", Detail: "benchmark"}); err != nil {
			b.Fatal(err)
		}
		lat = append(lat, time.Since(start))
	}
	b.StopTimer()
	slices.Sort(lat)
	pct := func(p float64) time.Duration {
		i := int(float64(len(lat)-1) * p)
		return lat[i]
	}
	b.ReportMetric(float64(pct(0.50).Nanoseconds())/1e6, "p50-ms")
	b.ReportMetric(float64(pct(0.99).Nanoseconds())/1e6, "p99-ms")
	b.ReportMetric(float64(lat[len(lat)-1].Nanoseconds())/1e6, "max-ms")
	fmt.Printf("use+audit over %d rounds: p50 %v p99 %v max %v (synchronous FULL, WAL)\n",
		len(lat), pct(0.50), pct(0.99), lat[len(lat)-1])
}
