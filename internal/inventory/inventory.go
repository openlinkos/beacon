// Package inventory checks that every CDN node is serving: a probe location that runs on a CDN node
// names the node's address and the DNS name of its cluster (the rotation), and every minute both are
// resolved. A node whose address is not among the rotation's answers has been taken out (a health
// check, a traffic limit, a mistake), even though the machine is up and still probing; an address in
// the rotation that belongs to no location is a node nobody watches. Both are exported for alert
// rules. Nothing here needs the CDN's API or credentials.
package inventory

import (
	"context"
	"net"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/openlinkos/beacon/internal/catalog"
	"github.com/openlinkos/beacon/internal/metrics"
)

// Checker resolves the rotations and the nodes' addresses. Resolver nil is the system's resolver.
type Checker struct {
	Catalog  func() *catalog.Catalog
	Resolver *net.Resolver
	Now      func() time.Time

	mu      sync.Mutex
	samples []named
}

type named struct {
	name string
	metrics.Sample
}

func (c *Checker) resolver() *net.Resolver {
	if c.Resolver != nil {
		return c.Resolver
	}
	return net.DefaultResolver
}

// lookup resolves name (or returns it, when it is an address) to its IPv4 addresses.
func (c *Checker) lookup(ctx context.Context, name string) ([]string, error) {
	if ip := net.ParseIP(name); ip != nil {
		return []string{ip.String()}, nil
	}
	ips, err := c.resolver().LookupIP(ctx, "ip4", name)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return slices.Compact(out), nil
}

// Check resolves everything once and keeps the result for Samples.
func (c *Checker) Check(ctx context.Context) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	cat := c.Catalog()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	rotations := map[string][]string{} // rotation -> members; nil when it did not resolve
	var out []named
	add := func(name, labels string, v float64) {
		out = append(out, named{name, metrics.Sample{Labels: labels, Value: v}})
	}
	for _, l := range cat.Locations {
		if l.Rotation == "" || !cat.Expected(l, now()) {
			continue
		}
		if _, done := rotations[l.Rotation]; done {
			continue
		}
		members, err := c.lookup(ctx, l.Rotation)
		ok := 1.0
		if err != nil {
			ok = 0
		}
		rotations[l.Rotation] = members
		add("linkos_rotation_resolve_success", metrics.Labels("rotation", l.Rotation), ok)
		add("linkos_rotation_members", metrics.Labels("rotation", l.Rotation), float64(len(members)))
	}
	claimed := map[string]map[string]bool{} // rotation -> addresses of its locations
	for _, l := range cat.Locations {
		members, known := rotations[l.Rotation]
		if l.Rotation == "" || !known || members == nil || !cat.Expected(l, now()) {
			continue
		}
		addrs, err := c.lookup(ctx, l.Address)
		if err != nil {
			continue // the node's own name does not resolve: no verdict
		}
		in := 0.0
		for _, a := range addrs {
			if claimed[l.Rotation] == nil {
				claimed[l.Rotation] = map[string]bool{}
			}
			claimed[l.Rotation][a] = true
			if slices.Contains(members, a) {
				in = 1
			}
		}
		add("linkos_location_in_rotation", metrics.Labels("location", l.ID, "rotation", l.Rotation), in)
	}
	for rot, members := range rotations {
		for _, a := range members {
			if !claimed[rot][a] {
				add("linkos_rotation_unknown_address", metrics.Labels("rotation", rot, "address", a), 1)
			}
		}
	}
	c.mu.Lock()
	c.samples = out
	c.mu.Unlock()
}

// Run checks every minute until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	c.Check(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Check(ctx)
		}
	}
}

// Register exports the last check's results.
func (c *Checker) Register(m *metrics.Metrics) {
	for name, help := range map[string]string{
		"linkos_rotation_resolve_success": "Whether a CDN cluster's DNS name resolved",
		"linkos_rotation_members":         "How many addresses a CDN cluster's DNS name resolved to",
		"linkos_location_in_rotation":     "Whether a probe location's CDN node is among its cluster's DNS answers",
		"linkos_rotation_unknown_address": "An address in a CDN cluster's DNS answers that belongs to no probe location",
	} {
		m.Register(name, help, "gauge", func() []metrics.Sample {
			c.mu.Lock()
			defer c.mu.Unlock()
			var out []metrics.Sample
			for _, s := range c.samples {
				if s.name == name {
					out = append(out, s.Sample)
				}
			}
			return out
		})
	}
}
