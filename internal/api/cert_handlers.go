package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"frp-panel/internal/auth"
	"frp-panel/internal/store"
)

const probePath = "/.well-known/acme-challenge/frp-panel-probe"

// startHTTP80 serves Let's Encrypt HTTP-01 challenges (and a self-check
// probe) on :80; every other request is redirected to the HTTPS panel. It
// starts once, on demand, and keeps running.
func (a *App) startHTTP80() error {
	a.http80Mu.Lock()
	defer a.http80Mu.Unlock()
	if a.http80On {
		return nil
	}
	if a.probeToken == "" {
		a.probeToken = auth.RandomToken()
	}
	_, port, _ := net.SplitHostPort(a.St.Get().Listen)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == probePath {
			_, _ = io.WriteString(w, a.probeToken)
			return
		}
		if ah := a.TLS.ACMEHTTPHandler(); ah != nil && strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") {
			ah.ServeHTTP(w, r)
			return
		}
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		http.Redirect(w, r, "https://"+net.JoinHostPort(host, port)+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
	ln, err := net.Listen("tcp", ":80")
	if err != nil {
		return err
	}
	a.http80On = true
	go func() { _ = (&http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}).Serve(ln) }()
	return nil
}

// port80Problem explains why :80 could not be opened.
func port80Problem(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "没有权限监听 80 端口（面板需要以 root 运行）"
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return "80 端口已被其他程序占用（例如 nginx / apache）。请停止它，或改用「上传证书」"
	}
	return "无法监听 80 端口：" + err.Error()
}

// ---- condition checks ----

type tlsCheck struct {
	Label  string `json:"label"`
	Status string `json:"status"` // ok | warn | fail | info
	Detail string `json:"detail"`
}

