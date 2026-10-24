package frp

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

const repo = "fatedier/frp"

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

var httpClient = &http.Client{Timeout: 5 * time.Minute}

// InstalledVersion runs `<bin> -v`; empty if the binary is missing.
func InstalledVersion(bin string) string {
	out, err := run(5*time.Second, bin, "-v")
	if err != nil {
		return ""
	}
	return versionRe.FindString(out)
}

// ReleaseArch maps the running platform to FRP's release naming.
func ReleaseArch() string {
	arch := runtime.GOARCH
	if arch == "arm" {
		goarm := ""
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "GOARM" {
					goarm = s.Value
				}
			}
		}
		if strings.HasPrefix(goarm, "7") {
			return "arm_hf"
		}
	}
	return arch
}

func withMirror(mirror, u string) string {
	mirror = strings.TrimSpace(mirror)
	if mirror == "" {
		return u
	}
	return strings.TrimRight(mirror, "/") + "/" + u
}

// LatestVersion asks GitHub (or the mirror) for the newest FRP release.
func LatestVersion(mirror string) (string, error) {
	if mirror == "" {
		req, _ := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/latest", nil)
		req.Header.Set("Accept", "application/vnd.github+json")
		c := &http.Client{Timeout: 15 * time.Second}
		if resp, err := c.Do(req); err == nil {
			defer resp.Body.Close()
			var r struct {
				Tag string `json:"tag_name"`
			}
			if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&r) == nil {
				if v := versionRe.FindString(r.Tag); v != "" {
					return v, nil
				}
			}
		}
	}
	// Fallback: the /releases/latest page redirects to /releases/tag/vX.Y.Z.
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get(withMirror(mirror, "https://github.com/"+repo+"/releases/latest"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if m := regexp.MustCompile(`/releases/tag/v(\d+\.\d+\.\d+)`).FindStringSubmatch(resp.Request.URL.Path); m != nil {
		return m[1], nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if m := regexp.MustCompile(`/releases/tag/v(\d+\.\d+\.\d+)`).FindSubmatch(body); m != nil {
		return string(m[1]), nil
	}
	return "", errors.New("无法获取 FRP 最新版本号")
}

func fetch(u string, w io.Writer, limit int64) error {
	resp, err := httpClient.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	_, err = io.Copy(w, io.LimitReader(resp.Body, limit))
	return err
}

// Install downloads FRP <version>, checks it against the release's published
// SHA-256 list and atomically replaces the requested binaries in binDir.
func Install(version, mirror, binDir string, names ...string) error {
	version = strings.TrimPrefix(version, "v")
	if !versionRe.MatchString(version) {
		return fmt.Errorf("版本号无效：%q", version)
	}
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s/", repo, version)
	pkg := fmt.Sprintf("frp_%s_%s_%s", version, runtime.GOOS, ReleaseArch())
	file := pkg + ".tar.gz"

	var sums strings.Builder
	if err := fetch(withMirror(mirror, base+"frp_sha256_checksums.txt"), &sums, 1<<20); err != nil {
		return fmt.Errorf("下载校验文件失败：%w", err)
	}
	want := ""
	sc := bufio.NewScanner(strings.NewReader(sums.String()))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == file {
			want = strings.ToLower(f[0])
		}
	}
	if want == "" {
		return fmt.Errorf("发布的校验文件中没有 %s（可能不支持此架构）", file)
	}

	tmp, err := os.CreateTemp("", "frp-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if err := fetch(withMirror(mirror, base+file), io.MultiWriter(tmp, h), 200<<20); err != nil {
		return fmt.Errorf("下载 %s 失败：%w", file, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s 的 SHA-256 不匹配（实际 %s，应为 %s），已放弃安装", file, got, want)
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}

	gz, err := gzip.NewReader(tmp)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}
	staged := map[string]string{}
	defer func() {
		for _, p := range staged {
			os.Remove(p)
		}
	}()
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := path.Base(hdr.Name)
		if hdr.Typeflag != tar.TypeReg || !wanted[name] {
			continue
		}
		dst := filepath.Join(binDir, "."+name+".new")
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 200<<20)); err != nil {
			f.Close()
			return err
		}
		f.Close()
		staged[name] = dst
	}
	for _, n := range names {
		if staged[n] == "" {
			return fmt.Errorf("压缩包 %[2]s 中没有 %[1]s", n, file)
		}
		// Sanity check before swapping it in.
		if out, err := exec.Command(staged[n], "-v").CombinedOutput(); err != nil {
			return fmt.Errorf("下载的 %s 无法运行：%v %s", n, err, out)
		}
	}
	for _, n := range names {
		if err := os.Rename(staged[n], filepath.Join(binDir, n)); err != nil {
			return err
		}
		delete(staged, n)
	}
	return nil
}
