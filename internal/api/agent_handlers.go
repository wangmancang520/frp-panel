package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"frp-panel/internal/auth"
	"frp-panel/internal/fleet"
	"frp-panel/internal/frp"
	"frp-panel/internal/store"
	"frp-panel/internal/tlsx"
)

// PanelRepo is where release builds of frp-panel are published; overridden
// at build time with -ldflags. Used to fetch agent binaries for other CPU
// architectures than the server's own.
var PanelRepo = "wangmancang520/frp-panel"

// ---- join command ----

// JoinCommand builds the one-line client install command: it fetches
// install.sh from this panel (trusting only the panel's pinned public key)
// and enrolls the machine with the enrollment key.
func JoinCommand(cfg store.Config, dataDir, host string) (cmd, panelURL string, err error) {
	pin, err := tlsx.SelfSignedPin(filepath.Join(dataDir, "tls"))
	if err != nil {
		return "", "", err
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return "", "", err
	}
	panelURL = "https://" + net.JoinHostPort(host, port)
	cmd = "curl -fsSLk --pinnedpubkey " + shellQuote(pin) + " " + shellQuote(panelURL+"/join.sh") +
		" | sudo bash -s -- agent --panel " + shellQuote(panelURL) +
		" --pin " + shellQuote(pin) + " --key " + shellQuote(cfg.EnrollKey)
	return cmd, panelURL, nil
}

// JoinHost picks the panel address for the join command: the configured
// public address, else the one the browser used. Host names are resolved to
// an IP when possible, because curl sends SNI for names and the panel would
// then present a domain certificate instead of the pinned self-signed one.
func JoinHost(publicAddr, reqHost string) string {
	h := strings.TrimSpace(publicAddr)
	if h == "" {
		h = hostOnly(reqHost)
	}
	if h == "" || net.ParseIP(h) != nil {
		return h
	}
	if ips, err := net.LookupIP(h); err == nil {
		for _, ip := range ips {
			if ip.To4() != nil {
				return ip.String()
			}
		}
		if len(ips) > 0 {
			return ips[0].String()
		}
	}
	return h
}

func hostOnly(h string) string {
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.Trim(h, "[]")
}

func (a *App) handleServerJoin(w http.ResponseWriter, r *http.Request) {
	cfg := a.St.Get()
	host := JoinHost(cfg.PublicAddr, r.Host)
	cmd, panelURL, err := JoinCommand(cfg, a.St.Dir, host)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"command": cmd, "panelUrl": panelURL, "host": host, "publicAddrSet": cfg.PublicAddr != ""})
}

func (a *App) handleServerJoinReset(w http.ResponseWriter, r *http.Request) {
	if err := a.St.Update(func(c *store.Config) { c.EnrollKey = auth.RandomString(32) }); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.handleServerJoin(w, r)
}

func (a *App) handleJoinScript(w http.ResponseWriter, r *http.Request) {
	if len(a.InstallScript) == 0 {
		writeErr(w, 404, "install.sh 不可用")
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	_, _ = w.Write(a.InstallScript)
}

// ---- agent API ----

// readAgentJSON is lenient about unknown fields so agents and panels of
// different versions keep talking.
func readAgentJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, 400, "请求格式错误："+err.Error())
		return false
	}
	return true
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (a *App) agentAuth(h func(http.ResponseWriter, *http.Request, fleet.Client)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := a.Fleet.Authenticate(bearer(r))
		if !ok {
			writeErr(w, 401, "客户端未注册或已被删除")
			return
		}
		h(w, r, c)
	}
}

// enrollKeyOK checks an enrollment key, counting failures like logins.
func (a *App) enrollKeyOK(w http.ResponseWriter, r *http.Request, key string) bool {
	ip := clientIP(r)
	if ok, _ := a.lim.Allowed(ip); !ok {
		writeErr(w, 429, "失败次数过多，请稍后再试")
		return false
	}
	want := a.St.Get().EnrollKey
	if want == "" || !auth.ConstantEq(key, want) {
		a.lim.Fail(ip)
		writeErr(w, 403, "接入密钥无效（可能已在面板中重置），请重新复制接入命令")
		return false
	}
	return true
}

