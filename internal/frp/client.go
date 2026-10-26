package frp

import (
	"fmt"
	"regexp"
	"strings"
)

// ClientCommon is the connection part of frpc.toml.
type ClientCommon struct {
	ServerAddr string `json:"serverAddr"`
	ServerPort int    `json:"serverPort"`
	Token      string `json:"token"`
	User       string `json:"user"`
	Protocol   string `json:"protocol"` // tcp | kcp | quic | websocket | wss
	TLSEnable  bool   `json:"tlsEnable"`
	LogLevel   string `json:"logLevel"`
	LogMaxDays int    `json:"logMaxDays"`
}

func ReadClientCommon(d Doc) ClientCommon {
	return ClientCommon{
		ServerAddr: getStr(d, "serverAddr"),
		ServerPort: getInt(d, "serverPort"),
		Token:      getStr(d, "auth.token"),
		User:       getStr(d, "user"),
		Protocol:   getStr(d, "transport.protocol"),
		TLSEnable:  getBool(d, "transport.tls.enable", true), // FRP default since v0.50
		LogLevel:   getStr(d, "log.level"),
		LogMaxDays: getInt(d, "log.maxDays"),
	}
}

func ApplyClientCommon(d Doc, c ClientCommon) error {
	c.ServerAddr = strings.TrimSpace(c.ServerAddr)
	if c.ServerAddr == "" {
		return fmt.Errorf("请填写服务器地址")
	}
	if !validPort(c.ServerPort, false) {
		return fmt.Errorf("服务器端口无效：%d", c.ServerPort)
	}
	if err := ApplyClientOptions(d, c); err != nil {
		return err
	}
	set(d, "serverAddr", c.ServerAddr)
	set(d, "serverPort", int64(c.ServerPort))
	set(d, "auth.method", "token")
	set(d, "auth.token", strings.TrimSpace(c.Token))
	setOrDel(d, "user", strings.TrimSpace(c.User))
	// Keep retrying instead of exiting when the server is briefly unreachable.
	set(d, "loginFailExit", false)
	return nil
}

// ApplyClientOptions writes the transport and log settings of c, i.e. the
// part of the connection that a managed client may still tune per machine.
func ApplyClientOptions(d Doc, c ClientCommon) error {
	switch c.Protocol {
	case "", "tcp", "kcp", "quic", "websocket", "wss":
	default:
		return fmt.Errorf("不支持的传输协议：%q", c.Protocol)
	}
	if c.Protocol == "tcp" {
		c.Protocol = ""
	}
	setOrDel(d, "transport.protocol", c.Protocol)
	if c.TLSEnable {
		del(d, "transport.tls.enable")
	} else {
		set(d, "transport.tls.enable", false)
	}
	setOrDel(d, "log.level", c.LogLevel)
	setOrDel(d, "log.maxDays", c.LogMaxDays)
	return nil
}

// managedKeys are filled in by the server for every managed client, so they
// are never stored in (or taken from) a client's own config.
var managedKeys = []string{"serverAddr", "serverPort", "auth", "user", "loginFailExit"}

// NewManagedDoc is the starting config of a server-managed client; the
// connection keys are added when the server renders it.
func NewManagedDoc(genSecret func() string) Doc {
	d := Doc{}
	set(d, "log.level", "info")
	set(d, "log.maxDays", int64(7))
	EnforceLocalAdmin(d, 7400, genSecret)
	return d
}

// StripManaged removes the keys the server controls from a client config.
func StripManaged(d Doc) {
	for _, k := range managedKeys {
		delete(d, k)
	}
}

