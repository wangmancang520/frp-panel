package frp

import (
	"fmt"
	"strconv"
	"strings"
)

// ServerSettings is the subset of frps.toml editable from the UI.
type ServerSettings struct {
	BindAddr          string `json:"bindAddr"`
	BindPort          int    `json:"bindPort"`
	KCPBindPort       int    `json:"kcpBindPort"`
	QUICBindPort      int    `json:"quicBindPort"`
	VhostHTTPPort     int    `json:"vhostHTTPPort"`
	VhostHTTPSPort    int    `json:"vhostHTTPSPort"`
	SubDomainHost     string `json:"subDomainHost"`
	Token             string `json:"token"`
	TLSForce          bool   `json:"tlsForce"`
	AllowPorts        string `json:"allowPorts"` // "6000-7000,8080"
	MaxPortsPerClient int    `json:"maxPortsPerClient"`
	LogLevel          string `json:"logLevel"`
	LogMaxDays        int    `json:"logMaxDays"`
}

func ReadServer(d Doc) ServerSettings {
	return ServerSettings{
		BindAddr:          getStr(d, "bindAddr"),
		BindPort:          getInt(d, "bindPort"),
		KCPBindPort:       getInt(d, "kcpBindPort"),
		QUICBindPort:      getInt(d, "quicBindPort"),
		VhostHTTPPort:     getInt(d, "vhostHTTPPort"),
		VhostHTTPSPort:    getInt(d, "vhostHTTPSPort"),
		SubDomainHost:     getStr(d, "subDomainHost"),
		Token:             getStr(d, "auth.token"),
		TLSForce:          getBool(d, "transport.tls.force", false),
		AllowPorts:        formatPortRanges(d["allowPorts"]),
		MaxPortsPerClient: getInt(d, "maxPortsPerClient"),
		LogLevel:          getStr(d, "log.level"),
		LogMaxDays:        getInt(d, "log.maxDays"),
	}
}

func validPort(p int, allowZero bool) bool {
	return (allowZero && p == 0) || (p >= 1 && p <= 65535)
}

func ApplyServer(d Doc, s ServerSettings) error {
	if !validPort(s.BindPort, false) {
		return fmt.Errorf("监听端口无效：%d", s.BindPort)
	}
	for _, p := range []int{s.KCPBindPort, s.QUICBindPort, s.VhostHTTPPort, s.VhostHTTPSPort} {
		if !validPort(p, true) {
			return fmt.Errorf("端口无效：%d", p)
		}
	}
	if len(strings.TrimSpace(s.Token)) < 8 {
		return fmt.Errorf("令牌至少 8 个字符")
	}
	ranges, err := parsePortRanges(s.AllowPorts)
	if err != nil {
		return err
	}
	setOrDel(d, "bindAddr", strings.TrimSpace(s.BindAddr))
	set(d, "bindPort", int64(s.BindPort))
	setOrDel(d, "kcpBindPort", s.KCPBindPort)
	setOrDel(d, "quicBindPort", s.QUICBindPort)
	setOrDel(d, "vhostHTTPPort", s.VhostHTTPPort)
	setOrDel(d, "vhostHTTPSPort", s.VhostHTTPSPort)
	setOrDel(d, "subDomainHost", strings.TrimSpace(s.SubDomainHost))
	set(d, "auth.method", "token")
	set(d, "auth.token", strings.TrimSpace(s.Token))
	if s.TLSForce {
		set(d, "transport.tls.force", true)
	} else {
		del(d, "transport.tls.force")
	}
	if len(ranges) == 0 {
		delete(d, "allowPorts")
	} else {
		d["allowPorts"] = ranges
	}
	setOrDel(d, "maxPortsPerClient", s.MaxPortsPerClient)
	setOrDel(d, "log.level", s.LogLevel)
	setOrDel(d, "log.maxDays", s.LogMaxDays)
	return nil
}

