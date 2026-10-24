// Package frp reads and writes frps/frpc TOML configs, controls the FRP
// processes and talks to their local admin APIs.
//
// Configs are handled as generic maps so that keys the panel doesn't know
// about (added by hand or by a newer FRP) survive a save from the UI.
package frp

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Doc map[string]any

const header = "# Managed by FRP Panel. Manual edits are kept, but comments are not.\n# Docs: https://gofrp.org/en/docs/reference/\n\n"

func ParseDoc(b []byte) (Doc, error) {
	d := Doc{}
	if err := toml.Unmarshal(b, (*map[string]any)(&d)); err != nil {
		return nil, err
	}
	return d, nil
}

func LoadDoc(path string) (Doc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseDoc(b)
}

func (d Doc) Marshal() ([]byte, error) {
	b, err := toml.Marshal(normalize(map[string]any(d)))
	if err != nil {
		return nil, err
	}
	return append([]byte(header), b...), nil
}

// normalize turns []any whose elements are all tables into []map[string]any
// so the encoder writes [[array-of-tables]] instead of inline tables.
func normalize(v any) any {
	switch t := v.(type) {
	case Doc:
		return normalize(map[string]any(t))
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = normalize(x)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(t))
		for i, m := range t {
			out[i] = normalize(m).(map[string]any)
		}
		return out
	case []any:
		allMaps := len(t) > 0
		for _, x := range t {
			if _, ok := x.(map[string]any); !ok {
				allMaps = false
				break
			}
		}
		if allMaps {
			out := make([]map[string]any, len(t))
			for i, x := range t {
				out[i] = normalize(x).(map[string]any)
			}
			return out
		}
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = normalize(x)
		}
		return out
	}
	return v
}

func lookup(m map[string]any, key string) (any, bool) {
	parts := strings.Split(key, ".")
	cur := m
	for i, p := range parts {
		v, ok := cur[p]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return nil, false
}

func getStr(m map[string]any, key string) string {
	v, _ := lookup(m, key)
	s, _ := v.(string)
	return s
}

func getInt(m map[string]any, key string) int {
	v, _ := lookup(m, key)
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func getBool(m map[string]any, key string, def bool) bool {
	v, ok := lookup(m, key)
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func getStrs(m map[string]any, key string) []string {
	v, _ := lookup(m, key)
	arr, _ := v.([]any)
	out := []string{}
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	if ss, ok := v.([]string); ok {
		out = append(out, ss...)
	}
	return out
}

// set writes a value at a dotted key, creating intermediate tables.
func set(m map[string]any, key string, v any) {
	parts := strings.Split(key, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

// del removes a dotted key and prunes tables left empty.
func del(m map[string]any, key string) {
	parts := strings.Split(key, ".")
	var walk func(cur map[string]any, i int) bool
	walk = func(cur map[string]any, i int) bool {
		if i == len(parts)-1 {
			delete(cur, parts[i])
		} else if next, ok := cur[parts[i]].(map[string]any); ok {
			if walk(next, i+1) {
				delete(cur, parts[i])
			}
		}
		return len(cur) == 0
	}
	walk(m, 0)
}

// setOrDel stores v unless it is the zero value, in which case the key is
// removed so FRP's own default applies.
func setOrDel(m map[string]any, key string, v any) {
	switch t := v.(type) {
	case string:
		if t == "" {
			del(m, key)
			return
		}
	case int:
		if t == 0 {
			del(m, key)
			return
		}
		v = int64(t)
	case []string:
		if len(t) == 0 {
			del(m, key)
			return
		}
		arr := make([]any, len(t))
		for i, s := range t {
			arr[i] = s
		}
		v = arr
	case bool:
		if !t {
			del(m, key)
			return
		}
	}
	set(m, key, v)
}

// Verify runs `<bin> verify -c` on candidate content, in the config's own
// directory so relative paths resolve the same way they will at runtime.
func Verify(bin, configPath string, content []byte) error {
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("无法校验配置：找不到 %s", bin)
	}
	dir := filepath.Dir(configPath)
	tmp, err := os.CreateTemp(dir, ".verify-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	cmd := exec.Command(bin, "verify", "-c", tmp.Name())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return errors.New("配置校验超时")
	}
	if err != nil {
		msg := StripANSI(out.String())
		msg = strings.ReplaceAll(msg, tmp.Name(), filepath.Base(configPath))
		return fmt.Errorf("FRP 校验未通过：%s", strings.TrimSpace(msg))
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func StripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= '@' && s[j] <= '~') {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