var domainRe = regexp.MustCompile(`^(?i)([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func localIPs() map[string]bool {
	out := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, ad := range addrs {
			if n, ok := ad.(*net.IPNet); ok {
				out[n.IP.String()] = true
			}
		}
	}
	return out
}

func (a *App) tlsChecks(ctx context.Context, domain string) []tlsCheck {
	var out []tlsCheck
	add := func(label, status, detail string) { out = append(out, tlsCheck{label, status, detail}) }
	if !domainRe.MatchString(domain) {
		add("域名格式", "fail", "请输入完整的域名，例如 panel.example.com（不能是 IP 地址）")
		return out
	}
	add("域名格式", "ok", domain)

	// DNS: Let's Encrypt connects to whatever the domain resolves to.
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, domain)
	if err != nil || len(ips) == 0 {
		add("域名解析", "fail", "无法解析 "+domain+"：请先添加 A 记录指向本服务器的公网 IP")
	} else {
		local := localIPs()
		var mine, others []string
		for _, ip := range ips {
			if local[ip.IP.String()] {
				mine = append(mine, ip.IP.String())
			} else {
				others = append(others, ip.IP.String())
			}
		}
		switch {
		case len(others) == 0:
			add("域名解析", "ok", "解析到 "+strings.Join(mine, "、")+"（本机）")
		case len(mine) > 0:
			add("域名解析", "warn", fmt.Sprintf("解析到本机 %s，同时也解析到 %s。Let's Encrypt 可能访问后者（IPv6 优先），请删除多余的记录",
				strings.Join(mine, "、"), strings.Join(others, "、")))
		default:
			add("域名解析", "warn", "解析到 "+strings.Join(others, "、")+"，不是本机网卡上的地址。如果服务器在 NAT 后面、这是它的公网 IP，可以忽略；如果开启了 CDN / 代理，请先关闭")
		}
	}

	// Port 80 for the HTTP-01 challenge.
	if err := a.startHTTP80(); err != nil {
		add("本机 80 端口", "fail", port80Problem(err))
		return out
	}
	add("本机 80 端口", "ok", "面板正在监听 80 端口，用于 Let's Encrypt 验证（其他请求会跳转到 HTTPS 面板）")

	// Self-check through the domain. From the server itself this only proves
	// DNS and the listener; the cloud security group is not on this path.
	pctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(pctx, "GET", "http://"+domain+probePath, nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := client.Do(req); err != nil {
		add("通过域名访问 80 端口", "warn", "本机自测失败（"+err.Error()+"）。服务器在 NAT 后面时自测可能不通，不一定影响申请")
	} else {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		a.http80Mu.Lock()
		want := a.probeToken
		a.http80Mu.Unlock()
		if strings.TrimSpace(string(b)) == want {
			add("通过域名访问 80 端口", "ok", "http://"+domain+" 能访问到本面板（本机自测）")
		} else {
			add("通过域名访问 80 端口", "fail", fmt.Sprintf("http://%s 返回了其他内容（HTTP %d），可能是 CDN、反向代理或其他服务器在响应", domain, resp.StatusCode))
		}
	}
	add("云服务商安全组", "info", "服务器无法自己检测外网能否访问 80 端口，请确认安全组 / 防火墙已放行 80/tcp（入站）")
	return out
}

func (a *App) handleTLSCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	writeJSON(w, 200, map[string]any{"checks": a.tlsChecks(ctx, strings.ToLower(strings.TrimSpace(req.Domain)))})
}

// ---- issue now ----

// acmeHint turns common Let's Encrypt failures into something actionable.
func acmeHint(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "ratelimit") || strings.Contains(m, "too many"):
		return "触发了 Let's Encrypt 的频率限制（同一域名每小时最多失败 5 次），请稍后再试"
	case strings.Contains(m, "caa"):
		return "域名的 CAA 记录不允许 Let's Encrypt 签发证书，请在 DNS 中添加或删除 CAA 记录"
	case strings.Contains(m, "nxdomain") || strings.Contains(m, "no valid a records") || strings.Contains(m, "dns problem"):
		return "Let's Encrypt 解析域名失败，请检查 A / AAAA 记录是否正确，新记录可能需要几分钟生效"
	case strings.Contains(m, "timeout") || strings.Contains(m, "connection refused") || strings.Contains(m, "connection reset") || strings.Contains(m, "fetching"):
		return "Let's Encrypt 无法从外网访问本机的 80 端口：请在云服务商安全组 / 防火墙放行 80/tcp（入站），并确认没有其他设备拦截"
	case strings.Contains(m, "unauthorized") || strings.Contains(m, "invalid response") || strings.Contains(m, "404"):
		return "Let's Encrypt 访问到的不是本面板：请确认域名直接解析到本机（关闭 CDN / 代理），且 80 端口没有被其他服务转发"
	case strings.Contains(m, "deadline exceeded") || strings.Contains(m, "context canceled"):
		return "申请仍在后台进行，稍后刷新页面查看结果"
	}
	return ""
}

func (a *App) handleTLSIssue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string `json:"domain"`
		Email  string `json:"email"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	email := strings.TrimSpace(req.Email)
	if !domainRe.MatchString(domain) {
		writeErr(w, 400, "请输入完整的域名，例如 panel.example.com")
		return
	}
	if err := a.startHTTP80(); err != nil {
		if _, port, _ := net.SplitHostPort(a.St.Get().Listen); port != "443" {
			writeErr(w, 400, port80Problem(err)+"，无法进行 Let's Encrypt 验证")
			return
		}
	}
	cfg := a.St.Get()
	if cfg.TLS.Mode != store.TLSACME || cfg.TLS.Domain != domain || cfg.TLS.Email != email {
		if err := a.TLS.Configure(store.TLSACME, domain, email, cfg.TLS.ExtraSANs); err != nil {
			_ = a.TLS.Configure(cfg.TLS.Mode, cfg.TLS.Domain, cfg.TLS.Email, cfg.TLS.ExtraSANs)
			writeErr(w, 400, err.Error())
			return
		}
		if err := a.St.Update(func(c *store.Config) {
			c.TLS.Mode, c.TLS.Domain, c.TLS.Email = store.TLSACME, domain, email
		}); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	err := a.TLS.IssueNow(ctx)
	st := a.tlsStatus()
	if err != nil {
		log.Printf("ACME issuance for %s: %v", domain, err)
		st["issueError"], st["hint"] = err.Error(), acmeHint(err.Error())
		if errors.Is(err, context.DeadlineExceeded) {
			st["pending"] = true
		}
	} else {
		log.Printf("ACME certificate for %s is ready", domain)
	}
	writeJSON(w, 200, st)
}

// warmACME loads (or obtains) the ACME certificate at startup, so it is
// ready, and renewed, even if nobody visits the panel by its domain.
func (a *App) warmACME() {
	if err := a.startHTTP80(); err != nil {
		if _, port, _ := net.SplitHostPort(a.St.Get().Listen); port != "443" {
			a.TLS.SetACMEError("无法监听 80 端口进行 HTTP-01 验证（" + err.Error() + "）；请释放 80 端口，或让面板监听 443 端口")
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := a.TLS.IssueNow(ctx); err != nil {
			log.Printf("ACME: %v", err)
		}
	}()
}
