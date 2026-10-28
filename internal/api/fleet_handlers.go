package api

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"frp-panel/internal/fleet"
	"frp-panel/internal/frp"
)

// clientView is a managed client as shown in the UI (no token or config).
type clientView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Hostname     string     `json:"hostname"`
	Arch         string     `json:"arch"`
	Created      time.Time  `json:"created"`
	Online       bool       `json:"online"`
	LastSeen     time.Time  `json:"lastSeen"`
	RemoteIP     string     `json:"remoteIP"`
	AgentVersion string     `json:"agentVersion"`
	FRPVersion   string     `json:"frpVersion"`
	Service      frp.Status `json:"service"`
	AdminOK      bool       `json:"adminOK"`
	ProxyTotal   int        `json:"proxyTotal"`
	ProxyRunning int        `json:"proxyRunning"`
	ProxyFailed  int        `json:"proxyFailed"`
	Synced       bool       `json:"synced"` // agent runs the current config
	Error        string     `json:"error"`
}

// proxyStatus finds a proxy in an agent report; frpc may list it with or
// without the user ("<client id>.") prefix.
func proxyStatus(st map[string]frp.ClientProxyStatus, id, name string) (frp.ClientProxyStatus, bool) {
	if s, ok := st[name]; ok {
		return s, true
	}
	s, ok := st[id+"."+name]
	return s, ok
}

func (a *App) viewClient(c fleet.Client) clientView {
	l := a.Fleet.Live(c.ID)
	rep := l.Report
	v := clientView{
		ID: c.ID, Name: c.Name, Hostname: c.Hostname, Arch: c.Arch, Created: c.Created,
		Online:   !l.LastSeen.IsZero() && time.Since(l.LastSeen) < fleet.OnlineWithin,
		LastSeen: l.LastSeen, RemoteIP: l.RemoteIP, AgentVersion: rep.Version, FRPVersion: rep.FRPVersion,
		Service: rep.Service, AdminOK: rep.AdminOK, Error: rep.Error,
	}
	if d, err := frp.ParseDoc([]byte(c.Config)); err == nil {
		for _, m := range frp.Proxies(d) {
			p := frp.ReadProxy(m)
			if p.Disabled {
				continue
			}
			v.ProxyTotal++
			if s, ok := proxyStatus(rep.Proxies, c.ID, p.Name); ok {
				switch s.Status {
				case "running":
					v.ProxyRunning++
				case "start error", "check failed", "closed":
					v.ProxyFailed++
				}
			}
		}
	}
	if cm, err := a.fleetCommon(l.PanelHost); err == nil {
		if b, err := fleet.Render(c, cm); err == nil {
			v.Synced = fleet.Hash(b) == rep.ConfigHash
		}
	}
	return v
}

func (a *App) clientOr404(w http.ResponseWriter, r *http.Request) (fleet.Client, bool) {
	c, ok := a.Fleet.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, fleet.ErrNotFound.Error())
	}
	return c, ok
}

func (a *App) handleClients(w http.ResponseWriter, r *http.Request) {
	list := a.Fleet.List()
	out := make([]clientView, 0, len(list))
	for _, c := range list {
		out = append(out, a.viewClient(c))
	}
	writeJSON(w, 200, map[string]any{"clients": out, "frpVersion": a.frpVersion(), "panelVersion": a.Version})
}

// clientSettings are the per-client options editable in the UI.
type clientSettings struct {
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	TLSEnable  bool   `json:"tlsEnable"`
	LogLevel   string `json:"logLevel"`
	LogMaxDays int    `json:"logMaxDays"`
}

func (a *App) handleClientGet(w http.ResponseWriter, r *http.Request) {
	c, ok := a.clientOr404(w, r)
	if !ok {
		return
	}
	d, err := frp.ParseDoc([]byte(c.Config))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cc := frp.ReadClientCommon(d)
	cm, _ := a.fleetCommon(a.Fleet.Live(c.ID).PanelHost)
	writeJSON(w, 200, map[string]any{
		"client":   a.viewClient(c),
		"settings": clientSettings{Name: c.Name, Protocol: cc.Protocol, TLSEnable: cc.TLSEnable, LogLevel: cc.LogLevel, LogMaxDays: cc.LogMaxDays},
		"server":   map[string]any{"addr": cm.ServerAddr, "port": cm.ServerPort, "tlsForce": cm.TLSForce},
	})
}

