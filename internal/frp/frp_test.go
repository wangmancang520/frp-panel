package frp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secret() string { return "s3cret-s3cret" }

func TestServerDocRoundTrip(t *testing.T) {
	d := NewServerDoc(7000, "token-123456", secret)
	s := ReadServer(d)
	s.AllowPorts = "6000-7000, 8080"
	s.VhostHTTPPort = 80
	if err := ApplyServer(d, s); err != nil {
		t.Fatal(err)
	}
	b, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{"bindPort = 7000", "[[allowPorts]]", "addr = '127.0.0.1'", "force = true"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	d2, err := ParseDoc(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := ReadServer(d2); got.AllowPorts != "6000-7000,8080" || got.VhostHTTPPort != 80 || !got.TLSForce {
		t.Errorf("round trip mismatch: %+v", got)
	}
	verifyIfAvailable(t, "frps", b)
}

func TestClientProxiesKeepUnknownKeys(t *testing.T) {
	src := `serverAddr = "1.2.3.4"
serverPort = 7000
auth.token = "abcdefgh"

[[proxies]]
name = "ssh"
type = "tcp"
localPort = 22
remotePort = 6000
healthCheck.type = "tcp"
healthCheck.intervalSeconds = 10
`
	d, err := ParseDoc([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	EnforceLocalAdmin(d, 7400, secret)
	p := ReadProxy(Proxies(d)[0])
	p.RemotePort = 6022
	p.UseEncryption = true
	if err := UpsertProxy(d, "ssh", p); err != nil {
		t.Fatal(err)
	}
	if err := UpsertProxy(d, "", Proxy{Name: "web", Type: "http", LocalPort: 8080, CustomDomains: []string{"a.example.com"}, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := UpsertProxy(d, "", Proxy{Name: "web", Type: "tcp", LocalPort: 1}); err == nil {
		t.Error("duplicate name accepted")
	}
	b, _ := d.Marshal()
	out := string(b)
	for _, want := range []string{"remotePort = 6022", "intervalSeconds = 10", "useEncryption = true", "customDomains = ['a.example.com']"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "[[proxies]]") != 2 {
		t.Errorf("expected 2 proxies:\n%s", out)
	}
	verifyIfAvailable(t, "frpc", b)

	old, _ := ParseDoc(b)
	if err := DeleteProxy(d, "web"); err != nil {
		t.Fatal(err)
	}
	if CommonChanged(old, d) {
		t.Error("deleting a proxy should not count as a common change")
	}
	c := ReadClientCommon(d)
	c.ServerPort = 7001
	_ = ApplyClientCommon(d, c)
	if !CommonChanged(old, d) {
		t.Error("server port change not detected")
	}
}

func TestValidation(t *testing.T) {
	d := Doc{}
	cases := []Proxy{
		{Name: "bad name!", Type: "tcp", LocalPort: 22},
		{Name: "x", Type: "tcp", LocalPort: 0},
		{Name: "x", Type: "http", LocalPort: 80},
		{Name: "x", Type: "stcp", LocalPort: 22, SecretKey: "123"},
		{Name: "x", Type: "tcp", LocalPort: 22, BandwidthLimit: "10mb"},
		{Name: "x", Type: "socks", LocalPort: 22},
	}
	for _, p := range cases {
		if err := UpsertProxy(d, "", p); err == nil {
			t.Errorf("accepted invalid proxy %+v", p)
		}
	}
	if _, err := parsePortRanges("7000-6000"); err == nil {
		t.Error("accepted reversed range")
	}
}

// verifyIfAvailable runs the real FRP binary when FRP_BIN_DIR is set.
func verifyIfAvailable(t *testing.T, name string, content []byte) {
	dir := os.Getenv("FRP_BIN_DIR")
	if dir == "" {
		return
	}
	cfg := filepath.Join(t.TempDir(), name+".toml")
	if err := Verify(filepath.Join(dir, name), cfg, content); err != nil {
		t.Errorf("%s verify failed: %v\n%s", name, err, content)
	}
}
