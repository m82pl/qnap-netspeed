package netroute

import (
	"errors"
	"net"
	"testing"
)

type fakeSys struct {
	addrs   map[string][]net.IP
	route   Route
	routeEr error
	rule    bool
	ruleErr error
	gotSrc  net.IP
}

func (f *fakeSys) IPv4Addrs(i string) ([]net.IP, error) {
	a, ok := f.addrs[i]
	if !ok {
		return nil, errors.New("no such network interface")
	}
	return a, nil
}
func (f *fakeSys) RouteGet(src, dst net.IP) (Route, error) { f.gotSrc = src; return f.route, f.routeEr }
func (f *fakeSys) HasSourceRule(net.IP) (bool, error)      { return f.rule, f.ruleErr }

var probe = net.ParseIP("1.1.1.1")

func TestInspectOK(t *testing.T) {
	f := &fakeSys{addrs: map[string][]net.IP{"eth0": {net.ParseIP("192.168.1.10")}},
		route: Route{EgressIface: "eth0", Gateway: "192.168.1.1", Table: "main"}}
	c := Inspect(f, "eth0", probe)
	if c.Route == nil || c.Route.Mismatch || c.Address.String() != "192.168.1.10" || c.Reason != "" {
		t.Fatalf("%+v", c)
	}
}

func TestInspectMismatchAndRule(t *testing.T) {
	f := &fakeSys{addrs: map[string][]net.IP{"eth1": {net.ParseIP("10.0.0.5")}},
		route: Route{EgressIface: "eth0", Table: "main"}, rule: true}
	c := Inspect(f, "eth1", probe)
	if !c.Route.Mismatch || !c.Route.PolicyRule {
		t.Fatalf("%+v", c.Route)
	}
}

func TestInspectSkipsLinkLocalAndLoopback(t *testing.T) {
	f := &fakeSys{addrs: map[string][]net.IP{"eth1": {net.ParseIP("169.254.3.4"), net.ParseIP("127.0.0.1")}}}
	c := Inspect(f, "eth1", probe)
	if c.Reason != ReasonNoIPv4 || c.Route != nil {
		t.Fatalf("%+v", c)
	}
}

func TestInspectPicksFirstGlobal(t *testing.T) {
	f := &fakeSys{addrs: map[string][]net.IP{"eth1": {net.ParseIP("169.254.3.4"), net.ParseIP("10.1.1.1"), net.ParseIP("10.1.1.2")}},
		route: Route{EgressIface: "eth1"}}
	Inspect(f, "eth1", probe)
	if f.gotSrc.String() != "10.1.1.1" {
		t.Errorf("source %v", f.gotSrc)
	}
}

func TestInspectNoInterface(t *testing.T) {
	c := Inspect(&fakeSys{addrs: map[string][]net.IP{}}, "eth9", probe)
	if c.Reason != ReasonNoIface || c.Err == nil {
		t.Fatalf("%+v", c)
	}
}

func TestInspectLookupFailureKeepsAddress(t *testing.T) {
	f := &fakeSys{addrs: map[string][]net.IP{"eth0": {net.ParseIP("192.168.1.10")}}, routeEr: errors.New("network is unreachable")}
	c := Inspect(f, "eth0", probe)
	if c.Err == nil || c.Route != nil || c.Address == nil || c.Reason != "" {
		t.Fatalf("%+v", c)
	}
}

func TestInspectRuleErrorIsIgnored(t *testing.T) {
	f := &fakeSys{addrs: map[string][]net.IP{"eth0": {net.ParseIP("192.168.1.10")}},
		route: Route{EgressIface: "eth0"}, ruleErr: errors.New("boom")}
	if c := Inspect(f, "eth0", probe); c.Route == nil || c.Route.PolicyRule {
		t.Fatalf("%+v", c)
	}
}

func TestTableName(t *testing.T) {
	for n, want := range map[uint32]string{254: "main", 255: "local", 253: "default", 100: "100"} {
		if got := TableName(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
}
