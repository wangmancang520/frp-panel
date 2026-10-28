package ports

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Linux: /proc/net/{tcp,tcp6,udp,udp6} list the sockets with their inode;
// /proc/<pid>/fd links ("socket:[inode]") map inodes to processes.

const (
	tcpListen = "0A"
	udpClose  = "07" // an unconnected, bound UDP socket
)

// parseProcNet reads one /proc/net table and returns the listening sockets
// keyed by inode.
func parseProcNet(r io.Reader, proto string) (map[string]Socket, error) {
	out := map[string]Socket{}
	sc := bufio.NewScanner(r)
	first := true
	for sc.Scan() {
		if first { // header
			first = false
			continue
		}
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		local, remote, state, inode := f[1], f[2], f[3], f[9]
		switch proto {
		case "tcp":
			if state != tcpListen {
				continue
			}
		case "udp":
			if state != udpClose || !zeroAddr(remote) {
				continue
			}
		}
		ip, port, err := decodeAddr(local)
		if err != nil || port == 0 {
			continue
		}
		out[inode] = Socket{Proto: proto, Addr: ip.String(), Port: port}
	}
	return out, sc.Err()
}

func zeroAddr(a string) bool { return strings.Trim(strings.Replace(a, ":", "", 1), "0") == "" }

// decodeAddr parses "0100007F:1F90" (IPv4) or a 32-hex-digit IPv6 address;
// the kernel prints each 32-bit word in host (little-endian) byte order.
func decodeAddr(s string) (net.IP, int, error) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil {
		return nil, 0, err
	}
	b, err := hex.DecodeString(h)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil, 0, fmt.Errorf("bad address %q", s)
	}
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	ip := net.IP(b)
	if v4 := ip.To4(); v4 != nil && len(b) == 16 && !ip.Equal(net.IPv6zero) {
		ip = v4 // IPv4-mapped
	}
	return ip, int(port), nil
}

// procOwners maps socket inodes to their process. Unreadable processes
// (not root) are skipped.
func procOwners(root string, want map[string]bool) map[string]Socket {
	out := map[string]Socket{}
	dirs, _ := os.ReadDir(root)
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(root, d.Name(), "fd"))
		if err != nil {
			continue
		}
		comm := ""
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(root, d.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
			if !want[inode] {
				continue
			}
			if comm == "" {
				b, _ := os.ReadFile(filepath.Join(root, d.Name(), "comm"))
				comm = strings.TrimSpace(string(b))
			}
			out[inode] = Socket{PID: pid, Process: comm}
		}
	}
	return out
}

func listProc(root string) ([]Socket, error) {
	byInode := map[string]Socket{}
	found := false
	for _, t := range []struct{ file, proto string }{{"tcp", "tcp"}, {"tcp6", "tcp"}, {"udp", "udp"}, {"udp6", "udp"}} {
		f, err := os.Open(filepath.Join(root, "net", t.file))
		if err != nil {
			continue
		}
		m, err := parseProcNet(f, t.proto)
		f.Close()
		if err != nil {
			return nil, err
		}
		found = true
		for k, v := range m {
			byInode[k] = v
		}
	}
	if !found {
		return nil, fmt.Errorf("%s/net is not readable", root)
	}
	want := map[string]bool{}
	for k := range byInode {
		want[k] = true
	}
	owners := procOwners(root, want)
	out := make([]Socket, 0, len(byInode))
	for inode, s := range byInode {
		if o, ok := owners[inode]; ok {
			s.PID, s.Process = o.PID, o.Process
		}
		out = append(out, s)
	}
	return out, nil
}
