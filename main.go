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

	"frp-panel/internal/agent"
	"frp-panel/internal/api"
	"frp-panel/internal/auth"
	"frp-panel/internal/fleet"
	"frp-panel/internal/frp"
	"frp-panel/internal/store"
	"frp-panel/internal/tlsx"
)

var version = "0.1.0"

//go:embed web
var webFS embed.FS

// installScript is served to client machines at /join.sh.
//
//go:embed install.sh
var installScript []byte

const usage = `FRP Panel — 一键部署与集中管理 FRP

Usage:
  frp-panel init  --role server [flags]          初始化服务端面板与 frps 配置
  frp-panel serve [--data DIR]                   启动 HTTPS 面板
  frp-panel agent enroll --panel URL --pin PIN --key KEY
                                                 把本机注册为受管客户端（由 install.sh 调用）
  frp-panel agent run [--data DIR]               运行客户端 agent
  frp-panel reset-password [--data DIR]          重置管理员密码
  frp-panel info [--data DIR]                    显示访问地址、证书指纹与客户端接入命令
  frp-panel install-script                       输出内置的安装 / 管理脚本
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
	case "agent":
		err = cmdAgent(os.Args[2:])
	case "reset-password":
		err = cmdResetPassword(os.Args[2:])
	case "info":
		err = cmdInfo(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println(version)
	case "install-script":
		// Used by the installer to refresh frp-panel-ctl after an update,
		// so the management script always matches the installed binary.
		_, err = os.Stdout.Write(installScript)
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
	role := fl.String("role", store.RoleServer, "only \"server\" (clients are installed as agents with the join command)")
	data := fl.String("data", "/etc/frp-panel", "panel data directory")
	frpDir := fl.String("frp-dir", "/etc/frp", "FRP config directory")
	binDir := fl.String("bin-dir", "/usr/local/bin", "directory containing frps/frpc")
	listen := fl.String("listen", ":7600", "panel HTTPS listen address")
	procMode := fl.String("proc", "systemd", "process manager: systemd or exec")
	publicAddr := fl.String("public-addr", "", "public IP/hostname of this machine (added to the certificate)")
	mirror := fl.String("mirror", "", "GitHub download mirror prefix, e.g. https://ghfast.top/")
	bindPort := fl.Int("bind-port", 7000, "frps bind port")
	token := fl.String("token", "", "frps auth token (generated if empty)")
	force := fl.Bool("force", false, "overwrite an existing panel config")
	_ = fl.Parse(args)

	if *role != store.RoleServer {
		return errors.New("面板只运行在服务端（--role server）；客户端请在服务端面板复制接入命令安装")
	}
	if *procMode != "systemd" && *procMode != "exec" {
		return errors.New("--proc must be systemd or exec")
	}
	var enrollKey string
	if old, err := store.Load(*data); err == nil {
		if !*force {
			return fmt.Errorf("%s already exists (use --force to re-initialize)", store.Path(*data))
		}
		// Keep already-printed join commands working across a re-install.
		enrollKey = old.Get().EnrollKey
	}
	if enrollKey == "" {
		enrollKey = auth.RandomString(32)
	}
	if err := os.MkdirAll(*frpDir, 0o755); err != nil {
		return err
	}

	cfg := store.Config{
		Role: *role, Listen: *listen, FRPDir: *frpDir, BinDir: *binDir, ProcMode: *procMode,
		PublicAddr: *publicAddr, GithubMirror: *mirror, EnrollKey: enrollKey,
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

	// frps config: keep an existing file (re-install), otherwise create one.
	confPath := cfg.FRPConfigPath()
	d, err := frp.LoadDoc(confPath)
	if err == nil {
		frp.EnforceLocalAdmin(d, 7500, func() string { return auth.RandomString(24) })
	} else {
		if *token == "" {
			*token = auth.RandomString(32)
		}
		d = frp.NewServerDoc(*bindPort, *token, func() string { return auth.RandomString(24) })
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
	host := *publicAddr
	if host == "" {
		host = "<服务器IP>"
	}
	join, _, err := api.JoinCommand(cfg, *data, api.JoinHost(host, ""))
	if err != nil {
		return err
	}

	// KEY=VALUE lines for install.sh to parse.
	s := frp.ReadServer(d)
	fmt.Printf("ROLE=%s\n", *role)
	fmt.Printf("PANEL_PORT=%s\n", portOf(*listen))
	fmt.Printf("ADMIN_USER=admin\nADMIN_PASSWORD=%s\n", password)
	fmt.Printf("CERT_FINGERPRINT=%s\n", fp)
	fmt.Printf("FRP_CONFIG=%s\n", confPath)
	fmt.Printf("BIND_PORT=%d\n", s.BindPort)
	fmt.Printf("JOIN_COMMAND=%s\n", join)
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
	if st.Get().Role != store.RoleServer {
		return errors.New("此版本不再提供客户端面板。请卸载后，在服务端面板「客户端」页复制接入命令，把本机重新安装为受管客户端")
	}
	if st.Get().EnrollKey == "" { // installed by an older version
		if err := st.Update(func(c *store.Config) { c.EnrollKey = auth.RandomString(32) }); err != nil {
			return err
		}
	}
	reg, err := fleet.Open(filepath.Join(*data, "clients.json"))
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
	if cfg.ProcMode == "exec" {
		e := &frp.Exec{Bin: cfg.FRPBinary(), Args: []string{"-c", cfg.FRPConfigPath()}}
		if err := e.Start(); err != nil {
			log.Printf("start frps: %v", err)
		}
		defer e.Stop()
		proc = e
	} else {
		proc = frp.Systemd{Unit: "frps"}
	}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := &api.App{Version: version, St: st, TLS: tm, Proc: proc, Web: web, Fleet: reg, InstallScript: installScript}
	return app.Serve(ctx)
}

func cmdAgent(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		os.Exit(2)
	}
	fl := flag.NewFlagSet("agent "+args[0], flag.ExitOnError)
	data := fl.String("data", "/etc/frp-panel", "agent data directory")
	frpDir := fl.String("frp-dir", "/etc/frp", "frpc config directory")
	binDir := fl.String("bin-dir", "/usr/local/bin", "directory for the frpc binary")
	panel := fl.String("panel", "", "[enroll] server panel URL, e.g. https://1.2.3.4:7600")
	pin := fl.String("pin", "", "[enroll] panel public key pin (sha256//...)")
	key := fl.String("key", "", "[enroll] enrollment key from the join command")
	force := fl.Bool("force", false, "[enroll] register again even if already enrolled")
	_ = fl.Parse(args[1:])
	o := agent.Options{Data: *data, FRPDir: *frpDir, BinDir: *binDir, Version: version}
	switch args[0] {
	case "enroll":
		if *panel == "" || *pin == "" || *key == "" {
			return errors.New("agent enroll 需要 --panel、--pin 和 --key（请使用服务端面板提供的接入命令）")
		}
		return agent.Enroll(o, *panel, *pin, *key, *force)
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return agent.Run(ctx, o)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	return nil
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
	if ac, err := agent.Load(*data); err == nil {
		fmt.Printf("角色: 受管客户端\n客户端 ID: %s\n服务端面板: %s\n", ac.ID, ac.Panel)
		return nil
	}
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
	fmt.Printf("面板地址: https://%s\n证书指纹 (SHA-256): %s\nFRP 配置: %s\n",
		net.JoinHostPort(host, strconv.Itoa(p)), fp, cfg.FRPConfigPath())
	if join, _, err := api.JoinCommand(cfg, *data, api.JoinHost(host, "")); err == nil {
		fmt.Printf("客户端接入命令:\n  %s\n", join)
	}
	return nil
}
