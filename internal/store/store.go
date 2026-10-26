// Package store persists the panel's own settings (not FRP's) as a JSON file.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	RoleServer = "server"
	// RoleClient was the standalone client panel of earlier versions; such
	// machines are now managed by the server through an agent.
	RoleClient = "client"

	TLSSelfSigned = "selfsigned"
	TLSCustom     = "custom"
	TLSACME       = "acme"
)

type TLSConfig struct {
	// Mode is one of selfsigned / custom / acme. Access by IP always falls
	// back to the self-signed certificate, so the panel is never plaintext.
	Mode      string   `json:"mode"`
	Domain    string   `json:"domain,omitempty"`
	Email     string   `json:"email,omitempty"`
	ExtraSANs []string `json:"extraSANs,omitempty"`
}

type Admin struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	MustChange   bool   `json:"mustChange"`
}

type Config struct {
	Role         string `json:"role"`
	Listen       string `json:"listen"`
	FRPDir       string `json:"frpDir"`
	BinDir       string `json:"binDir"`
	ProcMode     string `json:"procMode"` // systemd | exec
	PublicAddr   string `json:"publicAddr,omitempty"`
	GithubMirror string `json:"githubMirror,omitempty"`
	// EnrollKey lets machines join as managed clients (see the join command).
	EnrollKey string    `json:"enrollKey,omitempty"`
	TLS       TLSConfig `json:"tls"`
	Admin     Admin     `json:"admin"`
}

func (c Config) FRPConfigPath() string {
	if c.Role == RoleServer {
		return filepath.Join(c.FRPDir, "frps.toml")
	}
	return filepath.Join(c.FRPDir, "frpc.toml")
}

func (c Config) FRPBinary() string {
	if c.Role == RoleServer {
		return filepath.Join(c.BinDir, "frps")
	}
	return filepath.Join(c.BinDir, "frpc")
}

type Store struct {
	Dir  string
	mu   sync.RWMutex
	path string
	cfg  Config
}

func Path(dir string) string { return filepath.Join(dir, "panel.json") }

func Load(dir string) (*Store, error) {
	s := &Store{Dir: dir, path: Path(dir)}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("panel not initialized: run `frp-panel init` first (" + s.path + " missing)")
		}
		return nil, err
	}
	if err := json.Unmarshal(b, &s.cfg); err != nil {
		return nil, err
	}
	return s, nil
}

func Create(dir string, cfg Config) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{Dir: dir, path: Path(dir), cfg: cfg}
	return s, s.save()
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.TLS.ExtraSANs = append([]string(nil), s.cfg.TLS.ExtraSANs...)
	return c
}

func (s *Store) Update(fn func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
	return s.save()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(s.path, b, 0o600)
}

// WriteFileAtomic writes via a temp file in the same directory and renames it
// into place, keeping the owner of an existing file.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		chownLike(tmp.Name(), fi)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
