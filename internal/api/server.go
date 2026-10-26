// Package api is the panel's HTTPS server: auth, JSON API and static UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"frp-panel/internal/auth"
	"frp-panel/internal/fleet"
	"frp-panel/internal/frp"
	"frp-panel/internal/store"
	"frp-panel/internal/tlsx"
)

const cookieName = "__Host-frp_panel"

type App struct {
	Version string
	St      *store.Store
	TLS     *tlsx.Manager
	Proc    frp.Manager
	Web     fs.FS
	Fleet   *fleet.Registry
	// InstallScript is served at /join.sh for the client join command.
	InstallScript []byte

	sess    *auth.Sessions
	lim     *auth.Limiter
	cfgMu   sync.Mutex // serializes FRP config writes
	hist    *history
	started time.Time
	dlMu    sync.Mutex // serializes agent binary downloads into the cache

	verMu sync.Mutex
	ver   string
	verAt time.Time

	acmeOnce sync.Once
}

func (a *App) Serve(ctx context.Context) error {
	a.sess, a.lim, a.started = auth.NewSessions(), auth.NewLimiter(), time.Now()
	cfg := a.St.Get()
	a.hist = newHistory(360)
	go a.sample(ctx)
	if cfg.TLS.Mode == store.TLSACME {
		a.startACMEHTTP()
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	rl := tlsx.NewRedirectListener(ln, a.TLS.TLSConfig())
	srv := &http.Server{
		Handler:           a.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(filteredLog{}, "", 0),
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("FRP Panel %s (%s) listening on https://%s", a.Version, cfg.Role, cfg.Listen)
	// The listener already performs TLS (with plaintext redirect), so Serve —
	// not ServeTLS — is correct here.
	err = srv.Serve(rl)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// filteredLog drops the noisy "TLS handshake error" lines browsers produce
// every time a user looks at the self-signed warning page.
type filteredLog struct{}

func (filteredLog) Write(p []byte) (int, error) {
	if !strings.Contains(string(p), "TLS handshake error") {
		log.Print(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}

// startACMEHTTP serves HTTP-01 challenges on :80 (only the challenge path;
// everything else redirects to the HTTPS panel). TLS-ALPN-01 also works when
// the panel itself listens on :443.
func (a *App) startACMEHTTP() {
	a.acmeOnce.Do(func() {
		_, port, _ := net.SplitHostPort(a.St.Get().Listen)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			if port != "443" {
				a.TLS.SetACMEError("无法监听 80 端口进行 HTTP-01 验证（" + err.Error() + "）；请释放 80 端口，或让面板监听 443 端口")
			}
			return
		}
		go func() { _ = (&http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}).Serve(ln) }()
	})
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", a.handleLogin)

	authed := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.requireAuth(h)) }
	authed("POST /api/logout", a.handleLogout)
	authed("GET /api/me", a.handleMe)
	authed("POST /api/password", a.handlePassword)
	authed("GET /api/system", a.handleSystem)
	authed("POST /api/service/{action}", a.handleService)
	authed("GET /api/logs", a.handleLogs)
	authed("GET /api/frp/latest", a.handleLatest)
	authed("POST /api/frp/upgrade", a.handleUpgrade)
	authed("PUT /api/panel/settings", a.handlePanelSettings)
	authed("GET /api/tls", a.handleTLSGet)
	authed("PUT /api/tls", a.handleTLSPut)
	authed("POST /api/tls/regenerate", a.handleTLSRegen)
	authed("POST /api/tls/custom", a.handleTLSCustom)
	authed("GET /api/raw", a.handleRawGet)
	authed("PUT /api/raw", a.handleRawPut)

	authed("GET /api/server/overview", a.handleServerOverview)
	authed("GET /api/server/settings", a.handleServerSettingsGet)
	authed("PUT /api/server/settings", a.handleServerSettingsPut)
	authed("GET /api/server/proxies", a.handleServerProxies)
	authed("GET /api/server/join", a.handleServerJoin)
	authed("POST /api/server/join/reset", a.handleServerJoinReset)

	// Managed clients.
	authed("GET /api/clients", a.handleClients)
	authed("GET /api/clients/{id}", a.handleClientGet)
	authed("PUT /api/clients/{id}", a.handleClientPut)
	authed("DELETE /api/clients/{id}", a.handleClientDelete)
	authed("POST /api/clients/{id}/restart", a.handleClientRestart)
	authed("GET /api/clients/{id}/proxies", a.handleClientProxies)
	authed("POST /api/clients/{id}/proxies", a.handleClientProxyCreate)
	authed("PUT /api/clients/{id}/proxies/{name}", a.handleClientProxyUpdate)
	authed("DELETE /api/clients/{id}/proxies/{name}", a.handleClientProxyDelete)
	authed("GET /api/clients/{id}/logs", a.handleClientLogs)
	authed("GET /api/clients/{id}/raw", a.handleClientRawGet)
	authed("PUT /api/clients/{id}/raw", a.handleClientRawPut)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "接口不存在") })

	// Agents on client machines (token auth, no cookies) and their installer.
	mux.HandleFunc("GET /join.sh", a.handleJoinScript)
	mux.HandleFunc("POST /agent/v1/enroll", a.handleAgentEnroll)
	mux.HandleFunc("GET /agent/v1/config", a.agentAuth(a.handleAgentConfig))
	mux.HandleFunc("POST /agent/v1/report", a.agentAuth(a.handleAgentReport))
	mux.HandleFunc("GET /agent/v1/download/{name}", a.handleAgentDownload)
	mux.HandleFunc("/agent/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, 404, "接口不存在") })
	files := http.FileServerFS(a.Web)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, 405, "不支持的请求方法")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	return a.secure(mux)
}

func (a *App) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(r.URL.Path, "/agent/") {
			h.Set("Cache-Control", "no-store")
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
			if r.Method != http.MethodGet && !sameOrigin(r) {
				writeErr(w, 403, "已拒绝跨站请求")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin is CSRF defence in depth on top of SameSite=Strict cookies: a
// custom header can't be sent cross-site without a CORS preflight we never
// approve, and Origin (when present) must match the Host.
func sameOrigin(r *http.Request) bool {
	if r.Header.Get("X-FRP-Panel") != "1" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	return true
}

type ctxKey struct{}

func (a *App) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, 401, "未登录")
			return
		}
		if _, ok := a.sess.Get(c.Value); !ok {
			writeErr(w, 401, "登录已过期，请重新登录")
			return
		}
		if a.St.Get().Admin.MustChange {
			switch r.URL.Path {
			case "/api/me", "/api/password", "/api/logout":
			default:
				writeErr(w, 403, "请先修改初始密码")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c.Value)))
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, 400, "请求格式错误："+err.Error())
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
