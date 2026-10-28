package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"frp-panel/internal/frp"
	"frp-panel/internal/ports"
)

// portRow is a listening port as shown on the overview page.
type portRow struct {
	ports.Port
	Owner string `json:"owner"` // panel | frps | other
	Role  string `json:"role"`  // what the port is for (panel / frps only)
}

// portRoles explains what each frps / panel port is for, keyed "tcp/7000".
func (a *App) portRoles() (frpsRoles, panelRoles map[string]string) {
	frpsRoles, panelRoles = map[string]string{}, map[string]string{}
	key := func(proto string, port int) string { return proto + "/" + strconv.Itoa(port) }
	panelRoles[key("tcp", a.panelPort())] = "Web 面板（HTTPS）"
	panelRoles[key("tcp", 80)] = "Let's Encrypt 验证 / HTTP 跳转"

	d, err := a.loadDoc()
	if err != nil {
		return
	}
	s := frp.ReadServer(d)
	for _, r := range []struct {
		proto string
		port  int
		label string
	}{
		{"tcp", s.BindPort, "frpc 连接端口"},
		{"udp", s.KCPBindPort, "frpc 连接端口（KCP）"},
		{"udp", s.QUICBindPort, "frpc 连接端口（QUIC）"},
		{"tcp", s.VhostHTTPPort, "HTTP 域名转发"},
		{"tcp", s.VhostHTTPSPort, "HTTPS 域名转发"},
	} {
		if r.port > 0 {
			frpsRoles[key(r.proto, r.port)] = r.label
		}
	}
	for p, label := range frp.TCPPorts(d) { // e.g. tcpmux
		if _, ok := frpsRoles[key("tcp", p)]; !ok {
			frpsRoles[key("tcp", p)] = label
		}
	}
	api := frp.AdminFromDoc(d)
	if u, err := url.Parse(api.Base); err == nil {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			frpsRoles[key("tcp", p)] = "frps 管理接口（仅供面板调用）"
		}
	}
	// Proxy remote ports, named after the proxy and its managed client.
	if ps, err := api.ServerProxies(); err == nil {
		names := map[string]string{}
		for _, c := range a.Fleet.List() {
			names[c.ID] = c.Name
		}
		for _, p := range ps {
			rp := 0
			switch v := p.Conf["remotePort"].(type) {
			case float64:
				rp = int(v)
			}
			if rp == 0 || (p.Type != "tcp" && p.Type != "udp") {
				continue
			}
			label := "代理 " + p.Name
			if id, rest, ok := strings.Cut(p.Name, "."); ok && names[id] != "" {
				label = fmt.Sprintf("代理 %s · %s", rest, names[id])
			}
			frpsRoles[key(p.Type, rp)] = label
		}
	}
	return
}

func (a *App) handleServerPorts(w http.ResponseWriter, r *http.Request) {
	list, err := ports.List()
	if err != nil {
		writeErr(w, 500, "无法读取本机端口："+err.Error())
		return
	}
	frpsRoles, panelRoles := a.portRoles()
	self, frpsPID := os.Getpid(), a.Proc.Status().PID
	rows := make([]portRow, 0, len(list))
	counts := map[string]int{"panel": 0, "frps": 0, "other": 0}
	for _, p := range list {
		k := p.Proto + "/" + strconv.Itoa(p.Port)
		row := portRow{Port: p, Owner: "other"}
		switch {
		case p.PID != 0 && p.PID == self:
			row.Owner, row.Role = "panel", panelRoles[k]
		case (p.PID != 0 && p.PID == frpsPID) || p.Process == "frps":
			row.Owner, row.Role = "frps", frpsRoles[k]
			if row.Role == "" {
				row.Role = "代理端口"
			}
		}
		counts[row.Owner]++
		rows = append(rows, row)
	}
	writeJSON(w, 200, map[string]any{"ports": rows, "counts": counts, "scannedAt": time.Now().Unix()})
}