// EnforceLocalAdmin pins FRP's own dashboard/admin API to loopback. That API
// uses HTTP basic auth over plaintext, so it must never be reachable from the
// network; the panel proxies everything users need over HTTPS.
func EnforceLocalAdmin(d Doc, defPort int, genSecret func() string) {
	set(d, "webServer.addr", "127.0.0.1")
	if getInt(d, "webServer.port") == 0 {
		set(d, "webServer.port", int64(defPort))
	}
	if getStr(d, "webServer.user") == "" {
		set(d, "webServer.user", "panel")
	}
	if getStr(d, "webServer.password") == "" {
		set(d, "webServer.password", genSecret())
	}
	set(d, "log.disablePrintColor", true)
}

func NewServerDoc(bindPort int, token string, genSecret func() string) Doc {
	d := Doc{}
	_ = ApplyServer(d, ServerSettings{BindPort: bindPort, Token: token, TLSForce: true, LogLevel: "info", LogMaxDays: 7})
	EnforceLocalAdmin(d, 7500, genSecret)
	return d
}

func parsePortRanges(s string) ([]any, error) {
	var out []any
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if a, b, ok := strings.Cut(part, "-"); ok {
			lo, e1 := strconv.Atoi(strings.TrimSpace(a))
			hi, e2 := strconv.Atoi(strings.TrimSpace(b))
			if e1 != nil || e2 != nil || !validPort(lo, false) || !validPort(hi, false) || lo > hi {
				return nil, fmt.Errorf("端口范围无效：%q", part)
			}
			out = append(out, map[string]any{"start": int64(lo), "end": int64(hi)})
		} else {
			p, err := strconv.Atoi(part)
			if err != nil || !validPort(p, false) {
				return nil, fmt.Errorf("端口无效：%q", part)
			}
			out = append(out, map[string]any{"single": int64(p)})
		}
	}
	return out, nil
}

func formatPortRanges(v any) string {
	var items []map[string]any
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			if m, ok := x.(map[string]any); ok {
				items = append(items, m)
			}
		}
	case []map[string]any:
		items = t
	}
	var parts []string
	for _, m := range items {
		if s := getInt(m, "single"); s != 0 {
			parts = append(parts, strconv.Itoa(s))
		} else if a, b := getInt(m, "start"), getInt(m, "end"); a != 0 {
			parts = append(parts, fmt.Sprintf("%d-%d", a, b))
		}
	}
	return strings.Join(parts, ",")
}

// TCPPorts maps each TCP port frps listens on (besides proxy remote ports)
// to the setting that opens it, e.g. 80 -> "HTTP 端口（vhostHTTPPort）".
func TCPPorts(d Doc) map[int]string {
	out := map[int]string{}
	for _, k := range []struct{ key, label string }{
		{"bindPort", "监听端口（bindPort）"},
		{"vhostHTTPPort", "HTTP 端口（vhostHTTPPort）"},
		{"vhostHTTPSPort", "HTTPS 端口（vhostHTTPSPort）"},
		{"tcpmuxHTTPConnectPort", "TCPMux 端口（tcpmuxHTTPConnectPort）"},
	} {
		if p := getInt(d, k.key); p > 0 {
			if _, dup := out[p]; !dup {
				out[p] = k.label
			}
		}
	}
	return out
}

// ServerPorts lists every port frps listens on publicly, for firewall hints.
func ServerPorts(d Doc) []string {
	var out []string
	add := func(proto string, p int) {
		if p > 0 {
			out = append(out, fmt.Sprintf("%d/%s", p, proto))
		}
	}
	add("tcp", getInt(d, "bindPort"))
	add("udp", getInt(d, "kcpBindPort"))
	add("udp", getInt(d, "quicBindPort"))
	add("tcp", getInt(d, "vhostHTTPPort"))
	add("tcp", getInt(d, "vhostHTTPSPort"))
	if ap := formatPortRanges(d["allowPorts"]); ap != "" {
		out = append(out, strings.ReplaceAll(ap, "-", ":")+" (tcp+udp, proxy ports)")
	}
	return out
}
