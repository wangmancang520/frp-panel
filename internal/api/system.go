package api

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"frp-panel/internal/auth"
	"frp-panel/internal/frp"
	"frp-panel/internal/store"
	"frp-panel/internal/tlsx"
)

func (a *App) loadDoc() (frp.Doc, error) {
	return frp.LoadDoc(a.St.Get().FRPConfigPath())
}

// applyDoc verifies and writes a new frps.toml, restarts frps and lets the
// agents know, since their configs are rendered from it (port, token, TLS).
func (a *App) applyDoc(d frp.Doc) (string, error) {
	cfg := a.St.Get()
	if err := a.frpsPortConflict(d); err != nil {
		return "", err
	}
	frp.EnforceLocalAdmin(d, 7500, randomSecret)
	b, err := d.Marshal()
	if err != nil {
		return "", err
	}
	path := cfg.FRPConfigPath()
	if err := frp.Verify(cfg.FRPBinary(), path, b); err != nil {
		return "", err
	}
	if err := store.WriteFileAtomic(path, b, 0o640); err != nil {
		return "", err
	}
	a.Fleet.Notify()
	if !a.Proc.Status().Running {
		return "已保存。服务当前未运行，启动后生效。", nil
	}
	if err := a.Proc.Restart(); err != nil {
		return "", fmt.Errorf("已保存，但重启失败：%w", err)
	}
	return "已保存并重启服务", nil
}

func randomSecret() string { return auth.RandomString(24) }

func (a *App) handleSystem(w http.ResponseWriter, r *http.Request) {
	cfg := a.St.Get()
	host, _ := os.Hostname()
	out := map[string]any{
		"role":         cfg.Role,
		"panelVersion": a.Version,
		"hostname":     host,
		"os":           runtime.GOOS + "/" + runtime.GOARCH,
		"frpVersion":   frp.InstalledVersion(cfg.FRPBinary()),
		"service":      a.Proc.Status(),
		"procMode":     cfg.ProcMode,
		"panelUptime":  int(time.Since(a.started).Seconds()),
		"listen":       cfg.Listen,
		"githubMirror": cfg.GithubMirror,
		"publicAddr":   cfg.PublicAddr,
		"configPath":   cfg.FRPConfigPath(),
		"fingerprint":  "",
	}
	if fp, err := tlsx.SelfSignedFingerprint(a.St.Dir + "/tls"); err == nil {
		out["fingerprint"] = fp
	}
	if d, err := a.loadDoc(); err == nil {
		out["firewallPorts"] = frp.ServerPorts(d)
	}
	writeJSON(w, 200, out)
}

