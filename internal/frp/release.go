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

var (
	versionRe      = regexp.MustCompile(`\d+\.\d+\.\d+`)
	exactVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	platformRe     = regexp.MustCompile(`^[a-z]+_[a-z0-9_]+$`)
)

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

// WithMirror prefixes u with a GitHub download mirror, if one is set.
func WithMirror(mirror, u string) string {
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
	resp, err := c.Get(WithMirror(mirror, "https://github.com/"+repo+"/releases/latest"))
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

// Fetch GETs u into w, reading at most limit bytes.
func Fetch(u string, w io.Writer, limit int64) error {
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

// ChecksumFor returns the hex SHA-256 listed for file in a "sum  name" list.
func ChecksumFor(sums, file string) string {
	sc := bufio.NewScanner(strings.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == file {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// Archive returns the path of the FRP <version> release archive for platform
// (e.g. "linux_amd64") in cacheDir, downloading it first if needed. The
// download is checked against the release's published SHA-256 list.
func Archive(version, mirror, platform, cacheDir string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	if !exactVersionRe.MatchString(version) {
		return "", fmt.Errorf("版本号无效：%q", version)
	}
	if !platformRe.MatchString(platform) {
		return "", fmt.Errorf("平台无效：%q", platform)
	}
	file := fmt.Sprintf("frp_%s_%s.tar.gz", version, platform)
	dst := filepath.Join(cacheDir, file)
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s/", repo, version)
	var sums strings.Builder
	if err := Fetch(WithMirror(mirror, base+"frp_sha256_checksums.txt"), &sums, 1<<20); err != nil {
		return "", fmt.Errorf("下载校验文件失败：%w", err)
	}
	want := ChecksumFor(sums.String(), file)
	if want == "" {
		return "", fmt.Errorf("发布的校验文件中没有 %s（可能不支持此架构）", file)
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(cacheDir, ".dl-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	if err := Fetch(WithMirror(mirror, base+file), io.MultiWriter(tmp, h), 200<<20); err != nil {
		return "", fmt.Errorf("下载 %s 失败：%w", file, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("%s 的 SHA-256 不匹配（实际 %s，应为 %s），已放弃", file, got, want)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// ExtractBinary copies the executable called name out of an FRP archive to
// dst, replacing it atomically.
func ExtractBinary(archive, name, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("压缩包 %s 中没有 %s", filepath.Base(archive), name)
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != name {
			continue
		}
		out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-*")
		if err != nil {
			return err
		}
		defer os.Remove(out.Name())
		if _, err := io.Copy(out, io.LimitReader(tr, 200<<20)); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		if err := os.Chmod(out.Name(), 0o755); err != nil {
			return err
		}
		return os.Rename(out.Name(), dst)
	}
}

// Install downloads FRP <version> for this machine and atomically replaces
// the requested binaries in binDir.
func Install(version, mirror, binDir string, names ...string) error {
	tmp, err := os.MkdirTemp("", "frp-dl-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive, err := Archive(version, mirror, runtime.GOOS+"_"+ReleaseArch(), tmp)
	if err != nil {
		return err
	}
	staged := map[string]string{}
	defer func() {
		for _, p := range staged {
			os.Remove(p)
		}
	}()
	for _, n := range names {
		dst := filepath.Join(binDir, "."+n+".new")
		if err := ExtractBinary(archive, n, dst); err != nil {
			return err
		}
		staged[n] = dst
		if err := CheckRuns(dst); err != nil {
			return err
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

// CheckRuns makes sure a freshly downloaded binary executes ("-v").
func CheckRuns(bin string) error {
	if out, err := exec.Command(bin, "-v").CombinedOutput(); err != nil {
		return fmt.Errorf("下载的 %s 无法运行：%v %s", filepath.Base(bin), err, out)
	}
	return nil
}
