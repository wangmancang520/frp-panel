// Package tlsx provides the panel's HTTPS layer: a self-signed certificate
// that always works for IP access, optional custom / ACME certificates for a
// domain, and a listener that answers plaintext HTTP on the same port with a
// redirect to HTTPS (never with content).
package tlsx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

const (
	selfCert   = "selfsigned.crt"
	selfKey    = "selfsigned.key"
	customCert = "custom.crt"
	customKey  = "custom.key"
)

type Info struct {
	Source      string    `json:"source"` // selfsigned | custom | acme
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	DNSNames    []string  `json:"dnsNames"`
	IPs         []string  `json:"ips"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Fingerprint string    `json:"fingerprint"` // SHA-256 of DER, colon separated
}

// LocalSANs returns localhost, the hostname and every non-link-local
// interface address, plus any extras (e.g. the public IP behind NAT).
func LocalSANs(extra []string) (dns []string, ips []net.IP) {
	seenD := map[string]bool{}
	seenI := map[string]bool{}
	addD := func(s string) {
		s = strings.TrimSpace(strings.ToLower(s))
		if s != "" && !seenD[s] {
			seenD[s] = true
			dns = append(dns, s)
		}
	}
	addI := func(ip net.IP) {
		if ip == nil || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return
		}
		if k := ip.String(); !seenI[k] {
			seenI[k] = true
			ips = append(ips, ip)
		}
	}
	addD("localhost")
	if h, err := os.Hostname(); err == nil {
		addD(h)
	}
	addI(net.IPv4(127, 0, 0, 1))
	addI(net.IPv6loopback)
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				addI(n.IP)
			}
		}
	}
	for _, e := range extra {
		e = strings.TrimSpace(e)
		if ip := net.ParseIP(e); ip != nil {
			addI(ip)
		} else {
			addD(e)
		}
	}
	return dns, ips
}

// GenerateSelfSigned writes a fresh ECDSA P-256 certificate to dir.
func GenerateSelfSigned(dir string, extra []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	dns, ips := LocalSANs(extra)
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "FRP Panel", Organization: []string{"FRP Panel self-signed"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dns,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, selfKey), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, selfCert), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

func infoOf(c *tls.Certificate, source string) Info {
	if c == nil || len(c.Certificate) == 0 {
		return Info{Source: source}
	}
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return Info{Source: source}
	}
	ips := make([]string, 0, len(x.IPAddresses))
	for _, ip := range x.IPAddresses {
		ips = append(ips, ip.String())
	}
	sort.Strings(ips)
	return Info{
		Source: source, Subject: x.Subject.String(), Issuer: x.Issuer.String(),
		DNSNames: x.DNSNames, IPs: ips, NotBefore: x.NotBefore, NotAfter: x.NotAfter,
		Fingerprint: Fingerprint(x.Raw),
	}
}

// ValidateKeyPair checks that a PEM cert/key pair is usable.
func ValidateKeyPair(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil, err
	}
	if time.Now().After(x.NotAfter) {
		return nil, errors.New("证书已过期")
	}
	c.Leaf = x
	return &c, nil
}

// Manager selects which certificate to present for each handshake.
type Manager struct {
	dir string

	mu      sync.RWMutex
	mode    string
	domain  string
	self    *tls.Certificate
	custom  *tls.Certificate
	acme    *autocert.Manager
	acmeErr string
	// acmeHTTP serves HTTP-01 challenges while in ACME mode.
	acmeHTTP http.Handler
}

func NewManager(dir string) *Manager { return &Manager{dir: dir} }

// Configure (re)loads certificates for the given mode. The self-signed
// certificate is always loaded (and created if missing) as a fallback.
func (m *Manager) Configure(mode, domain, email string, extraSANs []string) error {
	certPath := filepath.Join(m.dir, selfCert)
	if _, err := os.Stat(certPath); errors.Is(err, os.ErrNotExist) {
		if err := GenerateSelfSigned(m.dir, extraSANs); err != nil {
			return fmt.Errorf("generate self-signed cert: %w", err)
		}
	}
	self, err := tls.LoadX509KeyPair(certPath, filepath.Join(m.dir, selfKey))
	if err != nil {
		return fmt.Errorf("load self-signed cert: %w", err)
	}
	var custom *tls.Certificate
	if mode == "custom" {
		c, err := tls.LoadX509KeyPair(filepath.Join(m.dir, customCert), filepath.Join(m.dir, customKey))
		if err != nil {
			return fmt.Errorf("加载上传的证书失败：%w", err)
		}
		custom = &c
	}
	var am *autocert.Manager
	if mode == "acme" {
		if domain == "" {
			return errors.New("自动申请证书需要填写域名")
		}
		am = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(domain),
			Cache:      autocert.DirCache(filepath.Join(m.dir, "acme")),
			Email:      email,
		}
	}
	m.mu.Lock()
	// autocert only offers the HTTP-01 challenge once HTTPHandler has been
	// called, so build it now rather than on the first port-80 request.
	var ah http.Handler
	if am != nil {
		ah = am.HTTPHandler(nil)
	}
	m.mode, m.domain, m.self, m.custom, m.acme, m.acmeHTTP, m.acmeErr = mode, strings.ToLower(domain), &self, custom, am, ah, ""
	m.mu.Unlock()
	return nil
}

// RegenerateSelfSigned replaces the self-signed cert and reloads it.
func (m *Manager) RegenerateSelfSigned(extra []string) error {
	if err := GenerateSelfSigned(m.dir, extra); err != nil {
		return err
	}
	self, err := tls.LoadX509KeyPair(filepath.Join(m.dir, selfCert), filepath.Join(m.dir, selfKey))
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.self = &self
	m.mu.Unlock()
	return nil
}

// SaveCustom stores an uploaded certificate (after validation).
func (m *Manager) SaveCustom(certPEM, keyPEM []byte) error {
	if _, err := ValidateKeyPair(certPEM, keyPEM); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.dir, customKey), keyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.dir, customCert), certPEM, 0o644)
}

func (m *Manager) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.RLock()
	mode, domain, self, custom, am := m.mode, m.domain, m.self, m.custom, m.acme
	m.mu.RUnlock()
	name := strings.ToLower(hello.ServerName)
	switch {
	case mode == "custom" && custom != nil && name != "":
		return custom, nil
	case mode == "acme" && am != nil && name == domain:
		c, err := am.GetCertificate(hello)
		if err == nil {
			return c, nil
		}
		m.mu.Lock()
		m.acmeErr = err.Error()
		m.mu.Unlock()
	}
	// Access by bare IP (no SNI) or anything we can't serve: self-signed.
	return self, nil
}

// TLSConfig returns a server config bound to this manager.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: m.getCertificate,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
	}
}

// ACMEHTTPHandler serves HTTP-01 challenges (nil unless in ACME mode).
func (m *Manager) ACMEHTTPHandler() http.Handler {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.acmeHTTP
}

func (m *Manager) SetACMEError(e string) {
	m.mu.Lock()
	m.acmeErr = e
	m.mu.Unlock()
}

// Status describes the certificates currently in use.
func (m *Manager) Status() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]any{
		"mode":       m.mode,
		"domain":     m.domain,
		"selfSigned": infoOf(m.self, "selfsigned"),
	}
	if m.custom != nil {
		out["custom"] = infoOf(m.custom, "custom")
	}
	if m.acme != nil {
		if b, err := os.ReadFile(filepath.Join(m.dir, "acme", m.domain)); err == nil {
			if blk, _ := pem.Decode(skipKey(b)); blk != nil {
				out["acme"] = infoOf(&tls.Certificate{Certificate: [][]byte{blk.Bytes}}, "acme")
			}
		}
		out["acmeError"] = m.acmeErr
	}
	return out
}

// SelfSignedFingerprint returns the fingerprint shown to users so they can
// verify the browser warning is about *their* certificate.
func SelfSignedFingerprint(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, selfCert))
	if err != nil {
		return "", err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return "", errors.New("invalid certificate file")
	}
	return Fingerprint(blk.Bytes), nil
}

// autocert caches "key PEM + cert PEM" in one file; skip to the first cert.
func skipKey(b []byte) []byte {
	for {
		blk, rest := pem.Decode(b)
		if blk == nil {
			return nil
		}
		if blk.Type == "CERTIFICATE" {
			return b
		}
		b = rest
	}
}
