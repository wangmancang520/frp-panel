package ports

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const tcp4 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1B58 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1D4C 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1002 1 0000000000000000 100 0 0 10 0
   2: 0F0F000A:1B58 0F0F000B:C350 01 00000000:00000000 00:00000000 00000000     0        0 1003 1 0000000000000000 20 4 30 10 -1
`

const tcp6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:1DB0 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2001 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:0016 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2002 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 2003 1 0000000000000000 100 0 0 10 0
`

const udp4 = `   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
   10: 00000000:1B5A 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 3001 2 0000000000000000 0
   11: 0F0F000A:D431 08080808:0035 01 00000000:00000000 00:00000000 00000000     0        0 3002 2 0000000000000000 0
`

func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "net"), 0o755))
	must(os.WriteFile(filepath.Join(root, "net", "tcp"), []byte(tcp4), 0o644))
	must(os.WriteFile(filepath.Join(root, "net", "tcp6"), []byte(tcp6), 0o644))
	must(os.WriteFile(filepath.Join(root, "net", "udp"), []byte(udp4), 0o644))
	proc := func(pid, comm string, inodes ...string) {
		dir := filepath.Join(root, pid)
		must(os.MkdirAll(filepath.Join(dir, "fd"), 0o755))
		must(os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644))
		must(os.Symlink("/dev/null", filepath.Join(dir, "fd", "0")))
		for i, in := range inodes {
			must(os.Symlink("socket:["+in+"]", filepath.Join(dir, "fd", string(rune('3'+i)))))
		}
	}
	proc("100", "frps", "1001", "1002", "3001")
	proc("200", "frp-panel", "2001")
	proc("300", "sshd", "2002")
	return root
}

func TestListProc(t *testing.T) {
	socks, err := listProc(fakeProc(t))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Port{}
	for _, p := range Group(socks) {
		got[p.Proto+"/"+itoa(p.Port)] = p
	}
	want := []struct {
		key, proc, addr string
		pid             int
		public          bool
	}{
		{"tcp/7000", "frps", "0.0.0.0", 100, true},
		{"tcp/7500", "frps", "127.0.0.1", 100, false},
		{"tcp/7600", "frp-panel", "::", 200, true},
		{"tcp/22", "sshd", "::1", 300, false},
		{"tcp/8080", "", "127.0.0.1", 0, false}, // IPv4-mapped, owner unknown
		{"udp/7002", "frps", "0.0.0.0", 100, true},
	}
	for _, w := range want {
		p, ok := got[w.key]
		if !ok {
			t.Errorf("%s missing", w.key)
			continue
		}
		if p.Process != w.proc || p.PID != w.pid || p.Public != w.public || strings.Join(p.Addrs, ",") != w.addr {
			t.Errorf("%s = %+v, want proc %q pid %d addr %s public %v", w.key, p, w.proc, w.pid, w.addr, w.public)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d ports, want %d (established TCP / connected UDP must be skipped): %v", len(got), len(want), got)
	}
}

func TestGroupMergesFamilies(t *testing.T) {
	ps := Group([]Socket{
		{Proto: "tcp", Addr: "127.0.0.1", Port: 80, PID: 5, Process: "nginx"},
		{Proto: "tcp", Addr: "::", Port: 80, PID: 5, Process: "nginx"},
		{Proto: "tcp", Addr: "::", Port: 80, PID: 6, Process: "other"},
	})
	if len(ps) != 2 || len(ps[0].Addrs) != 2 || !ps[0].Public {
		t.Errorf("unexpected grouping: %+v", ps)
	}
}

func TestParseLsof(t *testing.T) {
	in := "p42\ncfrps\nPTCP\nn*:7000\nPUDP\nn127.0.0.1:5353\nPUDP\nn10.0.0.2:5000->1.1.1.1:53\np7\ncsshd\nPTCP\nn[::1]:22\n"
	s := parseLsof(strings.NewReader(in))
	if len(s) != 3 || s[0].Addr != "0.0.0.0" || s[0].Process != "frps" || s[2].Addr != "::1" || s[2].PID != 7 {
		t.Errorf("unexpected: %+v", s)
	}
}

func itoa(n int) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
