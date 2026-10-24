package frp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Status struct {
	Running bool      `json:"running"`
	State   string    `json:"state"`
	PID     int       `json:"pid"`
	Since   time.Time `json:"since"`
}

// Manager starts/stops frps or frpc. Systemd is used on real installs; Exec
// supervises a child process directly (containers, dev machines).
type Manager interface {
	Start() error
	Stop() error
	Restart() error
	Status() Status
	Logs(n int) []string
}

// ---- systemd ----

type Systemd struct{ Unit string }

func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (s Systemd) ctl(action string) error {
	if out, err := run(30*time.Second, "systemctl", action, s.Unit); err != nil {
		return fmt.Errorf("systemctl %s %s: %v %s", action, s.Unit, err, out)
	}
	return nil
}

func (s Systemd) Start() error   { return s.ctl("start") }
func (s Systemd) Stop() error    { return s.ctl("stop") }
func (s Systemd) Restart() error { return s.ctl("restart") }

func (s Systemd) Status() Status {
	out, _ := run(5*time.Second, "systemctl", "show", s.Unit, "--no-pager",
		"-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "ActiveEnterTimestamp")
	kv := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[k] = v
		}
	}
	st := Status{State: kv["ActiveState"]}
	if kv["SubState"] != "" {
		st.State += "/" + kv["SubState"]
	}
	st.Running = kv["ActiveState"] == "active"
	st.PID, _ = strconv.Atoi(kv["MainPID"])
	if t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", kv["ActiveEnterTimestamp"]); err == nil && st.Running {
		st.Since = t
	}
	return st
}

func (s Systemd) Logs(n int) []string {
	out, err := run(10*time.Second, "journalctl", "-u", s.Unit, "-n", strconv.Itoa(n), "--no-pager", "-o", "cat")
	if err != nil && out == "" {
		return []string{"journalctl failed: " + err.Error()}
	}
	return strings.Split(StripANSI(out), "\n")
}

// ---- direct child process ----

type Exec struct {
	Bin  string
	Args []string

	mu      sync.Mutex
	cmd     *exec.Cmd
	want    bool
	since   time.Time
	logs    []string
	exited  chan struct{}
	lastErr string
}

const logCap = 2000

func (e *Exec) appendLog(line string) {
	e.mu.Lock()
	e.logs = append(e.logs, StripANSI(line))
	if len(e.logs) > logCap {
		e.logs = e.logs[len(e.logs)-logCap:]
	}
	e.mu.Unlock()
}

func (e *Exec) pipe(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		e.appendLog(sc.Text())
	}
}

// spawn starts the child; caller holds e.mu.
func (e *Exec) spawn() error {
	cmd := exec.Command(e.Bin, e.Args...)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		e.lastErr = err.Error()
		return err
	}
	e.cmd, e.since, e.exited = cmd, time.Now(), make(chan struct{})
	exited := e.exited
	go e.pipe(stdout)
	go e.pipe(stderr)
	go func() {
		err := cmd.Wait()
		e.mu.Lock()
		close(exited)
		if e.cmd == cmd {
			e.cmd = nil
		}
		want := e.want
		e.mu.Unlock()
		e.appendLog(fmt.Sprintf("%s [panel] process exited: %v", time.Now().Format("2006-01-02 15:04:05"), err))
		if want {
			time.Sleep(3 * time.Second)
			e.mu.Lock()
			if e.want && e.cmd == nil {
				e.appendLog(time.Now().Format("2006-01-02 15:04:05") + " [panel] restarting")
				_ = e.spawn()
			}
			e.mu.Unlock()
		}
	}()
	return nil
}

func (e *Exec) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.want = true
	if e.cmd != nil {
		return nil
	}
	return e.spawn()
}

func (e *Exec) Stop() error {
	e.mu.Lock()
	e.want = false
	cmd, exited := e.cmd, e.exited
	e.mu.Unlock()
	if cmd == nil {
		return nil
	}
	_ = cmd.Process.Signal(sigterm)
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
	return nil
}

func (e *Exec) Restart() error {
	if err := e.Stop(); err != nil {
		return err
	}
	return e.Start()
}

func (e *Exec) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cmd == nil {
		state := "inactive"
		if e.want {
			state = "activating/auto-restart"
		}
		return Status{State: state}
	}
	return Status{Running: true, State: "active/running", PID: e.cmd.Process.Pid, Since: e.since}
}

func (e *Exec) Logs(n int) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if n > len(e.logs) {
		n = len(e.logs)
	}
	return append([]string(nil), e.logs[len(e.logs)-n:]...)
}
