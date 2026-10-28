// Package ports lists the TCP ports listening and the UDP ports bound on
// this machine, with the process that owns each one.
package ports

import (
	"net"
	"sort"
)

// Socket is one listening (TCP) or bound (UDP) socket.
type Socket struct {
	Proto   string // tcp | udp
	Addr    string // local address, e.g. 0.0.0.0, ::, 127.0.0.1
	Port    int
	PID     int    // 0 when the owner cannot be determined
	Process string // short process name ("" when unknown)
}

// Port groups the sockets of one process on one proto/port (e.g. the IPv4
// and IPv6 listeners of the same service).
type Port struct {
	Proto   string   `json:"proto"`
	Port    int      `json:"port"`
	Addrs   []string `json:"addrs"`
	Public  bool     `json:"public"` // reachable from other machines (not loopback only)
	PID     int      `json:"pid"`
	Process string   `json:"process"`
}

// List returns the listening ports, sorted by port and protocol.
func List() ([]Port, error) {
	socks, err := listSockets()
	if err != nil {
		return nil, err
	}
	return Group(socks), nil
}

// Group merges sockets that share proto, port and owner.
func Group(socks []Socket) []Port {
	type key struct {
		proto string
		port  int
		pid   int
	}
	idx := map[key]int{}
	var out []Port
	for _, s := range socks {
		k := key{s.Proto, s.Port, s.PID}
		i, ok := idx[k]
		if !ok {
			i = len(out)
			idx[k] = i
			out = append(out, Port{Proto: s.Proto, Port: s.Port, PID: s.PID, Process: s.Process})
		}
		p := &out[i]
		dup := false
		for _, a := range p.Addrs {
			dup = dup || a == s.Addr
		}
		if !dup {
			p.Addrs = append(p.Addrs, s.Addr)
		}
		if ip := net.ParseIP(s.Addr); ip == nil || !ip.IsLoopback() {
			p.Public = true
		}
		if p.Process == "" {
			p.Process = s.Process
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		return out[i].PID < out[j].PID
	})
	return out
}