func (a *App) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	var req fleet.EnrollRequest
	if !readAgentJSON(w, r, &req) || !a.enrollKeyOK(w, r, req.Key) {
		return
	}
	c, tok, err := a.Fleet.Enroll(req.Hostname, req.Arch,
		func() string { return strings.ToLower(auth.RandomString(8)) }, auth.RandomToken, randomSecret)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("client %s (%s, %s) enrolled from %s", c.ID, c.Name, c.Arch, clientIP(r))
	writeJSON(w, 200, fleet.EnrollResponse{ID: c.ID, Name: c.Name, Token: tok})
}

// fleetCommon is the connection info rendered into every client config.
// Without a configured public address, clients use the address they reach
// the panel at.
func (a *App) fleetCommon(panelHost string) (fleet.Common, error) {
	d, err := a.loadDoc()
	if err != nil {
		return fleet.Common{}, err
	}
	s := frp.ReadServer(d)
	addr := a.St.Get().PublicAddr
	if addr == "" {
		addr = hostOnly(panelHost)
	}
	if addr == "" {
		addr = "127.0.0.1"
	}
	return fleet.Common{ServerAddr: addr, ServerPort: s.BindPort, Token: s.Token, TLSForce: s.TLSForce}, nil
}

func (a *App) agentConfig(id, panelHost string) (fleet.ConfigResponse, error) {
	c, ok := a.Fleet.Get(id)
	if !ok {
		return fleet.ConfigResponse{}, fleet.ErrNotFound
	}
	cm, err := a.fleetCommon(panelHost)
	if err != nil {
		return fleet.ConfigResponse{}, err
	}
	b, err := fleet.Render(c, cm)
	if err != nil {
		return fleet.ConfigResponse{}, err
	}
	resp := fleet.ConfigResponse{Hash: fleet.Hash(b), Config: string(b), FRPVersion: a.frpVersion(), PanelVersion: a.Version}
	resp.Rev = fleet.Rev(resp.Hash, resp.FRPVersion, resp.PanelVersion)
	return resp, nil
}

