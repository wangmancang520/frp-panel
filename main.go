// Command frp-panel is a web panel for deploying and managing frps / frpc.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"frp-panel/internal/api"
	"frp-panel/internal/auth"
	"frp-panel/internal/frp"
	"frp-panel/internal/store"
	"frp-panel/internal/tlsx"
)

var version = "0.1.0"

//go:embed web
var webFS embed.FS

const usage = `FRP Panel — 一键部署与图形化管理 FRP

Usage:
  frp-panel init  --role server|client [flags]   初始化面板与 FRP 配置
  frp-panel serve [--data DIR]                   启动 HTTPS 面板
  frp-panel reset-password [--data DIR]          重置管理员密码
  frp-panel info [--data DIR]                    显示访问地址与证书指纹
  frp-panel version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "reset-password":
		err = cmdResetPassword(os.Args[2:])
	case "info":
		err = cmdInfo(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println(version)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdInit(args []string) error {
	fl := flag.NewFlagSet("init", flag.ExitOnError)
	role := fl.String("role", "", "server or client")
	data := fl.String("data", "/etc/frp-panel", "panel data directory")
	frpDir := fl.String("frp-dir", "/etc/frp", "FRP config directory")
	binDir := fl.String("bin-dir", "/usr/local/bin", "directory containing frps/frpc")
	listen := fl.String("listen", ":7600", "panel HTTPS listen address")
	procMode := fl.String("proc", "systemd", "process manager: systemd or exec")
	publicAddr := fl.String("public-addr", "", "public IP/hostname of this machine (added to the certificate)")
	mirror := fl.String("mirror", "", "GitHub download mirror prefix, e.g. https://ghfast.top/")
	scriptURL := fl.String("script-url", "", "install.sh URL used in the client join command")
	bindPort := fl.Int("bind-port", 7000, "[server] frps bind port")
	server := fl.String("server", "", "[client] frps address")
	port := fl.Int("port", 7000, "[client] frps port")
	token := fl.String("token", "", "auth token (server: generated if empty)")
	force := fl.Bool("force", false, "overwrite an existing panel config")
	_ = fl.Parse(args)

	if *role != store.RoleServer && *role != store.RoleClient {
		return errors.New("--role must be server or client")
	}
	if *procMode != "systemd" && *procMode != "exec" {
		return errors.New("--proc must be systemd or exec")
	}
	if _, err := os.Stat(store.Path(*data)); err == nil && !*force {
		return fmt.Errorf("%s already exists (use --force to re-initialize)", store.Path(*data))
	}
	if err := os.MkdirAll(*frpDir, 0o755); err != nil {
		return err
	}

	cfg := store.Config{
		Role: *role, Listen: *listen, FRPDir: *frpDir, BinDir: *binDir, ProcMode: *procMode,
		PublicAddr: *publicAddr, GithubMirror: *mirror, InstallScriptURL: *scriptURL,
		TLS: store.TLSConfig{Mode: store.TLSSelfSigned},
	}
	if *publicAddr != "" {
		cfg.TLS.ExtraSANs = []string{*publicAddr}
	}
	password := auth.RandomString(16)
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	cfg.Admin = store.Admin{Username: "admin", PasswordHash: hash, MustChange: true}

	// FRP config: keep an existing file (re-install), otherwise create one.
	confPath := cfg.FRPConfigPath()
	var d frp.Doc
	if existing, err := frp.LoadDoc(confPath); err == nil {
		d = existing
		def := 7400
		if *role == store.RoleServer {
			def = 7500
		}
		frp.EnforceLocalAdmin(d, def, func() string { return auth.RandomString(24) })
		if *role == store.RoleClient && *server != "" {
			c := frp.ReadClientCommon(d)
			c.ServerAddr, c.ServerPort = *server, *port
			if *token != "" {
				c.Token = *token
			}
			if err := frp.ApplyClientCommon(d, c); err != nil {
				return err
			}
		}
	} else if *role == store.RoleServer {
		if *token == "" {
			*token = auth.RandomString(32)
		}
		d = frp.NewServerDoc(*bindPort, *token, func() string { return auth.RandomString(24) })
	} else {
		if *server == "" || *token == "" {
			return errors.New("client init requires --server and --token")
		}
		d, err = frp.NewClientDoc(frp.ClientCommon{ServerAddr: *server, ServerPort: *port, Token: *token}, func() string { return auth.RandomString(24) })
		if err != nil {
			return err
		}
	}
	b, err := d.Marshal()
	if err != nil {
		return err
	}
	if err := store.WriteFileAtomic(confPath, b, 0o640); err != nil {
		return err
	}

	if _, err := store.Create(*data, cfg); err != nil {
		return err
	}
	tlsDir := filepath.Join(*data, "tls")
	if err := tlsx.GenerateSelfSigned(tlsDir, cfg.TLS.ExtraSANs); err != nil {
		return err
	}
	fp, _ := tlsx.SelfSignedFingerprint(tlsDir)

	// KEY=VALUE lines for install.sh to parse.
	fmt.Printf("ROLE=%s\n", *role)
	fmt.Printf("PANEL_PORT=%s\n", portOf(*listen))
	fmt.Printf("ADMIN_USER=admin\nADMIN_PASSWORD=%s\n", password)
	fmt.Printf("CERT_FINGERPRINT=%s\n", fp)
	fmt.Printf("FRP_CONFIG=%s\n", confPath)
	if *role == store.RoleServer {
		s := frp.ReadServer(d)
		fmt.Printf("BIND_PORT=%d\nTOKEN=%s\n", s.BindPort, s.Token)
	}
	return nil
}

func portOf(listen string) string {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	return p
}

func cmdServe(args []string) error {
	fl := flag.NewFlagSet("serve", flag.ExitOnError)
	data := fl.String("data", "/etc/frp-panel", "panel data directory")
	_ = fl.Parse(args)

	st, err := store.Load(*data)
	if err != nil {
		return err
	}
	cfg := st.Get()
	tm := tlsx.NewManager(filepath.Join(*data, "tls"))
	if err := tm.Configure(cfg.TLS.Mode, cfg.TLS.Domain, cfg.TLS.Email, cfg.TLS.ExtraSANs); err != nil {
		log.Printf("TLS mode %q failed (%v); falling back to self-signed", cfg.TLS.Mode, err)
		if err := tm.Configure(store.TLSSelfSigned, "", "", cfg.TLS.ExtraSANs); err != nil {
			return err
		}
	}

	var proc frp.Manager
	unit := "frpc"
	if cfg.Role == store.RoleServer {
		unit = "frps"
	}
	if cfg.ProcMode == "exec" {
		e := &frp.Exec{Bin: cfg.FRPBinary(), Args: []string{"-c", cfg.FRPConfigPath()}}
		if err := e.Start(); err != nil {
			log.Printf("start %s: %v", unit, err)
		}
		defer e.Stop()
		proc = e
	} else {
		proc = frp.Systemd{Unit: unit}
	}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := &api.App{Version: version, St: st, TLS: tm, Proc: proc, Web: web}
	return app.Serve(ctx)
}

func cmdResetPassword(args []string) error {
	fl := flag.NewFlagSet("reset-password", flag.ExitOnError)
	data := fl.String("data", "/etc/frp-panel", "panel data directory")
	_ = fl.Parse(args)
	st, err := store.Load(*data)
	if err != nil {
		return err
	}
	pw := auth.RandomString(16)
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	if err := st.Update(func(c *store.Config) {
		c.Admin.PasswordHash, c.Admin.MustChange = hash, true
	}); err != nil {
		return err
	}
	fmt.Printf("用户名: %s\n新密码: %s\n（登录后需立即修改；如面板正在运行，请重启 frp-panel 使旧会话失效）\n", st.Get().Admin.Username, pw)
	return nil
}

func cmdInfo(args []string) error {
	fl := flag.NewFlagSet("info", flag.ExitOnError)
	data := fl.String("data", "/etc/frp-panel", "panel data directory")
	_ = fl.Parse(args)
	st, err := store.Load(*data)
	if err != nil {
		return err
	}
	cfg := st.Get()
	fp, _ := tlsx.SelfSignedFingerprint(filepath.Join(*data, "tls"))
	host := cfg.PublicAddr
	if host == "" {
		host = "<服务器IP>"
	}
	p, _ := strconv.Atoi(portOf(cfg.Listen))
	fmt.Printf("角色: %s\n面板地址: https://%s\n证书指纹 (SHA-256): %s\nFRP 配置: %s\n",
		cfg.Role, net.JoinHostPort(host, strconv.Itoa(p)), fp, cfg.FRPConfigPath())
	return nil
}