func (a *App) handleClientPut(w http.ResponseWriter, r *http.Request) {
	var s clientSettings
	if !readJSON(w, r, &s) {
		return
	}
	s.Name = strings.TrimSpace(s.Name)
	if s.Name == "" || len(s.Name) > 64 {
		writeErr(w, 400, "名称需要 1-64 个字符")
		return
	}
	a.mutateManaged(w, r.PathValue("id"), func(c *fleet.Client, d frp.Doc) error {
		c.Name = s.Name
		return frp.ApplyClientOptions(d, frp.ClientCommon{Protocol: s.Protocol, TLSEnable: s.TLSEnable, LogLevel: s.LogLevel, LogMaxDays: s.LogMaxDays})
	})
}

func (a *App) handleClientDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.Fleet.Delete(r.PathValue("id")); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"message": "已删除。该机器上的 frpc 会被 agent 停止；如需清理该机器上的配置，请在该机器上运行 sudo frp-client remove（或 frp-client uninstall）"})
}

func (a *App) handleClientRestart(w http.ResponseWriter, r *http.Request) {
	c, ok := a.clientOr404(w, r)
	if !ok {
		return
	}
	a.Fleet.RequestRestart(c.ID)
	writeJSON(w, 200, map[string]string{"message": "已通知客户端重启 frpc"})
}

// verifyManaged checks a client's rendered config with the server's own
// frpc binary (installed alongside frps for this purpose), when present.
func (a *App) verifyManaged(c fleet.Client) error {
	cm, err := a.fleetCommon("")
	if err != nil {
		return err
	}
	b, err := fleet.Render(c, cm)
	if err != nil {
		return err
	}
	bin := filepath.Join(a.St.Get().BinDir, "frpc")
	if _, err := os.Stat(bin); err != nil {
		return nil
	}
	return frp.Verify(bin, filepath.Join(os.TempDir(), "frpc.toml"), b)
}

// mutateManaged edits client id's config with fn, validates the result
// (ports across clients, frpc verify) and stores it; the agent picks it up
// through its long-poll.
func (a *App) mutateManaged(w http.ResponseWriter, id string, fn func(c *fleet.Client, d frp.Doc) error) {
	err := a.Fleet.Update(id, func(c *fleet.Client, others []fleet.Client) error {
		d, err := frp.ParseDoc([]byte(c.Config))
		if err != nil {
			return err
		}
		if err := fn(c, d); err != nil {
			return err
		}
		frp.StripManaged(d)
		frp.EnforceLocalAdmin(d, 7400, randomSecret)
		if err := fleet.CheckPorts(d, others); err != nil {
			return err
		}
		b, err := d.Marshal()
		if err != nil {
			return err
		}
		c.Config = string(b)
		return a.verifyManaged(*c)
	})
	if errors.Is(err, fleet.ErrNotFound) {
		writeErr(w, 404, err.Error())
		return
	}
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg := "已保存，客户端会在几秒内自动应用"
	if l := a.Fleet.Live(id); l.LastSeen.IsZero() || time.Since(l.LastSeen) > fleet.OnlineWithin {
		msg = "已保存。客户端当前离线，上线后会自动应用"
	}
	writeJSON(w, 200, map[string]string{"message": msg})
}

// ---- proxies ----

type proxyRow struct {
	frp.Proxy
	Status     string `json:"status"`
	Err        string `json:"err"`
	RemoteAddr string `json:"remoteAddr"`
	PublicURL  string `json:"publicUrl"`
}