// Proxy is one [[proxies]] entry as edited in the UI.
type Proxy struct {
	Name              string   `json:"name"`
	Type              string   `json:"type"`
	LocalIP           string   `json:"localIP"`
	LocalPort         int      `json:"localPort"`
	RemotePort        int      `json:"remotePort"`
	CustomDomains     []string `json:"customDomains"`
	SubDomain         string   `json:"subdomain"`
	Locations         []string `json:"locations"`
	HTTPUser          string   `json:"httpUser"`
	HTTPPassword      string   `json:"httpPassword"`
	HostHeaderRewrite string   `json:"hostHeaderRewrite"`
	SecretKey         string   `json:"secretKey"`
	AllowUsers        []string `json:"allowUsers"`
	UseEncryption     bool     `json:"useEncryption"`
	UseCompression    bool     `json:"useCompression"`
	BandwidthLimit    string   `json:"bandwidthLimit"`
	Plugin            string   `json:"plugin,omitempty"` // read-only: set via raw editor
	Disabled          bool     `json:"disabled"`
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]{1,64}$`)
var bwRe = regexp.MustCompile(`^\d+(KB|MB)$`)

var ProxyTypes = []string{"tcp", "udp", "http", "https", "stcp", "xtcp"}

func Proxies(d Doc) []map[string]any {
	var out []map[string]any
	switch t := d["proxies"].(type) {
	case []any:
		for _, x := range t {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	case []map[string]any:
		out = t
	}
	return out
}

func setProxies(d Doc, ps []map[string]any) {
	if len(ps) == 0 {
		delete(d, "proxies")
		return
	}
	d["proxies"] = ps
}

func ReadProxy(m map[string]any) Proxy {
	p := Proxy{
		Name:              getStr(m, "name"),
		Type:              getStr(m, "type"),
		LocalIP:           getStr(m, "localIP"),
		LocalPort:         getInt(m, "localPort"),
		RemotePort:        getInt(m, "remotePort"),
		CustomDomains:     getStrs(m, "customDomains"),
		SubDomain:         getStr(m, "subdomain"),
		Locations:         getStrs(m, "locations"),
		HTTPUser:          getStr(m, "httpUser"),
		HTTPPassword:      getStr(m, "httpPassword"),
		HostHeaderRewrite: getStr(m, "hostHeaderRewrite"),
		SecretKey:         getStr(m, "secretKey"),
		AllowUsers:        getStrs(m, "allowUsers"),
		UseEncryption:     getBool(m, "transport.useEncryption", false),
		UseCompression:    getBool(m, "transport.useCompression", false),
		BandwidthLimit:    getStr(m, "transport.bandwidthLimit"),
		Plugin:            getStr(m, "plugin.type"),
		Disabled:          !getBool(m, "enabled", true),
	}
	if p.LocalIP == "" && p.Plugin == "" {
		p.LocalIP = "127.0.0.1"
	}
	return p
}

func cleanList(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func validateProxy(p *Proxy) error {
	p.Name = strings.TrimSpace(p.Name)
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("名称只能包含 1-64 个字母、数字、. - _")
	}
	known := false
	for _, t := range ProxyTypes {
		known = known || t == p.Type
	}
	if !known {
		return fmt.Errorf("不支持的代理类型：%q", p.Type)
	}
	p.CustomDomains = cleanList(p.CustomDomains)
	p.Locations = cleanList(p.Locations)
	p.AllowUsers = cleanList(p.AllowUsers)
	p.SubDomain = strings.TrimSpace(p.SubDomain)
	if p.Plugin == "" && !validPort(p.LocalPort, false) {
		return fmt.Errorf("本地端口无效：%d", p.LocalPort)
	}
	switch p.Type {
	case "tcp", "udp":
		if !validPort(p.RemotePort, true) {
			return fmt.Errorf("远程端口无效：%d", p.RemotePort)
		}
	case "http", "https":
		if len(p.CustomDomains) == 0 && p.SubDomain == "" {
			return fmt.Errorf("%s 代理需要填写自定义域名或子域名", p.Type)
		}
	case "stcp", "xtcp":
		if len(p.SecretKey) < 6 {
			return fmt.Errorf("密钥至少 6 个字符")
		}
	}
	if p.BandwidthLimit != "" && !bwRe.MatchString(p.BandwidthLimit) {
		return fmt.Errorf("带宽限制格式应为 512KB 或 10MB")
	}
	return nil
}

// applyProxy writes p into m, removing keys that don't apply to its type
// while leaving unknown keys (health checks, plugins, metadata…) untouched.
func applyProxy(m map[string]any, p Proxy) {
	set(m, "name", p.Name)
	set(m, "type", p.Type)
	if p.Plugin == "" {
		setOrDel(m, "localIP", strings.TrimSpace(p.LocalIP))
		setOrDel(m, "localPort", p.LocalPort)
	}
	isTCP := p.Type == "tcp" || p.Type == "udp"
	isHTTP := p.Type == "http" || p.Type == "https"
	isSecret := p.Type == "stcp" || p.Type == "xtcp"
	pick := func(cond bool, key string, v any) {
		if cond {
			setOrDel(m, key, v)
		} else {
			del(m, key)
		}
	}
	pick(isTCP, "remotePort", p.RemotePort)
	pick(isHTTP, "customDomains", p.CustomDomains)
	pick(isHTTP, "subdomain", p.SubDomain)
	pick(p.Type == "http", "locations", p.Locations)
	pick(p.Type == "http", "httpUser", strings.TrimSpace(p.HTTPUser))
	pick(p.Type == "http", "httpPassword", p.HTTPPassword)
	pick(p.Type == "http", "hostHeaderRewrite", strings.TrimSpace(p.HostHeaderRewrite))
	pick(isSecret, "secretKey", p.SecretKey)
	pick(isSecret, "allowUsers", p.AllowUsers)
	setOrDel(m, "transport.useEncryption", p.UseEncryption)
	setOrDel(m, "transport.useCompression", p.UseCompression)
	setOrDel(m, "transport.bandwidthLimit", p.BandwidthLimit)
	if p.Disabled {
		set(m, "enabled", false)
	} else {
		del(m, "enabled")
	}
}

// UpsertProxy adds a proxy or, when origName is set, replaces that proxy.
func UpsertProxy(d Doc, origName string, p Proxy) error {
	if err := validateProxy(&p); err != nil {
		return err
	}
	ps := Proxies(d)
	idx := -1
	for i, m := range ps {
		n := getStr(m, "name")
		if origName != "" && n == origName {
			idx = i
		} else if n == p.Name {
			return fmt.Errorf("已存在名为 %q 的代理", p.Name)
		}
	}
	if origName != "" && idx < 0 {
		return fmt.Errorf("找不到代理 %q", origName)
	}
	if idx < 0 {
		m := map[string]any{}
		applyProxy(m, p)
		ps = append(ps, m)
	} else {
		p.Plugin = getStr(ps[idx], "plugin.type")
		applyProxy(ps[idx], p)
	}
	setProxies(d, ps)
	return nil
}

func DeleteProxy(d Doc, name string) error {
	ps := Proxies(d)
	for i, m := range ps {
		if getStr(m, "name") == name {
			setProxies(d, append(ps[:i], ps[i+1:]...))
			return nil
		}
	}
	return fmt.Errorf("找不到代理 %q", name)
}

// CommonChanged reports whether anything outside proxies/visitors differs;
// frpc's hot reload only applies proxy changes, so those need a restart.
func CommonChanged(a, b Doc) bool {
	strip := func(d Doc) string {
		c := Doc{}
		for _, k := range sortedKeys(d) {
			if k != "proxies" && k != "visitors" {
				c[k] = d[k]
			}
		}
		out, _ := c.Marshal()
		return string(out)
	}
	return strip(a) != strip(b)
}

// RemotePorts maps "tcp/6000"-style keys to the names of the enabled proxies
// in d that claim that public port on the server.
func RemotePorts(d Doc) map[string]string {
	out := map[string]string{}
	for _, m := range Proxies(d) {
		p := ReadProxy(m)
		if p.Disabled || p.RemotePort == 0 || (p.Type != "tcp" && p.Type != "udp") {
			continue
		}
		out[fmt.Sprintf("%s/%d", p.Type, p.RemotePort)] = p.Name
	}
	return out
}
