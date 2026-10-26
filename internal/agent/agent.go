// Package agent runs on managed client machines. It has no UI and opens no
// ports: it connects out to the server panel over HTTPS (trusting only the
// panel's pinned public key), keeps frpc.toml and the frpc binary in sync
// with what the server says, and reports frpc's status back.
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"frp-panel/internal/fleet"
	"frp-panel/internal/frp"
	"frp-panel/internal/store"
	"frp-panel/internal/tlsx"
)

const configFile = "agent.json"

// Config is the agent's identity, stored in <data>/agent.json (0600).
type Config struct {
	Panel string `json:"panel"` // https://host:port of the server panel
	Pin   string `json:"pin"`   // sha256//<base64> of the panel's public key
	ID    string `json:"id"`
	Token string `json:"token"`
}

// Options locate the agent's files.
type Options struct {
	Data, FRPDir, BinDir, Version string
}

func ConfigPath(dir string) string { return filepath.Join(dir, configFile) }

func Load(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath(dir))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Panel == "" || c.Pin == "" || c.Token == "" {
		return c, errors.New(ConfigPath(dir) + " 不完整")
	}
	return c, nil
}

func (c Config) save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(ConfigPath(dir), b, 0o600)
}

// ---- HTTP ----

// newHTTPClient trusts only the pinned key. It sends no SNI, so the panel
// always answers with its self-signed certificate (whose key is the one
// pinned) even when a domain certificate is configured.
func newHTTPClient(pin string) *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			tc := tls.Client(raw, &tls.Config{
				MinVersion: tls.VersionTLS12,
				// Chain and name checks are replaced by the pin check below.
				InsecureSkipVerify: true,
				VerifyConnection: func(cs tls.ConnectionState) error {
					if len(cs.PeerCertificates) == 0 {
						return errors.New("面板没有提供证书")
					}
					if tlsx.SPKIPin(cs.PeerCertificates[0].RawSubjectPublicKeyInfo) != pin {
						return errors.New("面板证书公钥与接入命令中的指纹不一致，已拒绝连接")
					}
					return nil
				},
			})
			if err := tc.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			return tc, nil
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 90 * time.Second,
	}
	return &http.Client{Transport: tr}
}

type apiClient struct {
	base, token string
	hc          *http.Client
}

type statusError struct {
	code int
	msg  string
}

func (e *statusError) Error() string { return e.msg }

func statusOf(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.code
	}
	return 0
}

// do sends a JSON request. A 204 leaves out untouched and returns (204, nil).
func (a *apiClient) do(ctx context.Context, timeout time.Duration, method, path string, in, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return resp.StatusCode, &statusError{resp.StatusCode, e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// download fetches a binary for this machine from the panel into a temp file
// next to dst and returns its path; the caller renames or removes it.
func (a *apiClient) download(ctx context.Context, name, dst string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	u := a.base + "/agent/v1/download/" + name + "?arch=" + url.QueryEscape(frp.ReleaseArch())
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = json.Unmarshal(b, &e)
		return "", fmt.Errorf("从面板下载 %s 失败：%s %s", name, resp.Status, e.Error)
	}
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20)); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// ---- agent ----

type Agent struct {
	o        Options
	cfg      Config
	api      *apiClient
	proc     frp.Systemd
	confPath string
	frpcBin  string

	mu          sync.Mutex
	seen        string // revision of the last config received
	applied     string // hash of the config currently on disk
	frpcVersion string
	lastErr     string
	retryAfter  time.Time // upgrade back-off
}

func newAgent(o Options, cfg Config) *Agent {
	a := &Agent{
		o: o, cfg: cfg,
		api:      &apiClient{base: strings.TrimRight(cfg.Panel, "/"), token: cfg.Token, hc: newHTTPClient(cfg.Pin)},
		proc:     frp.Systemd{Unit: "frpc"},
		confPath: filepath.Join(o.FRPDir, "frpc.toml"),
		frpcBin:  filepath.Join(o.BinDir, "frpc"),
	}
	// seen starts empty so the first poll returns at once and the agent
	// catches up on anything that changed while it was not running.
	if b, err := os.ReadFile(a.confPath); err == nil {
		a.applied = fleet.Hash(b)
	}
	a.frpcVersion = frp.InstalledVersion(a.frpcBin)
	return a
}

func (a *Agent) setErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.lastErr = ""
		return
	}
	a.lastErr = err.Error()
	log.Print(err)
}

