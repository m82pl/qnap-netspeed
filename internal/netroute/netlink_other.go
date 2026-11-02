//go:build !linux

package netroute

import "net"

// Linux is a stub: netlink exists only on Linux.
type Linux struct{}

func (Linux) IPv4Addrs(iface string) ([]net.IP, error) { return netIfaceAddrs(iface) }
func (Linux) RouteGet(src, dst net.IP) (Route, error)  { return Route{}, ErrUnsupported }
func (Linux) HasSourceRule(src net.IP) (bool, error)   { return false, ErrUnsupported }