func (a *App) handleClientProxies(w http.ResponseWriter, r *http.Request) {
	c, ok := a.clientOr404(w, r)
	if !ok {
		return
	}
	d, err := frp.ParseDoc([]byte(c.Config))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	v := a.viewClient(c)
	l := a.Fleet.Live(c.ID)
	cm, _ := a.fleetCommon(l.PanelHost)
	rows := []proxyRow{}
	for _, m := range frp.Proxies(d) {
		p := frp.ReadProxy(m)
		row := proxyRow{Proxy: p, Status: "unknown"}
		if p.Disabled {
			row.Status = "disabled"
		} else if !v.Online {
			row.Status = "offline"
		} else if !l.Report.AdminOK {
			row.Status = "stopped"
		} else if s, ok := proxyStatus(l.Report.Proxies, c.ID, p.Name); ok {
			row.Status, row.Err, row.RemoteAddr = s.Status, s.Err, s.RemoteAddr
		}
		switch p.Type {
		case "tcp", "udp":
			if p.RemotePort > 0 {
				row.PublicURL = net.JoinHostPort(cm.ServerAddr, strconv.Itoa(p.RemotePort))
			} else if row.RemoteAddr != "" {
				_, port, _ := net.SplitHostPort(row.RemoteAddr)
				row.PublicURL = net.JoinHostPort(cm.ServerAddr, port)
			}
		case "http", "https":
			// frpc reports host:vhostPort, which is the address users can open.
			if row.RemoteAddr != "" {
				host := strings.TrimPrefix(strings.TrimPrefix(row.RemoteAddr, "http://"), "https://")
				host = strings.TrimSuffix(host, map[string]string{"http": ":80", "https": ":443"}[p.Type])
				row.PublicURL = p.Type + "://" + host
			} else if len(p.CustomDomains) > 0 {
				row.PublicURL = p.Type + "://" + p.CustomDomains[0]
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, 200, map[string]any{"proxies": rows, "online": v.Online, "running": v.Online && l.Report.AdminOK, "serverAddr": cm.ServerAddr, "synced": v.Synced})
}

func (a *App) handleClientProxyCreate(w http.ResponseWriter, r *http.Request) {
	var p frp.Proxy
	if !readJSON(w, r, &p) {
		return
	}
	a.mutateManaged(w, r.PathValue("id"), func(_ *fleet.Client, d frp.Doc) error { return frp.UpsertProxy(d, "", p) })
}

func (a *App) handleClientProxyUpdate(w http.ResponseWriter, r *http.Request) {
	var p frp.Proxy
	if !readJSON(w, r, &p) {
		return
	}
	name := r.PathValue("name")
	a.mutateManaged(w, r.PathValue("id"), func(_ *fleet.Client, d frp.Doc) error { return frp.UpsertProxy(d, name, p) })
}

func (a *App) handleClientProxyDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a.mutateManaged(w, r.PathValue("id"), func(_ *fleet.Client, d frp.Doc) error { return frp.DeleteProxy(d, name) })
}

// ---- logs and raw config ----

func (a *App) handleClientLogs(w http.ResponseWriter, r *http.Request) {
	c, ok := a.clientOr404(w, r)
	if !ok {
		return
	}
	var lines []string
	for _, l := range a.Fleet.WantLogs(c.ID) {
		if !adminNoise.MatchString(l) {
			lines = append(lines, l)
		}
	}
	writeJSON(w, 200, map[string]any{"lines": lines, "online": a.viewClient(c).Online})
}

func (a *App) handleClientRawGet(w http.ResponseWriter, r *http.Request) {
	c, ok := a.clientOr404(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]string{"content": c.Config, "path": c.Name + " · frpc.toml"})
}

func (a *App) handleClientRawPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	nd, err := frp.ParseDoc([]byte(req.Content))
	if err != nil {
		writeErr(w, 400, "TOML 语法错误："+err.Error())
		return
	}
	a.mutateManaged(w, r.PathValue("id"), func(_ *fleet.Client, d frp.Doc) error {
		for k := range d {
			delete(d, k)
		}
		for k, v := range nd {
			d[k] = v
		}
		return nil
	})
}
