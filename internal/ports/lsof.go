package ports

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// listSockets uses /proc where it exists (Linux) and lsof elsewhere (macOS,
// for development).
func listSockets() ([]Socket, error) {
	if _, err := os.Stat("/proc/net/tcp"); err == nil {
		return listProc("/proc")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-w", "-iTCP", "-sTCP:LISTEN", "-iUDP", "-F", "pcPn").Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	return parseLsof(strings.NewReader(string(out))), nil
}

// parseLsof reads `lsof -F pcPn` output: p<pid>, c<command>, then per file
// P<proto> and n<addr:port>; connected UDP sockets ("->") are skipped.
func parseLsof(r io.Reader) []Socket {
	var out []Socket
	var pid int
	var comm, proto string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		v := line[1:]
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(v)
			comm = ""
		case 'c':
			comm = v
		case 'P':
			proto = strings.ToLower(v)
		case 'n':
			if strings.Contains(v, "->") || (proto != "tcp" && proto != "udp") {
				continue
			}
			host, port, err := net.SplitHostPort(v)
			if err != nil {
				continue
			}
			n, err := strconv.Atoi(port)
			if err != nil || n == 0 {
				continue
			}
			switch host {
			case "*":
				host = "0.0.0.0"
			case "[::]":
				host = "::"
			}
			out = append(out, Socket{Proto: proto, Addr: strings.Trim(host, "[]"), Port: n, PID: pid, Process: comm})
		}
	}
	return out
}