// handleAgentConfig long-polls: it answers as soon as the client's config or
// the versions it should run differ from the revision the agent has (or a
// restart is queued), and with 204 after ~25s of no change.
func (a *App) handleAgentConfig(w http.ResponseWriter, r *http.Request, c fleet.Client) {
	have := r.URL.Query().Get("rev")
	a.Fleet.SawConfigRequest(c.ID, r.Host)
	timeout := time.NewTimer(25 * time.Second)
	defer timeout.Stop()
	for {
		changed := a.Fleet.Changed()
		resp, err := a.agentConfig(c.ID, r.Host)
		if errors.Is(err, fleet.ErrNotFound) {
			writeErr(w, 401, "客户端已被删除")
			return
		}
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		resp.Restart = a.Fleet.TakeRestart(c.ID)
		if resp.Rev != have || resp.Restart {
			writeJSON(w, 200, resp)
			return
		}
		select {
		case <-changed:
		case <-timeout.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (a *App) handleAgentReport(w http.ResponseWriter, r *http.Request, c fleet.Client) {
	var rep fleet.Report
	if !readAgentJSON(w, r, &rep) {
		return
	}
	writeJSON(w, 200, a.Fleet.SetReport(c.ID, clientIP(r), rep))
}

// ---- binaries for agents ----

var archNames = map[string]string{"amd64": "amd64", "arm64": "arm64", "arm_hf": "armv7", "arm": ""} // FRP name -> panel release name

// handleAgentDownload serves frpc or frp-panel for an agent's CPU. Allowed
// with an agent token, or with the enrollment key (the install script needs
// frp-panel before the machine is enrolled).
func (a *App) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.Fleet.Authenticate(bearer(r)); !ok {
		if !a.enrollKeyOK(w, r, r.Header.Get("X-Enroll-Key")) {
			return
		}
	}
	arch := r.URL.Query().Get("arch")
	if _, ok := archNames[arch]; !ok {
		writeErr(w, 400, "不支持的 CPU 架构："+arch)
		return
	}
	var path string
	var err error
	switch r.PathValue("name") {
	case "frpc":
		path, err = a.agentFRPC(arch)
	case "frp-panel":
		path, err = a.agentPanel(arch)
	default:
		writeErr(w, 404, "未知文件")
		return
	}
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, r.PathValue("name"), fi.ModTime(), f)
}

func (a *App) cacheDir() string { return filepath.Join(a.St.Dir, "cache") }

// agentFRPC returns an frpc binary for arch matching the server's frps
// version: the server's own frpc when the CPU matches, otherwise one
// extracted from the (checksum-verified, cached) official release.
func (a *App) agentFRPC(arch string) (string, error) {
	cfg := a.St.Get()
	ver := a.frpVersion()
	if ver == "" {
		return "", errors.New("服务端尚未安装 FRP")
	}
	if own := filepath.Join(cfg.BinDir, "frpc"); arch == frp.ReleaseArch() && frp.InstalledVersion(own) == ver {
		return own, nil
	}
	a.dlMu.Lock()
	defer a.dlMu.Unlock()
	bin := filepath.Join(a.cacheDir(), fmt.Sprintf("frpc-%s-linux-%s", ver, arch))
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	archive, err := frp.Archive(ver, cfg.GithubMirror, "linux_"+arch, a.cacheDir())
	if err != nil {
		return "", err
	}
	if err := frp.ExtractBinary(archive, "frpc", bin); err != nil {
		return "", err
	}
	return bin, nil
}

var releaseVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// agentPanel returns an frp-panel binary for arch: this very executable when
// the CPU matches, otherwise the same version from the cache (which the
// installer seeds from a local dist/ build) or from the GitHub release.
func (a *App) agentPanel(arch string) (string, error) {
	if arch == frp.ReleaseArch() {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
	name := archNames[arch]
	if name == "" {
		return "", fmt.Errorf("没有适用于 %s 的 frp-panel 发布版本", arch)
	}
	a.dlMu.Lock()
	defer a.dlMu.Unlock()
	file := "frp-panel-linux-" + name
	bin := filepath.Join(a.cacheDir(), file+"-"+a.Version)
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	if !releaseVersionRe.MatchString(a.Version) {
		return "", fmt.Errorf("面板是开发版本（%s），没有适用于 %s 的程序；请用 build.sh 编译全部架构并通过本地 dist 目录更新服务端，或使用正式发布版本", a.Version, arch)
	}
	mirror := a.St.Get().GithubMirror
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s/", PanelRepo, a.Version)
	var sums strings.Builder
	if err := frp.Fetch(frp.WithMirror(mirror, base+"sha256sums.txt"), &sums, 1<<20); err != nil {
		return "", fmt.Errorf("下载面板校验文件失败：%w", err)
	}
	want := frp.ChecksumFor(sums.String(), file)
	if want == "" {
		return "", fmt.Errorf("面板发布的校验文件中没有 %s", file)
	}
	if err := os.MkdirAll(a.cacheDir(), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(a.cacheDir(), ".dl-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if err := frp.Fetch(frp.WithMirror(mirror, base+file), io.MultiWriter(tmp, h), 200<<20); err != nil {
		return "", fmt.Errorf("下载 %s 失败：%w", file, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("%s 的 SHA-256 不匹配，已放弃", file)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	return bin, os.Rename(tmp.Name(), bin)
}

// frpVersion is the installed frps version, cached for a minute because
// every agent long-poll needs it.
func (a *App) frpVersion() string {
	a.verMu.Lock()
	defer a.verMu.Unlock()
	if time.Since(a.verAt) > time.Minute {
		a.ver, a.verAt = frp.InstalledVersion(a.St.Get().FRPBinary()), time.Now()
	}
	return a.ver
}

func (a *App) forgetFRPVersion() {
	a.verMu.Lock()
	a.verAt = time.Time{}
	a.verMu.Unlock()
}
