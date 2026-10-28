package api

import (
	"path/filepath"
	"strings"
	"testing"

	"frp-panel/internal/frp"
	"frp-panel/internal/store"
)

func testApp(t *testing.T, mode, listen string) *App {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Create(dir, store.Config{Role: store.RoleServer, Listen: listen, FRPDir: dir, BinDir: dir, TLS: store.TLSConfig{Mode: mode}})
	if err != nil {
		t.Fatal(err)
	}
	return &App{St: st}
}

func serverDoc(t *testing.T, http, https int) frp.Doc {
	t.Helper()
	d := frp.NewServerDoc(7000, "token-123456", func() string { return "secret-secret" })
	s := frp.ReadServer(d)
	s.VhostHTTPPort, s.VhostHTTPSPort = http, https
	if err := frp.ApplyServer(d, s); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFrpsPortConflict(t *testing.T) {
	cases := []struct {
		name, mode, listen string
		http, https        int
		want               string // substring of the error, "" for none
	}{
		{"vhost 80 while panel uses ACME", store.TLSACME, ":7600", 80, 0, "vhostHTTPPort"},
		{"vhost 80 with self-signed cert", store.TLSSelfSigned, ":7600", 80, 0, ""},
		{"vhost 80 with uploaded cert", store.TLSCustom, ":7600", 80, 0, ""},
		{"vhost 80 with ACME on :443 (TLS-ALPN)", store.TLSACME, ":443", 80, 0, ""},
		{"vhost 8080 with ACME", store.TLSACME, ":7600", 8080, 0, ""},
		{"vhost https on the panel port", store.TLSSelfSigned, ":7600", 0, 7600, "面板自身"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := testApp(t, c.mode, c.listen).frpsPortConflict(serverDoc(t, c.http, c.https))
			switch {
			case c.want == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("error = %v, want it to mention %q", err, c.want)
			}
		})
	}
}

func TestFrpsOn80(t *testing.T) {
	a := testApp(t, store.TLSSelfSigned, ":7600")
	write := func(d frp.Doc) {
		b, _ := d.Marshal()
		if err := store.WriteFileAtomic(filepath.Join(a.St.Get().FRPDir, "frps.toml"), b, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write(serverDoc(t, 8080, 0))
	if got := a.frpsOn80(); got != "" {
		t.Errorf("frpsOn80() = %q with vhost 8080", got)
	}
	write(serverDoc(t, 80, 0))
	if got := a.frpsOn80(); !strings.Contains(got, "vhostHTTPPort") {
		t.Errorf("frpsOn80() = %q, want the vhostHTTPPort setting", got)
	}
}
