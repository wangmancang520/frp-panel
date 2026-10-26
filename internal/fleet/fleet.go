// Package fleet is the server's registry of managed clients: who they are,
// the frpc.toml the server maintains for each one, and what their agents
// last reported. It also defines the agent <-> panel protocol.
package fleet

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"frp-panel/internal/frp"
	"frp-panel/internal/store"
)

// ---- protocol (agent <-> panel, under /agent/v1/) ----

type EnrollRequest struct {
	Key      string `json:"key"`
	Hostname string `json:"hostname"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
}

type EnrollResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// ConfigResponse is what an agent should be running.
type ConfigResponse struct {
	// Rev changes whenever anything below does; agents long-poll with it.
	Rev          string `json:"rev"`
	Hash         string `json:"hash"`   // SHA-256 of Config
	Config       string `json:"config"` // complete frpc.toml
	FRPVersion   string `json:"frpVersion"`
	PanelVersion string `json:"panelVersion"`
	Restart      bool   `json:"restart"`
}

// Report is the agent's periodic status update.
type Report struct {
	Hostname   string                           `json:"hostname"`
	Version    string                           `json:"version"`
	FRPVersion string                           `json:"frpVersion"`
	Service    frp.Status                       `json:"service"`
	AdminOK    bool                             `json:"adminOK"`
	Proxies    map[string]frp.ClientProxyStatus `json:"proxies"`
	ConfigHash string                           `json:"configHash"`
	Error      string                           `json:"error"`
	Logs       []string                         `json:"logs,omitempty"`
}

type ReportResponse struct {
	Interval int  `json:"interval"` // seconds until the next report
	WantLogs bool `json:"wantLogs"`
}

// Hash identifies a rendered config.
func Hash(config []byte) string {
	sum := sha256.Sum256(config)
	return hex.EncodeToString(sum[:])
}

// Rev identifies a config together with the versions agents should run.
func Rev(hash, frpVersion, panelVersion string) string {
	return Hash([]byte(hash + "\n" + frpVersion + "\n" + panelVersion))[:16]
}

// ---- registry ----

// Client is a managed machine as persisted in clients.json.
type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"tokenHash"`
	Hostname  string    `json:"hostname"`
	Arch      string    `json:"arch"`
	Created   time.Time `json:"created"`
	// Config is the client's frpc.toml without the server-managed
	// connection keys (see frp.StripManaged); Render fills those in.
	Config string `json:"config"`
}

// Live is the in-memory state reported by a client's agent.
type Live struct {
	LastSeen   time.Time `json:"lastSeen"`
	RemoteIP   string    `json:"remoteIP"`
	PanelHost  string    `json:"-"` // Host the agent used to reach the panel
	Report     Report    `json:"report"`
	Logs       []string  `json:"-"`
	logsWanted time.Time
	restart    bool
}

const (
	reportInterval = 10
	logsInterval   = 3
	// OnlineWithin is how recent a report must be for a client to count as online.
	OnlineWithin = 3 * reportInterval * time.Second
)

type Registry struct {
	path string

	mu      sync.Mutex
	clients map[string]*Client
	live    map[string]*Live
	byToken map[string]string // token hash -> id
	changed chan struct{}
}

func Open(path string) (*Registry, error) {
	r := &Registry{path: path, clients: map[string]*Client{}, live: map[string]*Live{}, byToken: map[string]string{}, changed: make(chan struct{})}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var list []*Client
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, c := range list {
		r.clients[c.ID] = c
		r.byToken[c.TokenHash] = c.ID
	}
	return r, nil
}

