package api

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"frp-panel/internal/frp"
)

func (a *App) handleClientOverview(w http.ResponseWriter, r *http.Request) {
	d, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	common := frp.ReadClientCommon(d)
	common.Token = "" // not needed on the dashboard
	out := map[string]any{"service": a.Proc.Status(), "common": common}
	total, running, failed := 0, 0, 0
	for range frp.Proxies(d) {
		total++
	}
	if st, err := frp.AdminFromDoc(d).ClientStatus(); err == nil {
		for _, s := range st {
			switch s.Status {
			case "running":
				running++
			case "start error", "check failed", "closed":
				failed++
			}
		}
		out["adminReachable"] = true
	}
	out["proxyTotal"], out["proxyRunning"], out["proxyFailed"] = total, running, failed
	writeJSON(w, 200, out)
}

func (a *App) handleClientCommonGet(w http.ResponseWriter, r *http.Request) {
	d, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, frp.ReadClientCommon(d))
}

func (a *App) handleClientCommonPut(w http.ResponseWriter, r *http.Request) {
	var c frp.ClientCommon
	if !readJSON(w, r, &c) {
		return
	}
	a.mutateClient(w, func(d frp.Doc) error { return frp.ApplyClientCommon(d, c) })
}

// mutateClient loads frpc.toml, applies fn to a copy, then verifies, saves
// and applies it.
func (a *App) mutateClient(w http.ResponseWriter, fn func(frp.Doc) error) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	old, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	d, _ := frp.ParseDoc(mustMarshal(old))
	if err := fn(d); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg, err := a.applyDoc(old, d)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"message": msg})
}

type proxyRow struct {
	frp.Proxy
	Status     string `json:"status"`
	Err        string `json:"err"`
	RemoteAddr string `json:"remoteAddr"`
	PublicURL  string `json:"publicUrl"`
}

func (a *App) handleClientProxies(w http.ResponseWriter, r *http.Request) {
	d, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	common := frp.ReadClientCommon(d)
	st, stErr := frp.AdminFromDoc(d).ClientStatus()
	rows := []proxyRow{}
	for _, m := range frp.Proxies(d) {
		p := frp.ReadProxy(m)
		row := proxyRow{Proxy: p, Status: "unknown"}
		if p.Disabled {
			row.Status = "disabled"
		} else if stErr != nil {
			row.Status = "stopped"
		} else if s, ok := st[p.Name]; ok {
			row.Status, row.Err, row.RemoteAddr = s.Status, s.Err, s.RemoteAddr
		}
		switch p.Type {
		case "tcp", "udp":
			if p.RemotePort > 0 {
				row.PublicURL = net.JoinHostPort(common.ServerAddr, strconv.Itoa(p.RemotePort))
			} else if row.RemoteAddr != "" {
				_, port, _ := net.SplitHostPort(row.RemoteAddr)
				row.PublicURL = net.JoinHostPort(common.ServerAddr, port)
			}
		case "http", "https":
			if len(p.CustomDomains) > 0 {
				row.PublicURL = p.Type + "://" + p.CustomDomains[0]
			} else if row.RemoteAddr != "" {
				row.PublicURL = p.Type + "://" + strings.TrimPrefix(strings.TrimPrefix(row.RemoteAddr, "http://"), "https://")
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, 200, map[string]any{"proxies": rows, "running": stErr == nil, "serverAddr": common.ServerAddr})
}

func (a *App) handleClientProxyCreate(w http.ResponseWriter, r *http.Request) {
	var p frp.Proxy
	if !readJSON(w, r, &p) {
		return
	}
	a.mutateClient(w, func(d frp.Doc) error { return frp.UpsertProxy(d, "", p) })
}

func (a *App) handleClientProxyUpdate(w http.ResponseWriter, r *http.Request) {
	var p frp.Proxy
	if !readJSON(w, r, &p) {
		return
	}
	name := r.PathValue("name")
	a.mutateClient(w, func(d frp.Doc) error { return frp.UpsertProxy(d, name, p) })
}

func (a *App) handleClientProxyDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a.mutateClient(w, func(d frp.Doc) error { return frp.DeleteProxy(d, name) })
}

// handleClientTest checks TCP reachability of frps (and of a local service).
func (a *App) handleClientTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Addr string `json:"addr"`
		Port int    `json:"port"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Addr == "" || req.Port < 1 || req.Port > 65535 {
		writeErr(w, 400, "地址或端口无效")
		return
	}
	start := time.Now()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(strings.TrimSpace(req.Addr), strconv.Itoa(req.Port)), 5*time.Second)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	c.Close()
	writeJSON(w, 200, map[string]any{"ok": true, "latency": fmt.Sprintf("%d ms", time.Since(start).Milliseconds())})
}