// Enroll registers this machine with the panel using the enrollment key from
// the join command, then installs frpc and the initial frpc.toml (without
// starting anything; the install script sets up the services).
func Enroll(o Options, panel, pin, key string, force bool) error {
	panel = strings.TrimRight(panel, "/")
	if !strings.HasPrefix(panel, "https://") {
		return errors.New("--panel 必须是 https:// 地址")
	}
	if !strings.HasPrefix(pin, "sha256//") {
		return errors.New("--pin 格式应为 sha256//<base64>")
	}
	ctx := context.Background()
	register := func() (Config, error) {
		host, _ := os.Hostname()
		api := &apiClient{base: panel, hc: newHTTPClient(pin)}
		var resp fleet.EnrollResponse
		if _, err := api.do(ctx, time.Minute, "POST", "/agent/v1/enroll",
			fleet.EnrollRequest{Key: key, Hostname: host, Arch: frp.ReleaseArch(), Version: o.Version}, &resp); err != nil {
			return Config{}, fmt.Errorf("注册到面板失败：%w", err)
		}
		c := Config{Panel: panel, Pin: pin, ID: resp.ID, Token: resp.Token}
		fmt.Printf("CLIENT_NAME=%s\n", resp.Name)
		return c, c.save(o.Data)
	}
	fetch := func(cfg Config) (*Agent, fleet.ConfigResponse, error) {
		a := newAgent(o, cfg)
		var cr fleet.ConfigResponse
		_, err := a.api.do(ctx, time.Minute, "GET", "/agent/v1/config?rev=", nil, &cr)
		return a, cr, err
	}

	// Re-running the join command keeps the machine's identity, unless it
	// was deleted in the panel (or points at another panel).
	cfg, err := Load(o.Data)
	reuse := err == nil && !force && cfg.Panel == panel && cfg.Pin == pin
	if !reuse {
		if cfg, err = register(); err != nil {
			return err
		}
	}
	a, cr, err := fetch(cfg)
	if reuse && statusOf(err) == http.StatusUnauthorized {
		if cfg, err = register(); err != nil {
			return err
		}
		a, cr, err = fetch(cfg)
	} else if reuse {
		fmt.Println("CLIENT_NAME=(已注册，沿用原有身份)")
	}
	if err != nil {
		return fmt.Errorf("获取配置失败：%w", err)
	}
	fmt.Printf("CLIENT_ID=%s\n", cfg.ID)

	if a.frpcVersion == "" || (cr.FRPVersion != "" && a.frpcVersion != cr.FRPVersion) {
		if err := a.installFRPC(ctx, cr.FRPVersion); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(o.FRPDir, 0o755); err != nil {
		return err
	}
	if err := frp.Verify(a.frpcBin, a.confPath, []byte(cr.Config)); err != nil {
		return err
	}
	return store.WriteFileAtomic(a.confPath, []byte(cr.Config), 0o640)
}

// Run keeps the machine in sync with the panel until ctx ends.
func Run(ctx context.Context, o Options) error {
	cfg, err := Load(o.Data)
	if err != nil {
		return fmt.Errorf("尚未注册到面板（%v）；请在服务端面板复制接入命令运行", err)
	}
	a := newAgent(o, cfg)
	log.Printf("FRP Panel agent %s: client %s, panel %s", o.Version, cfg.ID, cfg.Panel)
	go a.reportLoop(ctx)
	a.configLoop(ctx)
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (a *Agent) configLoop(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		a.mu.Lock()
		have := a.seen
		a.mu.Unlock()
		var cr fleet.ConfigResponse
		// The panel holds the request for up to ~25s until something changes.
		code, err := a.api.do(ctx, 60*time.Second, "GET", "/agent/v1/config?rev="+url.QueryEscape(have), nil, &cr)
		switch {
		case ctx.Err() != nil:
			return
		case statusOf(err) == http.StatusUnauthorized:
			log.Print("面板拒绝了本机的凭据（客户端可能已在面板中删除），停止 frpc")
			_ = a.proc.Stop()
			sleep(ctx, 5*time.Minute)
			continue
		case err != nil:
			log.Printf("连接面板失败：%v", err)
			sleep(ctx, backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = 2 * time.Second
		if code == http.StatusNoContent {
			continue
		}
		a.apply(ctx, cr)
	}
}

func (a *Agent) apply(ctx context.Context, cr fleet.ConfigResponse) {
	a.mu.Lock()
	a.seen = cr.Rev
	a.mu.Unlock()
	restarted := a.upgrade(ctx, cr)

	a.mu.Lock()
	changed := cr.Hash != a.applied
	a.mu.Unlock()
	if changed {
		if err := a.writeConfig([]byte(cr.Config)); err != nil {
			a.setErr(err)
			return
		}
		a.mu.Lock()
		a.applied, a.lastErr = cr.Hash, ""
		a.mu.Unlock()
		restarted = true
	}
	if cr.Restart && !restarted {
		a.setErr(a.proc.Restart())
	}
}

// writeConfig verifies and installs a new frpc.toml, then makes it effective:
// hot reload when only proxies changed, otherwise a restart.
func (a *Agent) writeConfig(b []byte) error {
	if err := frp.Verify(a.frpcBin, a.confPath, b); err != nil {
		return err
	}
	old, _ := frp.LoadDoc(a.confPath)
	if err := store.WriteFileAtomic(a.confPath, b, 0o640); err != nil {
		return err
	}
	d, err := frp.ParseDoc(b)
	if err != nil {
		return err
	}
	if !a.proc.Status().Running {
		log.Print("配置已更新，启动 frpc")
		return a.proc.Start()
	}
	if old != nil && !frp.CommonChanged(old, d) {
		if err := frp.AdminFromDoc(d).Reload(); err == nil {
			log.Print("配置已更新并热重载")
			return nil
		}
	}
	log.Print("配置已更新，重启 frpc")
	return a.proc.Restart()
}

// upgrade brings frpc and the agent itself to the versions the panel runs.
// It reports whether frpc was restarted. A successful self-update exits the
// process so systemd starts the new binary.
func (a *Agent) upgrade(ctx context.Context, cr fleet.ConfigResponse) bool {
	a.mu.Lock()
	wait, cur := time.Now().Before(a.retryAfter), a.frpcVersion
	a.mu.Unlock()
	if wait {
		return false
	}
	fail := func(err error) bool {
		a.setErr(err)
		a.mu.Lock()
		a.retryAfter = time.Now().Add(30 * time.Minute)
		a.mu.Unlock()
		return false
	}
	restarted := false
	if cr.FRPVersion != "" && cur != cr.FRPVersion {
		log.Printf("升级 frpc：%s -> %s", cur, cr.FRPVersion)
		if err := a.installFRPC(ctx, cr.FRPVersion); err != nil {
			return fail(err)
		}
		if a.proc.Status().Running {
			a.setErr(a.proc.Restart())
			restarted = true
		}
	}
	if cr.PanelVersion != "" && cr.PanelVersion != a.o.Version {
		log.Printf("更新 agent：%s -> %s", a.o.Version, cr.PanelVersion)
		if err := a.selfUpdate(ctx, cr.PanelVersion); err != nil {
			return fail(err)
		}
		log.Print("agent 已更新，退出以便 systemd 启动新版本")
		os.Exit(0)
	}
	return restarted
}

func (a *Agent) installFRPC(ctx context.Context, want string) error {
	tmp, err := a.api.download(ctx, "frpc", a.frpcBin)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := frp.CheckRuns(tmp); err != nil {
		return err
	}
	got := frp.InstalledVersion(tmp)
	if want != "" && got != want {
		return fmt.Errorf("面板提供的 frpc 版本是 %s，期望 %s", got, want)
	}
	if err := os.Rename(tmp, a.frpcBin); err != nil {
		return err
	}
	a.mu.Lock()
	a.frpcVersion = got
	a.mu.Unlock()
	return nil
}

func (a *Agent) selfUpdate(ctx context.Context, want string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	tmp, err := a.api.download(ctx, "frp-panel", exe)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	out, err := exec.Command(tmp, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("下载的 frp-panel 无法运行：%v %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != want {
		return fmt.Errorf("面板提供的 frp-panel 版本是 %s，期望 %s", got, want)
	}
	return os.Rename(tmp, exe)
}

// ---- reporting ----

func (a *Agent) reportLoop(ctx context.Context) {
	interval, wantLogs := 10*time.Second, false
	for ctx.Err() == nil {
		var rr fleet.ReportResponse
		if _, err := a.api.do(ctx, 30*time.Second, "POST", "/agent/v1/report", a.collect(wantLogs), &rr); err == nil {
			wantLogs = rr.WantLogs
			if rr.Interval > 0 {
				interval = time.Duration(min(max(rr.Interval, 2), 60)) * time.Second
			}
		}
		sleep(ctx, interval)
	}
}

func (a *Agent) collect(withLogs bool) fleet.Report {
	host, _ := os.Hostname()
	a.mu.Lock()
	rep := fleet.Report{Hostname: host, Version: a.o.Version, FRPVersion: a.frpcVersion, ConfigHash: a.applied, Error: a.lastErr}
	a.mu.Unlock()
	rep.Service = a.proc.Status()
	if d, err := frp.LoadDoc(a.confPath); err == nil {
		if st, err := frp.AdminFromDoc(d).ClientStatus(); err == nil {
			rep.Proxies, rep.AdminOK = st, true
		}
	}
	if withLogs {
		rep.Logs = a.proc.Logs(300)
	}
	return rep
}
