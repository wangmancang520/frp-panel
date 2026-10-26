package fleet

import (
	"path/filepath"
	"strings"
	"testing"

	"frp-panel/internal/frp"
)

func gen(s string) func() string { return func() string { return s } }

func enrollWithProxy(t *testing.T, r *Registry, id string, p frp.Proxy) Client {
	t.Helper()
	c, _, err := r.Enroll("host-"+id, "amd64", gen(id), gen("tok-"+id), gen("secret-"+id))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Update(c.ID, func(c *Client, others []Client) error {
		d, err := frp.ParseDoc([]byte(c.Config))
		if err != nil {
			return err
		}
		if err := frp.UpsertProxy(d, "", p); err != nil {
			return err
		}
		if err := CheckPorts(d, others); err != nil {
			return err
		}
		b, err := d.Marshal()
		c.Config = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ = r.Get(c.ID)
	return c
}

func TestRenderFillsConnection(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := enrollWithProxy(t, r, "aaa", frp.Proxy{Name: "ssh", Type: "tcp", LocalPort: 22, RemotePort: 6022})
	cm := Common{ServerAddr: "1.2.3.4", ServerPort: 7000, Token: "token-123456", TLSForce: true}
	b, err := Render(c, cm)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{"serverAddr = '1.2.3.4'", "serverPort = 7000", "token = 'token-123456'", "user = 'caaa'", "remotePort = 6022", "addr = '127.0.0.1'"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// Rendering is deterministic, so the hash only changes with the inputs.
	b2, _ := Render(c, cm)
	cm.Token = "other-token-1"
	b3, _ := Render(c, cm)
	if Hash(b) != Hash(b2) || Hash(b) == Hash(b3) {
		t.Error("hash should follow the rendered content")
	}
}

func TestCheckPortsAcrossClients(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	enrollWithProxy(t, r, "aaa", frp.Proxy{Name: "ssh", Type: "tcp", LocalPort: 22, RemotePort: 6022})
	b, _, err := r.Enroll("b", "amd64", gen("bbb"), gen("tok-b"), gen("secret-b"))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Update(b.ID, func(c *Client, others []Client) error {
		d, _ := frp.ParseDoc([]byte(c.Config))
		if err := frp.UpsertProxy(d, "", frp.Proxy{Name: "ssh", Type: "tcp", LocalPort: 22, RemotePort: 6022}); err != nil {
			return err
		}
		return CheckPorts(d, others)
	})
	if err == nil || !strings.Contains(err.Error(), "6022") {
		t.Fatalf("expected a port conflict, got %v", err)
	}
	// udp on the same number is a different port.
	err = r.Update(b.ID, func(c *Client, others []Client) error {
		d, _ := frp.ParseDoc([]byte(c.Config))
		if err := frp.UpsertProxy(d, "", frp.Proxy{Name: "dns", Type: "udp", LocalPort: 53, RemotePort: 6022}); err != nil {
			return err
		}
		return CheckPorts(d, others)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistryPersistsAndAuthenticates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, tok, err := r.Enroll("box", "arm64", gen("xyz"), gen("the-token"), gen("s"))
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r2.Authenticate(tok); !ok || got.ID != c.ID || got.Name != "box" {
		t.Fatalf("authenticate after reload: %+v %v", got, ok)
	}
	if _, ok := r2.Authenticate("wrong"); ok {
		t.Fatal("wrong token accepted")
	}
	if strings.Contains(c.TokenHash, tok) {
		t.Fatal("token stored in clear")
	}
	ch := r2.Changed()
	if err := r2.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("delete should wake waiting agents")
	}
	if _, ok := r2.Authenticate(tok); ok {
		t.Fatal("deleted client still authenticates")
	}
}

func TestStripManagedKeepsOptions(t *testing.T) {
	d, err := frp.ParseDoc([]byte(`serverAddr = "evil"
serverPort = 1
user = "x"
auth.token = "t"
transport.protocol = "kcp"
`))
	if err != nil {
		t.Fatal(err)
	}
	frp.StripManaged(d)
	b, _ := Render(Client{ID: "cid", Config: string(mustMarshal(t, d))}, Common{ServerAddr: "1.2.3.4", ServerPort: 7000, Token: "good-token"})
	out := string(b)
	if strings.Contains(out, "evil") || !strings.Contains(out, "protocol = 'kcp'") || !strings.Contains(out, "user = 'cid'") {
		t.Errorf("unexpected render:\n%s", out)
	}
}

func mustMarshal(t *testing.T, d frp.Doc) []byte {
	t.Helper()
	b, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