// save persists the registry; caller holds r.mu.
func (r *Registry) save() error {
	list := make([]*Client, 0, len(r.clients))
	for _, c := range r.clients {
		list = append(list, c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFileAtomic(r.path, b, 0o600)
}

// Changed returns a channel that is closed on the next Notify. Take it
// before reading state so that no change can slip in between.
func (r *Registry) Changed() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changed
}

// Notify wakes every agent waiting for a new config, e.g. after the server
// settings their configs are rendered from have changed.
func (r *Registry) Notify() {
	r.mu.Lock()
	r.notifyLocked()
	r.mu.Unlock()
}

func (r *Registry) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func tokenHash(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// Enroll registers a new client and returns it with its agent token, which
// is only stored hashed.
func (r *Registry) Enroll(hostname, arch string, genID, genToken, genSecret func() string) (Client, string, error) {
	hostname = strings.TrimSpace(hostname)
	if len(hostname) > 64 {
		hostname = hostname[:64]
	}
	d := frp.NewManagedDoc(genSecret)
	b, err := d.Marshal()
	if err != nil {
		return Client{}, "", err
	}
	tok := genToken()
	r.mu.Lock()
	defer r.mu.Unlock()
	id := "c" + genID()
	for r.clients[id] != nil {
		id = "c" + genID()
	}
	name := hostname
	if name == "" {
		name = id
	}
	c := &Client{ID: id, Name: name, TokenHash: tokenHash(tok), Hostname: hostname, Arch: arch, Created: time.Now(), Config: string(b)}
	r.clients[id] = c
	r.byToken[c.TokenHash] = id
	if err := r.save(); err != nil {
		delete(r.clients, id)
		delete(r.byToken, c.TokenHash)
		return Client{}, "", err
	}
	return *c, tok, nil
}

// Authenticate maps an agent token to its client.
func (r *Registry) Authenticate(token string) (Client, bool) {
	if token == "" {
		return Client{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[r.byToken[tokenHash(token)]]
	if !ok {
		return Client{}, false
	}
	return *c, true
}

func (r *Registry) Get(id string) (Client, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[id]
	if !ok {
		return Client{}, false
	}
	return *c, true
}

// List returns all clients, oldest first.
func (r *Registry) List() []Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Client, 0, len(r.clients))
	for _, c := range r.clients {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

var ErrNotFound = errors.New("客户端不存在")

// Update applies fn to a copy of client id (others are the remaining
// clients, for cross-client checks) and stores the result if fn succeeds.
func (r *Registry) Update(id string, fn func(c *Client, others []Client) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.clients[id]
	if !ok {
		return ErrNotFound
	}
	var others []Client
	for _, c := range r.clients {
		if c.ID != id {
			others = append(others, *c)
		}
	}
	next := *cur
	if err := fn(&next, others); err != nil {
		return err
	}
	r.clients[id] = &next
	if err := r.save(); err != nil {
		r.clients[id] = cur
		return err
	}
	r.notifyLocked()
	return nil
}

func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.clients, id)
	delete(r.byToken, c.TokenHash)
	delete(r.live, id)
	if err := r.save(); err != nil {
		r.clients[id], r.byToken[c.TokenHash] = c, id
		return err
	}
	r.notifyLocked()
	return nil
}

// Live returns what client id last reported (zero Live if never).
func (r *Registry) Live(id string) Live {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l := r.live[id]; l != nil {
		return *l
	}
	return Live{}
}

func (r *Registry) liveLocked(id string) *Live {
	l := r.live[id]
	if l == nil {
		l = &Live{}
		r.live[id] = l
	}
	return l
}

// SawConfigRequest records the host an agent uses to reach the panel.
func (r *Registry) SawConfigRequest(id, panelHost string) {
	r.mu.Lock()
	r.liveLocked(id).PanelHost = panelHost
	r.mu.Unlock()
}

// SetReport stores a status report and tells the agent when to report next.
func (r *Registry) SetReport(id, ip string, rep Report) ReportResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.liveLocked(id)
	l.LastSeen, l.RemoteIP = time.Now(), ip
	if rep.Logs != nil {
		l.Logs = rep.Logs
		rep.Logs = nil
	}
	l.Report = rep
	if c := r.clients[id]; c != nil && rep.Hostname != "" && rep.Hostname != c.Hostname {
		next := *c
		next.Hostname = rep.Hostname
		r.clients[id] = &next
		_ = r.save()
	}
	if time.Now().Before(l.logsWanted) {
		return ReportResponse{Interval: logsInterval, WantLogs: true}
	}
	return ReportResponse{Interval: reportInterval}
}

// WantLogs asks client id to include its logs in the next reports (for a
// while) and returns the most recent ones received.
func (r *Registry) WantLogs(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.liveLocked(id)
	l.logsWanted = time.Now().Add(20 * time.Second)
	return append([]string(nil), l.Logs...)
}

// RequestRestart queues a frpc restart for client id's agent.
func (r *Registry) RequestRestart(id string) {
	r.mu.Lock()
	r.liveLocked(id).restart = true
	r.notifyLocked()
	r.mu.Unlock()
}

// TakeRestart reports and clears a queued restart.
func (r *Registry) TakeRestart(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.live[id]
	if l == nil || !l.restart {
		return false
	}
	l.restart = false
	return true
}

// ---- rendering ----

// Common is the connection info the server puts into every client config.
type Common struct {
	ServerAddr string
	ServerPort int
	Token      string
	TLSForce   bool
}

// Render produces the complete frpc.toml for c.
func Render(c Client, cm Common) ([]byte, error) {
	d, err := frp.ParseDoc([]byte(c.Config))
	if err != nil {
		return nil, fmt.Errorf("客户端 %s 的配置无法解析：%w", c.Name, err)
	}
	frp.StripManaged(d)
	cc := frp.ReadClientCommon(d)
	cc.ServerAddr, cc.ServerPort, cc.Token, cc.User = cm.ServerAddr, cm.ServerPort, cm.Token, c.ID
	if cm.TLSForce {
		cc.TLSEnable = true
	}
	if err := frp.ApplyClientCommon(d, cc); err != nil {
		return nil, err
	}
	return d.Marshal()
}

// CheckPorts rejects tcp/udp remote ports in d that another client already
// uses; frps would otherwise refuse one of the two proxies at runtime.
func CheckPorts(d frp.Doc, others []Client) error {
	mine := frp.RemotePorts(d)
	if len(mine) == 0 {
		return nil
	}
	for _, o := range others {
		od, err := frp.ParseDoc([]byte(o.Config))
		if err != nil {
			continue
		}
		for k, theirs := range frp.RemotePorts(od) {
			if name, ok := mine[k]; ok {
				return fmt.Errorf("代理 %s 的远程端口 %s 已被客户端「%s」的代理 %s 占用", name, k, o.Name, theirs)
			}
		}
	}
	return nil
}
