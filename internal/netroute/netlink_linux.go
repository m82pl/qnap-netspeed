//go:build linux

package netroute

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// Netlink constants not exported by package syscall.
const (
	rtaDst     = 1
	rtaOif     = 4
	rtaGateway = 5
	rtaSrc     = 2
	rtaTable   = 15

	fraSrc = 2

	rtmNewRoute = 24
	rtmGetRoute = 26
	rtmNewRule  = 32
	rtmGetRule  = 34

	nlmsgHdrLen = 16
)

// Linux is the real System, backed by a NETLINK_ROUTE socket.
type Linux struct{}

// IPv4Addrs implements System.
func (Linux) IPv4Addrs(iface string) ([]net.IP, error) { return netIfaceAddrs(iface) }

// RouteGet implements System: RTM_GETROUTE with RTA_SRC and RTA_DST makes the
// kernel do the same lookup as `ip route get <dst> from <src>`.
func (Linux) RouteGet(src, dst net.IP) (Route, error) {
	src4, dst4 := src.To4(), dst.To4()
	if dst4 == nil || (src != nil && src4 == nil) {
		return Route{}, fmt.Errorf("only IPv4 is supported")
	}
	body := make([]byte, 12) // struct rtmsg
	body[0] = syscall.AF_INET
	body[1] = 32 // dst_len
	body = append(body, attr(rtaDst, dst4)...)
	if src4 != nil { // a nil src is a plain `ip route get <dst>`
		body[2] = 32
		body = append(body, attr(rtaSrc, src4)...)
	}
	msgs, err := transact(rtmGetRoute, syscall.NLM_F_REQUEST, body)
	if err != nil {
		return Route{}, err
	}
	for _, m := range msgs {
		if m.typ == rtmNewRoute {
			return parseRoute(m.payload, ifaceName)
		}
	}
	return Route{}, fmt.Errorf("no route in kernel reply")
}

// HasSourceRule implements System: a dump of RTM_GETRULE.
func (Linux) HasSourceRule(src net.IP) (bool, error) {
	body := make([]byte, 12) // struct fib_rule_hdr
	body[0] = syscall.AF_INET
	msgs, err := transact(rtmGetRule, syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP, body)
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		if m.typ == rtmNewRule && ruleMatchesSource(m.payload, src) {
			return true, nil
		}
	}
	return false, nil
}

func ifaceName(idx int) string {
	if ifc, err := net.InterfaceByIndex(idx); err == nil {
		return ifc.Name
	}
	return fmt.Sprintf("if%d", idx)
}

type nlMsg struct {
	typ     uint16
	payload []byte
}

func attr(typ uint16, data []byte) []byte {
	l := 4 + len(data)
	b := make([]byte, (l+3)&^3)
	binary.LittleEndian.PutUint16(b[0:], uint16(l))
	binary.LittleEndian.PutUint16(b[2:], typ)
	copy(b[4:], data)
	return b
}

// transact sends one request and collects the reply messages up to NLMSG_DONE.
func transact(typ, flags uint16, body []byte) ([]nlMsg, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	defer syscall.Close(fd)
	tv := syscall.NsecToTimeval(int64(5 * time.Second))
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("netlink bind: %w", err)
	}
	req := make([]byte, nlmsgHdrLen, nlmsgHdrLen+len(body))
	binary.LittleEndian.PutUint32(req[0:], uint32(nlmsgHdrLen+len(body)))
	binary.LittleEndian.PutUint16(req[4:], typ)
	binary.LittleEndian.PutUint16(req[6:], flags)
	binary.LittleEndian.PutUint32(req[8:], uint32(os.Getpid()))
	req = append(req, body...)
	if err := syscall.Sendto(fd, req, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("netlink send: %w", err)
	}
	var out []nlMsg
	buf := make([]byte, 1<<16)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, fmt.Errorf("netlink receive: %w", err)
		}
		msgs, done, err := splitMessages(buf[:n])
		if err != nil {
			return nil, err
		}
		out = append(out, msgs...)
		if done || flags&syscall.NLM_F_DUMP == 0 {
			return out, nil
		}
	}
}

// splitMessages cuts a datagram into messages; done is true at NLMSG_DONE.
func splitMessages(b []byte) (msgs []nlMsg, done bool, err error) {
	for len(b) >= nlmsgHdrLen {
		l := int(binary.LittleEndian.Uint32(b[0:]))
		if l < nlmsgHdrLen || l > len(b) {
			return msgs, done, fmt.Errorf("netlink: bad message length %d", l)
		}
		typ := binary.LittleEndian.Uint16(b[4:])
		switch typ {
		case syscall.NLMSG_DONE:
			return msgs, true, nil
		case syscall.NLMSG_ERROR:
			if l >= nlmsgHdrLen+4 {
				if code := int32(binary.LittleEndian.Uint32(b[nlmsgHdrLen:])); code != 0 {
					return msgs, done, fmt.Errorf("netlink: %w", syscall.Errno(-code))
				}
			}
		default:
			msgs = append(msgs, nlMsg{typ: typ, payload: b[nlmsgHdrLen:l]})
		}
		b = b[(l+3)&^3:]
	}
	return msgs, done, nil
}

// attrs walks the rtattr list that follows a fixed header of hdrLen bytes.
func attrs(payload []byte, hdrLen int, fn func(typ uint16, data []byte)) {
	if len(payload) < hdrLen {
		return
	}
	b := payload[hdrLen:]
	for len(b) >= 4 {
		l := int(binary.LittleEndian.Uint16(b[0:]))
		if l < 4 || l > len(b) {
			return
		}
		fn(binary.LittleEndian.Uint16(b[2:])&0x3fff, b[4:l])
		b = b[(l+3)&^3:]
	}
}

// parseRoute decodes an RTM_NEWROUTE reply (struct rtmsg is 12 bytes).
func parseRoute(payload []byte, name func(int) string) (Route, error) {
	if len(payload) < 12 {
		return Route{}, fmt.Errorf("netlink: short route message")
	}
	r := Route{Table: TableName(uint32(payload[4]))}
	oif := -1
	attrs(payload, 12, func(typ uint16, d []byte) {
		switch {
		case typ == rtaOif && len(d) == 4:
			oif = int(binary.LittleEndian.Uint32(d))
		case typ == rtaGateway && len(d) == 4:
			r.Gateway = net.IP(d).String()
		case typ == rtaTable && len(d) == 4:
			r.Table = TableName(binary.LittleEndian.Uint32(d))
		}
	})
	if oif < 0 {
		return Route{}, fmt.Errorf("netlink: route without output interface")
	}
	r.EgressIface = name(oif)
	return r, nil
}

// ruleMatchesSource decodes an RTM_NEWRULE reply (fib_rule_hdr is 12 bytes)
// and reports whether it selects traffic by a source prefix containing src.
func ruleMatchesSource(payload []byte, src net.IP) bool {
	if len(payload) < 12 || payload[0] != syscall.AF_INET || payload[2] == 0 {
		return false
	}
	srcLen := int(payload[2])
	match := false
	attrs(payload, 12, func(typ uint16, d []byte) {
		if typ == fraSrc && len(d) == 4 {
			n := &net.IPNet{IP: net.IP(d), Mask: net.CIDRMask(srcLen, 32)}
			match = n.Contains(src)
		}
	})
	return match
}
