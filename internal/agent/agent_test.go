package agent

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frp-panel/internal/frp"
)

func TestPickAdminPortAvoidsSiblingsAndLegacy(t *testing.T) {
	servers := t.TempDir()
	a := filepath.Join(servers, "a")
	if err := (Config{Panel: "https://a", Pin: "sha256//x", Token: "t", AdminPort: 7401}).save(a); err != nil {
		t.Fatal(err)
	}
	// A port in use by something else is skipped too.
	ln, err := net.Listen("tcp", "127.0.0.1:7402")
	if err == nil {
		defer ln.Close()
	}
	got, err := pickAdminPort(filepath.Join(servers, "b"))
	if err != nil {
		t.Fatal(err)
	}
	if got == 7400 || got == 7401 || (err == nil && ln != nil && got == 7402) {
		t.Errorf("pickAdminPort = %d, want a port other than 7400 (legacy), 7401 (sibling) and 7402 (busy)", got)
	}
	// Its own previous port does not count as taken.
	if p, _ := pickAdminPort(a); p == 7400 {
		t.Errorf("pickAdminPort(a) = 7400")
	}
}

func TestLocalizeOverridesOnlyTheAdminPort(t *testing.T) {
	d := frp.NewManagedDoc(func() string { return "secret-secret" })
	server, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: Config{AdminPort: 7403}}
	out, ld, err := a.localize(server)
	if err != nil {
		t.Fatal(err)
	}
	if got := frp.AdminFromDoc(ld).Base; got != "http://127.0.0.1:7403" {
		t.Errorf("admin base = %s", got)
	}
	if !strings.Contains(string(out), "port = 7403") || !strings.Contains(string(out), "password = 'secret-secret'") {
		t.Errorf("unexpected localized config:\n%s", out)
	}
	// Without an assigned port (migrated single-server agents) nothing changes.
	out2, _, _ := (&Agent{}).localize(server)
	if strings.Contains(string(out2), "7403") || !strings.Contains(string(out2), "port = 7400") {
		t.Errorf("config changed without an admin port:\n%s", out2)
	}
}

func TestClientScriptRefresh(t *testing.T) {
	if ScriptVersion([]byte("#!/bin/bash\nFRP_CLIENT_SCRIPT_VERSION=3\nset -e\n")) != 3 || ScriptVersion([]byte("echo hi")) != 0 {
		t.Fatal("ScriptVersion")
	}
	path := filepath.Join(t.TempDir(), "frp-client")
	newer := []byte("FRP_CLIENT_SCRIPT_VERSION=2\n")

	installClientScript(Options{ClientScript: newer, ClientScriptPath: path})
	if _, err := os.Stat(path); err == nil {
		t.Error("installed a script that was not there before")
	}
	os.WriteFile(path, []byte("FRP_CLIENT_SCRIPT_VERSION=1\n"), 0o755)
	installClientScript(Options{ClientScript: newer, ClientScriptPath: path})
	if b, _ := os.ReadFile(path); ScriptVersion(b) != 2 {
		t.Errorf("older script not replaced: %s", b)
	}
	installClientScript(Options{ClientScript: []byte("FRP_CLIENT_SCRIPT_VERSION=1\n"), ClientScriptPath: path})
	if b, _ := os.ReadFile(path); ScriptVersion(b) != 2 {
		t.Errorf("newer script downgraded: %s", b)
	}
}
