// Package netroute answers "which interface would traffic from this address
// leave through" (spec pkt 5.1.1), like `ip route get <dst> from <src>` and
// `ip rule`, using netlink directly (no external `ip`, which BusyBox lacks).
package netroute

import (
	"errors"
	"fmt"
	"net"
)

// Route is the result of a route lookup.
type Route struct {
	EgressIface string `json:"egress_iface"`
	Gateway     string `json:"gateway"` // "" when the destination is on-link
	Table       string `json:"table"`
	PolicyRule  bool   `json:"policy_rule"` // an `ip rule from <address>` applies
	Mismatch    bool   `json:"mismatch"`
}

// System is the part of the OS this package needs; tests replace it.
type System interface {
	// IPv4Addrs lists the IPv4 addresses of an interface.
	IPv4Addrs(iface string) ([]net.IP, error)
	// RouteGet is `ip route get <dst> from <src>`; src may be nil.
	RouteGet(src, dst net.IP) (Route, error)
	// HasSourceRule reports whether an `ip rule` with a "from" selector matches src.
	HasSourceRule(src net.IP) (bool, error)
}

// Masterer is an optional System capability: the bridge or bond an interface
// is a member of ("" if none). It lets a "no IPv4" answer name the interface
// that actually holds the address (on QNAP, eth0 is a member of qvs0).
type Masterer interface{ Master(iface string) string }

// Reasons why an interface cannot be tested.
const (
	ReasonNoIPv4  = "no_ipv4"
	ReasonNoIface = "no_iface"
)

// Check is the outcome of the pre-test route check for one interface.
type Check struct {
	Iface   string
	Address net.IP // nil when the interface has no usable IPv4 address
	Route   *Route // nil when Address is nil or the lookup failed
	Reason  string // ReasonNoIPv4 / ReasonNoIface when the interface cannot be tested
	Err     error  // the underlying failure, if any
	Master  string // bridge or bond the interface belongs to, set with ReasonNoIPv4
}

// Inspect runs the steps of spec pkt 5.1.1 for iface and probe address.
func Inspect(sys System, iface string, probe net.IP) Check {
	c := Check{Iface: iface}
	addrs, err := sys.IPv4Addrs(iface)
	if err != nil {
		c.Reason = ReasonNoIface
		c.Err = err
		return c
	}
	c.Address = FirstGlobal(addrs)
	if c.Address == nil {
		c.Reason = ReasonNoIPv4
		if m, ok := sys.(Masterer); ok {
			c.Master = m.Master(iface)
		}
		return c
	}
	r, err := sys.RouteGet(c.Address, probe)
	if err != nil {
		c.Err = fmt.Errorf("route lookup from %s to %s: %w", c.Address, probe, err)
		return c
	}
	if rule, rerr := sys.HasSourceRule(c.Address); rerr == nil {
		r.PolicyRule = rule
	}
	r.Mismatch = r.EgressIface != iface
	c.Route = &r
	return c
}

// FirstGlobal returns the first IPv4 address that is not loopback or link-local.
func FirstGlobal(addrs []net.IP) net.IP {
	for _, a := range addrs {
		if a4 := a.To4(); a4 != nil && !a4.IsLoopback() && !a4.IsLinkLocalUnicast() && !a4.IsUnspecified() {
			return a4
		}
	}
	return nil
}

// TableName names the well-known routing tables, else prints the number.
func TableName(n uint32) string {
	switch n {
	case 253:
		return "default"
	case 254:
		return "main"
	case 255:
		return "local"
	}
	return fmt.Sprintf("%d", n)
}

// ErrUnsupported is returned on systems without netlink.
var ErrUnsupported = errors.New("netlink is not available on this system")

// netIfaceAddrs lists IPv4 addresses with the standard library.
func netIfaceAddrs(iface string) ([]net.IP, error) {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			out = append(out, n.IP)
		}
	}
	return out, nil
}
