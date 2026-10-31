//go:build !linux

package hostmetrics

import "github.com/openlinkos/beacon/internal/probe"

// Filesystem sizes and time sync come from Linux system calls; elsewhere (development machines) they are
// left out.
func (c *Collector) filesystems() []probe.Sample { return nil }

func (c *Collector) system() []probe.Sample { return nil }
