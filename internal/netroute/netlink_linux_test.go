//go:build linux

package netroute

import (
	"encoding/binary"
	"net"
	"syscall"
	"testing"
)

func u32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

func TestParseRoute(t *testing.T) {
	p := make([]byte, 12)
	p[0], p[4] = syscall.AF_INET, 254 // family, table (8 bit)
	p = append(p, attr(rtaOif, u32(3))...)
	p = append(p, attr(rtaGateway, net.IPv4(192, 168, 1, 1).To4())...)
	p = append(p, attr(rtaTable, u32(100))...)
	r, err := parseRoute(p, func(i int) string { return map[int]string{3: "eth1"}[i] })
	if err != nil || r.EgressIface != "eth1" || r.Gateway != "192.168.1.1" || r.Table != "100" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestParseRouteOnLinkAndErrors(t *testing.T) {
	p := make([]byte, 12)
	p = append(p, attr(rtaOif, u32(2))...)
	r, err := parseRoute(p, func(int) string { return "eth0" })
	if err != nil || r.Gateway != "" || r.Table != "0" && r.Table != "main" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := parseRoute(make([]byte, 12), func(int) string { return "" }); err == nil {
		t.Error("want error without oif")
	}
	if _, err := parseRoute([]byte{1, 2}, nil); err == nil {
		t.Error("want error for a short message")
	}
}

func TestRuleMatchesSource(t *testing.T) {
	rule := func(srcLen byte, src string) []byte {
		p := make([]byte, 12)
		p[0], p[2] = syscall.AF_INET, srcLen
		if src != "" {
			p = append(p, attr(fraSrc, net.ParseIP(src).To4())...)
		}
		return p
	}
	ip := net.ParseIP("10.0.2.15")
	for name, tc := range map[string]struct {
		p    []byte
		want bool
	}{
		"exact":   {rule(32, "10.0.2.15"), true},
		"subnet":  {rule(24, "10.0.2.0"), true},
		"other":   {rule(32, "10.0.3.15"), false},
		"no from": {rule(0, ""), false},
		"short":   {[]byte{2, 0, 0}, false},
	} {
		if got := ruleMatchesSource(tc.p, ip); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestSplitMessages(t *testing.T) {
	msg := func(typ uint16, payload []byte) []byte {
		b := make([]byte, 16, 16+len(payload))
		binary.LittleEndian.PutUint32(b, uint32(16+len(payload)))
		binary.LittleEndian.PutUint16(b[4:], typ)
		return append(b, payload...)
	}
	pad := func(b []byte) []byte { return append(b, make([]byte, (4-len(b)%4)%4)...) }
	data := append(pad(msg(24, []byte{1, 2, 3})), msg(syscall.NLMSG_DONE, nil)...)
	msgs, done, err := splitMessages(data)
	if err != nil || !done || len(msgs) != 1 || len(msgs[0].payload) != 3 {
		t.Fatalf("%v %v %+v", err, done, msgs)
	}
	errPayload := u32(uint32(0xffffffff - uint32(syscall.ENETUNREACH) + 1)) // -ENETUNREACH
	if _, _, err := splitMessages(msg(syscall.NLMSG_ERROR, errPayload)); err == nil {
		t.Error("want netlink error")
	}
	if _, _, err := splitMessages([]byte{200, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("want error for bad length")
	}
}

// Live check against the sandbox kernel: the loopback must route to itself.
func TestLinuxRouteGetLoopback(t *testing.T) {
	r, err := Linux{}.RouteGet(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"))
	if err != nil {
		t.Skipf("netlink not usable here: %v", err)
	}
	if r.EgressIface != "lo" {
		t.Errorf("%+v", r)
	}
	if _, err := (Linux{}).HasSourceRule(net.ParseIP("127.0.0.1")); err != nil {
		t.Errorf("rule dump: %v", err)
	}
}
