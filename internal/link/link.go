// Package link reads the state of network interfaces from /sys/class/net
// (spec pkt 5.3): operstate, speed, duplex, MTU, bridge/bond membership.
// It takes an fs.FS rooted at /sys/class/net, so tests use fstest.MapFS.
package link

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Interface kinds.
const (
	KindPhysical = "physical"
	KindBridge   = "bridge"
	KindBond     = "bond"
	KindVirtual  = "virtual"
)

// Interface is one entry of link.jsonl (spec pkt 13.3).
type Interface struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Operstate    string   `json:"operstate"`
	SpeedMbps    *int     `json:"speed_mbps"` // null: the driver does not report (-1) or the read failed
	ExpectedMbps *int     `json:"expected_mbps"`
	Duplex       string   `json:"duplex"`
	MTU          int      `json:"mtu"`
	Master       string   `json:"master,omitempty"`
	Driver       string   `json:"driver,omitempty"`
	MAC          string   `json:"mac,omitempty"`
	Members      []string `json:"members,omitempty"`
	BondMode     string   `json:"bond_mode,omitempty"`
	carrier      string
}

// Physical is the physical interface behind a logical one (spec pkt 5.3).
type Physical struct {
	Name      string `json:"name"`
	SpeedMbps *int   `json:"speed_mbps"`
	Duplex    string `json:"duplex"`
	MTU       int    `json:"mtu"`
}

// Record is a link.jsonl line.
type Record struct {
	Schema     int         `json:"schema"`
	Timestamp  time.Time   `json:"timestamp"`
	Type       string      `json:"type"`
	Interfaces []Interface `json:"interfaces"`
	Warnings   []string    `json:"warnings"`
}

// Read lists all interfaces, sorted by name.
func Read(fsys fs.FS) ([]Interface, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	var out []Interface
	for _, e := range entries {
		if e.Name() == "bonding_masters" { // a file in /sys/class/net, not an interface
			continue
		}
		out = append(out, readOne(fsys, e.Name()))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ReadOne reads a single interface.
func ReadOne(fsys fs.FS, name string) (Interface, error) {
	if _, err := fs.Stat(fsys, name); err != nil {
		return Interface{}, fmt.Errorf("interface %s: not found", name)
	}
	return readOne(fsys, name), nil
}

func readOne(fsys fs.FS, name string) Interface {
	i := Interface{Name: name, Kind: KindVirtual}
	i.Operstate = str(fsys, name, "operstate")
	i.carrier = str(fsys, name, "carrier")
	i.Duplex = str(fsys, name, "duplex")
	i.MAC = str(fsys, name, "address")
	i.MTU, _ = strconv.Atoi(str(fsys, name, "mtu"))
	// `speed` reads fail (EINVAL) on an interface that is down, and some
	// drivers return -1: both mean "unknown", never a speed (plan W6).
	if v, err := strconv.Atoi(str(fsys, name, "speed")); err == nil && v > 0 {
		i.SpeedMbps = &v
	}
	if t, err := fs.ReadLink(fsys, path.Join(name, "master")); err == nil {
		i.Master = path.Base(t)
	}
	if t, err := fs.ReadLink(fsys, path.Join(name, "device/driver")); err == nil {
		i.Driver = path.Base(t)
	}
	switch {
	case exists(fsys, path.Join(name, "bridge")):
		i.Kind = KindBridge
		if es, err := fs.ReadDir(fsys, path.Join(name, "brif")); err == nil {
			for _, e := range es {
				i.Members = append(i.Members, e.Name())
			}
			sort.Strings(i.Members)
		}
	case exists(fsys, path.Join(name, "bonding")):
		i.Kind = KindBond
		i.Members = strings.Fields(str(fsys, name, "bonding/slaves"))
		sort.Strings(i.Members)
		i.BondMode = str(fsys, name, "bonding/mode")
	case exists(fsys, path.Join(name, "device")):
		i.Kind = KindPhysical
	}
	return i
}

func exists(fsys fs.FS, p string) bool { _, err := fs.Stat(fsys, p); return err == nil }

func str(fsys fs.FS, dir, file string) string {
	b, err := fs.ReadFile(fsys, path.Join(dir, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ParseExpect parses LINK_EXPECT ("eth0:2500 eth1:10000") into a map.
func ParseExpect(v string) map[string]int {
	m := map[string]int{}
	for _, f := range strings.Fields(v) {
		if name, sp, ok := strings.Cut(f, ":"); ok {
			if n, err := strconv.Atoi(sp); err == nil {
				m[name] = n
			}
		}
	}
	return m
}

// Resolve descends through bridges and bonds to the physical interfaces
// behind name. A physical interface resolves to itself. The second result is
// the summed speed for a bond (nil if no member reports one).
func Resolve(fsys fs.FS, name string) ([]Physical, *int) {
	var out []Physical
	seen := map[string]bool{}
	var walk func(n string, depth int)
	walk = func(n string, depth int) {
		if seen[n] || depth > 8 {
			return
		}
		seen[n] = true
		i, err := ReadOne(fsys, n)
		if err != nil {
			return
		}
		if len(i.Members) > 0 && (i.Kind == KindBridge || i.Kind == KindBond) {
			for _, m := range i.Members {
				walk(m, depth+1)
			}
			return
		}
		out = append(out, Physical{Name: i.Name, SpeedMbps: i.SpeedMbps, Duplex: i.Duplex, MTU: i.MTU})
	}
	walk(name, 0)
	var sum *int
	if i, err := ReadOne(fsys, name); err == nil && i.Kind == KindBond {
		total, any := 0, false
		for _, p := range out {
			if p.SpeedMbps != nil {
				total += *p.SpeedMbps
				any = true
			}
		}
		if any {
			sum = &total
		}
	}
	return out, sum
}

// Evaluate fills ExpectedMbps and returns the warnings (spec pkt 5.3):
// link_degraded:IF, duplex_half:IF, link_down:IF, mtu_mismatch:BRIDGE.
// Without an expectation the speed is only reported.
func Evaluate(ifaces []Interface, expect map[string]int) []string {
	warn := []string{}
	byName := map[string]*Interface{}
	for k := range ifaces {
		byName[ifaces[k].Name] = &ifaces[k]
	}
	for k := range ifaces {
		i := &ifaces[k]
		if want, ok := expect[i.Name]; ok {
			w := want
			i.ExpectedMbps = &w
			switch {
			case i.carrier == "0" || (i.carrier == "" && i.Operstate == "down"):
				warn = append(warn, "link_down:"+i.Name)
			case i.SpeedMbps != nil && *i.SpeedMbps < want:
				warn = append(warn, "link_degraded:"+i.Name)
			}
		}
		if i.Kind == KindPhysical && i.Duplex == "half" {
			warn = append(warn, "duplex_half:"+i.Name)
		}
		if i.Kind == KindBridge || i.Kind == KindBond {
			for _, m := range i.Members {
				if mm := byName[m]; mm != nil && mm.MTU != i.MTU {
					warn = append(warn, "mtu_mismatch:"+i.Name)
					break
				}
			}
		}
	}
	sort.Strings(warn)
	return warn
}
