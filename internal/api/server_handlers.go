package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"frp-panel/internal/frp"
)

// history keeps recent frps traffic counters so the UI can draw rates.
type sample struct {
	T     int64 `json:"t"`
	In    int64 `json:"in"`
	Out   int64 `json:"out"`
	Conns int64 `json:"conns"`
}

type history struct {
	mu  sync.Mutex
	cap int
	buf []sample
}

func newHistory(n int) *history { return &history{cap: n} }

func (h *history) add(s sample) {
	h.mu.Lock()
	h.buf = append(h.buf, s)
	if len(h.buf) > h.cap {
		h.buf = h.buf[len(h.buf)-h.cap:]
	}
	h.mu.Unlock()
}

func (h *history) list() []sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]sample(nil), h.buf...)
}

func (a *App) sample(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		if d, err := a.loadDoc(); err == nil {
			if si, err := frp.AdminFromDoc(d).ServerInfo(); err == nil {
				a.hist.add(sample{T: time.Now().Unix(), In: si.TotalTrafficIn, Out: si.TotalTrafficOut, Conns: si.CurConns})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *App) handleServerOverview(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"service": a.Proc.Status(), "history": a.hist.list()}
	d, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out["settings"] = frp.ReadServer(d)
	api := frp.AdminFromDoc(d)
	if si, err := api.ServerInfo(); err == nil {
		out["info"] = si
		if ps, err := api.ServerProxies(); err == nil {
			online := 0
			for _, p := range ps {
				if p.Status == "online" {
					online++
				}
			}
			out["proxyTotal"], out["proxyOnline"] = len(ps), online
		}
	} else {
		out["infoError"] = "无法连接 frps 管理接口（服务可能未运行）"
	}
	writeJSON(w, 200, out)
}

func (a *App) handleServerSettingsGet(w http.ResponseWriter, r *http.Request) {
	d, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, frp.ReadServer(d))
}

func (a *App) handleServerSettingsPut(w http.ResponseWriter, r *http.Request) {
	var s frp.ServerSettings
	if !readJSON(w, r, &s) {
		return
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	old, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	d, _ := frp.ParseDoc(mustMarshal(old))
	if err := frp.ApplyServer(d, s); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg, err := a.applyDoc(d)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"message": msg, "firewallPorts": frp.ServerPorts(d)})
}

func mustMarshal(d frp.Doc) []byte {
	b, _ := d.Marshal()
	return b
}

func (a *App) handleServerProxies(w http.ResponseWriter, r *http.Request) {
	d, err := a.loadDoc()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ps, err := frp.AdminFromDoc(d).ServerProxies()
	if err != nil {
		writeErr(w, 502, "无法连接 frps 管理接口（服务可能未运行）")
		return
	}
	// Managed clients connect with user = client ID, so frps names their
	// proxies "<client id>.<proxy name>".
	names := map[string]string{}
	for _, c := range a.Fleet.List() {
		names[c.ID] = c.Name
	}
	type row struct {
		frp.ServerProxy
		ProxyName  string `json:"proxyName"`
		ClientID   string `json:"managedClientId"`
		ClientName string `json:"clientName"`
	}
	rows := make([]row, 0, len(ps))
	for _, p := range ps {
		rw := row{ServerProxy: p, ProxyName: p.Name}
		if id, rest, ok := strings.Cut(p.Name, "."); ok && names[id] != "" {
			rw.ClientID, rw.ClientName, rw.ProxyName = id, names[id], rest
		}
		rows = append(rows, rw)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Status != rows[j].Status {
			return rows[i].Status == "online"
		}
		return rows[i].Name < rows[j].Name
	})
	writeJSON(w, 200, map[string]any{"proxies": rows})
}

// shellQuote wraps s in single quotes for a POSIX shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