func (a *App) handleService(w http.ResponseWriter, r *http.Request) {
	var err error
	switch r.PathValue("action") {
	case "start":
		err = a.Proc.Start()
	case "stop":
		err = a.Proc.Stop()
	case "restart":
		err = a.Proc.Restart()
	default:
		writeErr(w, 400, "未知操作")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	time.Sleep(800 * time.Millisecond)
	writeJSON(w, 200, a.Proc.Status())
}

func (a *App) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := atoiDefault(r.URL.Query().Get("n"), 300)
	if n < 1 || n > 2000 {
		n = 300
	}
	// Hide FRP's access log lines for the panel's own admin-API polling.
	var lines []string
	for _, l := range a.Proc.Logs(n + 600) {
		if !adminNoise.MatchString(l) {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	writeJSON(w, 200, map[string]any{"lines": lines})
}

var adminNoise = regexp.MustCompile(`middleware\.go:\d+\] http (request|response) \[/api/`)

func (a *App) handleLatest(w http.ResponseWriter, r *http.Request) {
	cfg := a.St.Get()
	v, err := frp.LatestVersion(cfg.GithubMirror)
	if err != nil {
		writeErr(w, 502, err.Error()+"（如果无法访问 GitHub，请在「系统」页设置下载镜像）")
		return
	}
	writeJSON(w, 200, map[string]string{"latest": v, "installed": frp.InstalledVersion(cfg.FRPBinary())})
}

func (a *App) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	cfg := a.St.Get()
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	wasRunning := a.Proc.Status().Running
	// frpc is kept next to frps to verify client configs and to hand out to
	// agents on the same CPU architecture.
	if err := frp.Install(req.Version, cfg.GithubMirror, cfg.BinDir, "frps", "frpc"); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	// Agents follow the server's FRP version.
	a.forgetFRPVersion()
	a.Fleet.Notify()
	if wasRunning {
		if err := a.Proc.Restart(); err != nil {
			writeErr(w, 500, "已安装，但重启失败："+err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]string{"installed": frp.InstalledVersion(cfg.FRPBinary())})
}

func (a *App) handlePanelSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GithubMirror string `json:"githubMirror"`
		PublicAddr   string `json:"publicAddr"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if u := req.GithubMirror; u != "" {
		if p, err := url.Parse(u); err != nil || p.Scheme != "https" || p.Host == "" {
			writeErr(w, 400, "镜像地址必须以 https:// 开头")
			return
		}
	}
	if strings.ContainsAny(req.PublicAddr, " /'\"`$;&|") {
		writeErr(w, 400, "公网地址只能是 IP 或域名")
		return
	}
	err := a.St.Update(func(c *store.Config) {
		c.GithubMirror = strings.TrimSpace(req.GithubMirror)
		c.PublicAddr = strings.TrimSpace(req.PublicAddr)
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The public address is the serverAddr in every client config.
	a.Fleet.Notify()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- TLS ----

func (a *App) handleTLSGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.tlsStatus())
}

// tlsStatus is the certificate state shown on the 证书 page.
func (a *App) tlsStatus() map[string]any {
	cfg := a.St.Get()
	st := a.TLS.Status()
	st["extraSANs"], st["email"] = cfg.TLS.ExtraSANs, cfg.TLS.Email
	// The panel's own port, for links: the browser may be behind a proxy.
	_, st["panelPort"], _ = net.SplitHostPort(cfg.Listen)
	return st
}

func (a *App) handleTLSPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode   string `json:"mode"`
		Domain string `json:"domain"`
		Email  string `json:"email"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	switch req.Mode {
	case store.TLSSelfSigned, store.TLSCustom, store.TLSACME:
	default:
		writeErr(w, 400, "未知的证书模式")
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if what := a.frpsOn80(); req.Mode == store.TLSACME && what != "" && a.panelPort() != 443 {
		writeErr(w, 400, frps80Problem(what))
		return
	}
	cfg := a.St.Get()
	if err := a.TLS.Configure(req.Mode, req.Domain, req.Email, cfg.TLS.ExtraSANs); err != nil {
		_ = a.TLS.Configure(cfg.TLS.Mode, cfg.TLS.Domain, cfg.TLS.Email, cfg.TLS.ExtraSANs)
		writeErr(w, 400, err.Error())
		return
	}
	if err := a.St.Update(func(c *store.Config) {
		c.TLS.Mode, c.TLS.Domain, c.TLS.Email = req.Mode, req.Domain, strings.TrimSpace(req.Email)
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if req.Mode == store.TLSACME {
		a.warmACME()
	} else {
		a.stopHTTP80() // free :80 for frps
	}
	a.handleTLSGet(w, r)
}

func (a *App) handleTLSRegen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExtraSANs []string `json:"extraSANs"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var sans []string
	for _, s := range req.ExtraSANs {
		if s = strings.TrimSpace(s); s != "" {
			if strings.ContainsAny(s, " /'\"") || len(s) > 253 {
				writeErr(w, 400, "名称无效："+s)
				return
			}
			sans = append(sans, s)
		}
	}
	if err := a.TLS.RegenerateSelfSigned(sans); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = a.St.Update(func(c *store.Config) { c.TLS.ExtraSANs = sans })
	a.handleTLSGet(w, r)
}

func (a *App) handleTLSCustom(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cert string `json:"cert"`
		Key  string `json:"key"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := a.TLS.SaveCustom([]byte(req.Cert), []byte(req.Key)); err != nil {
		writeErr(w, 400, "证书无效："+err.Error())
		return
	}
	cfg := a.St.Get()
	if err := a.TLS.Configure(store.TLSCustom, "", "", cfg.TLS.ExtraSANs); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	_ = a.St.Update(func(c *store.Config) { c.TLS.Mode = store.TLSCustom })
	a.stopHTTP80() // free :80 for frps
	a.handleTLSGet(w, r)
}

// ---- raw config ----

func (a *App) handleRawGet(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile(a.St.Get().FRPConfigPath())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"content": string(b), "path": a.St.Get().FRPConfigPath()})
}

func (a *App) handleRawPut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	d, err := frp.ParseDoc([]byte(req.Content))
	if err != nil {
		writeErr(w, 400, "TOML 语法错误："+err.Error())
		return
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	msg, err := a.applyDoc(d)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"message": msg})
}
