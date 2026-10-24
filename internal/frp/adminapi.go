package frp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AdminAPI talks to frps' dashboard or frpc's admin server on 127.0.0.1.
type AdminAPI struct {
	Base, User, Pass string
}

var adminClient = &http.Client{Timeout: 5 * time.Second}

func AdminFromDoc(d Doc) AdminAPI {
	return AdminAPI{
		Base: fmt.Sprintf("http://127.0.0.1:%d", getInt(d, "webServer.port")),
		User: getStr(d, "webServer.user"),
		Pass: getStr(d, "webServer.password"),
	}
}

func (a AdminAPI) do(method, p string, out any) error {
	req, err := http.NewRequest(method, a.Base+p, nil)
	if err != nil {
		return err
	}
	if a.User != "" {
		req.SetBasicAuth(a.User, a.Pass)
	}
	resp, err := adminClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s %s: %s %s", method, p, resp.Status, body)
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (a AdminAPI) Get(p string, out any) error { return a.do("GET", p, out) }

// ---- frps ----

type ServerInfo struct {
	Version         string         `json:"version"`
	BindPort        int            `json:"bindPort"`
	VhostHTTPPort   int            `json:"vhostHTTPPort"`
	VhostHTTPSPort  int            `json:"vhostHTTPSPort"`
	SubdomainHost   string         `json:"subdomainHost"`
	TotalTrafficIn  int64          `json:"totalTrafficIn"`
	TotalTrafficOut int64          `json:"totalTrafficOut"`
	CurConns        int64          `json:"curConns"`
	ClientCounts    int64          `json:"clientCounts"`
	ProxyTypeCount  map[string]int `json:"proxyTypeCount"`
}

type ServerProxy struct {
	Name            string         `json:"name"`
	Conf            map[string]any `json:"conf"`
	ClientID        string         `json:"clientID"`
	ClientVersion   string         `json:"clientVersion"`
	TodayTrafficIn  int64          `json:"todayTrafficIn"`
	TodayTrafficOut int64          `json:"todayTrafficOut"`
	CurConns        int64          `json:"curConns"`
	LastStartTime   string         `json:"lastStartTime"`
	LastCloseTime   string         `json:"lastCloseTime"`
	Status          string         `json:"status"`
	Type            string         `json:"type"`
}

type ServerClient struct {
	ClientID         string `json:"clientID"`
	User             string `json:"user"`
	Version          string `json:"version"`
	Hostname         string `json:"hostname"`
	ClientIP         string `json:"clientIP"`
	FirstConnectedAt int64  `json:"firstConnectedAt"`
	LastConnectedAt  int64  `json:"lastConnectedAt"`
	Online           bool   `json:"online"`
}

func (a AdminAPI) ServerInfo() (ServerInfo, error) {
	var si ServerInfo
	return si, a.Get("/api/serverinfo", &si)
}

func (a AdminAPI) ServerProxies() ([]ServerProxy, error) {
	var all []ServerProxy
	for _, t := range []string{"tcp", "udp", "http", "https", "stcp", "xtcp", "sudp", "tcpmux"} {
		var r struct {
			Proxies []ServerProxy `json:"proxies"`
		}
		if err := a.Get("/api/proxy/"+t, &r); err != nil {
			return nil, err
		}
		for _, p := range r.Proxies {
			p.Type = t
			all = append(all, p)
		}
	}
	return all, nil
}

// ServerClients uses /api/clients (frps >= 0.6x); older versions return nil.
func (a AdminAPI) ServerClients() []ServerClient {
	var cs []ServerClient
	if err := a.Get("/api/clients", &cs); err != nil {
		return nil
	}
	return cs
}

// ---- frpc ----

type ClientProxyStatus struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Err        string `json:"err"`
	LocalAddr  string `json:"local_addr"`
	Plugin     string `json:"plugin"`
	RemoteAddr string `json:"remote_addr"`
}

func (a AdminAPI) ClientStatus() (map[string]ClientProxyStatus, error) {
	var r map[string][]ClientProxyStatus
	if err := a.Get("/api/status", &r); err != nil {
		return nil, err
	}
	out := map[string]ClientProxyStatus{}
	for _, list := range r {
		for _, s := range list {
			out[s.Name] = s
		}
	}
	return out, nil
}

func (a AdminAPI) Reload() error { return a.Get("/api/reload", nil) }
