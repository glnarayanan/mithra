package installer

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DetectHost is read-only. It never installs packages, edits a firewall, or
// assumes ownership of another application's files.
func DetectHost(ctx context.Context, root, domain string, port int) HostFacts {
	if root == "" {
		root = "/"
	}
	facts := RuntimeFacts(root, port)
	facts.OS, facts.Arch = runtime.GOOS, runtime.GOARCH
	facts.Commands = map[string]string{}
	for _, command := range []string{"systemctl", "sqlite3", "caddy", "nginx", "apache2ctl", "httpd", "ss"} {
		if path, err := exec.LookPath(command); err == nil {
			facts.Commands[command] = path
		}
	}
	if root == "/" {
		facts.Systemd = facts.Commands["systemctl"] != ""
		facts.SQLite = facts.Commands["sqlite3"] != "" || facts.SQLite
		facts.Listeners = detectListeners(ctx, facts.Commands["ss"])
	}
	facts.VHosts, facts.DetectedProxy = detectVHosts(root)
	facts.CaddyImportsOwnedDir = caddyImportsOwnedDirectory(root)
	if domain != "" {
		domain = strings.ToLower(strings.TrimSpace(domain))
		_ = domain
	}
	for _, path := range []string{rooted(root, "/etc/arivu"), rooted(root, "/var/lib/arivu"), rooted(root, "/etc/systemd/system/arivu.service")} {
		if exists(path) {
			facts.ArivuPaths = append(facts.ArivuPaths, path)
		}
	}
	facts.ArivuBaseline = CaptureArivuBaseline(ctx, root)
	var stat syscall.Statfs_t
	if syscall.Statfs(root, &stat) == nil {
		facts.FreeBytes = stat.Bavail * uint64(stat.Bsize)
	}
	return facts
}

func caddyImportsOwnedDirectory(root string) bool {
	raw, err := os.ReadFile(rooted(root, "/etc/caddy/Caddyfile"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "import" {
			pattern := strings.TrimPrefix(fields[1], "/etc/caddy/")
			if pattern == "conf.d/*" || pattern == "conf.d/*.caddy" {
				return true
			}
		}
	}
	return false
}

func CaptureArivuBaseline(ctx context.Context, root string) ArivuBaseline {
	baseline := ArivuBaseline{Artifacts: map[string]string{}}
	candidates := []string{
		rooted(root, "/usr/local/bin/arivu"),
		rooted(root, "/etc/arivu"),
		rooted(root, "/etc/systemd/system/arivu.service"),
		rooted(root, "/etc/caddy"),
		rooted(root, "/etc/nginx"),
		rooted(root, "/etc/apache2"),
		rooted(root, "/etc/httpd"),
	}
	for _, candidate := range candidates {
		_ = filepath.WalkDir(candidate, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if strings.Contains(path, string(filepath.Separator)+"etc"+string(filepath.Separator)) && !strings.Contains(strings.ToLower(path), "arivu") {
				return nil
			}
			digest, err := digestRegularFile(path)
			if err == nil {
				baseline.Artifacts[path] = digest
			}
			return nil
		})
	}
	if len(baseline.Artifacts) == 0 {
		return baseline
	}
	if root == "/" {
		if output, err := exec.CommandContext(ctx, "id", "arivu").Output(); err == nil {
			baseline.Identity = strings.TrimSpace(string(output))
		}
		baseline.ServiceActive = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "arivu.service").Run() == nil
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:8080/api/health", nil)
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		if err == nil {
			baseline.HealthOK = response.StatusCode == http.StatusOK
			response.Body.Close()
		}
	}
	return baseline
}

func digestRegularFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, 256<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func VerifyArivuBaseline(ctx context.Context, root string, expected ArivuBaseline) error {
	if len(expected.Artifacts) == 0 {
		return nil
	}
	current := CaptureArivuBaseline(ctx, root)
	if len(current.Artifacts) != len(expected.Artifacts) {
		return errors.New("Arivu immutable artifact set changed")
	}
	for path, digest := range expected.Artifacts {
		if current.Artifacts[path] != digest {
			return fmt.Errorf("Arivu immutable artifact changed: %s", path)
		}
	}
	if root == "/" && (current.Identity != expected.Identity || current.ServiceActive != expected.ServiceActive || current.HealthOK != expected.HealthOK) {
		return errors.New("Arivu identity, service state, or health changed")
	}
	return nil
}

func detectListeners(ctx context.Context, ssPath string) map[int]string {
	result := map[int]string{}
	if ssPath == "" {
		return result
	}
	out, err := exec.CommandContext(ctx, ssPath, "-ltnp").Output()
	if err != nil {
		return result
	}
	for _, line := range strings.Split(string(out), "\n") {
		for _, field := range strings.Fields(line) {
			index := strings.LastIndex(field, ":")
			if index < 0 {
				continue
			}
			if port, err := strconv.Atoi(strings.Trim(field[index+1:], "*[]")); err == nil {
				result[port] = strings.TrimSpace(line)
			}
		}
	}
	return result
}

func detectVHosts(root string) (map[string]string, ProxyMode) {
	hosts := map[string]string{}
	mode := ProxyMode("")
	roots := []struct {
		path string
		mode ProxyMode
	}{{"/etc/caddy", Caddy}, {"/etc/nginx", Nginx}, {"/etc/apache2", Apache}, {"/etc/httpd", Apache}}
	for _, candidate := range roots {
		base := rooted(root, candidate.path)
		_ = filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if mode == "" {
				mode = candidate.mode
			}
			file, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 1024), 256<<10)
			for scanner.Scan() {
				line := strings.TrimSpace(strings.Split(scanner.Text(), "#")[0])
				var values []string
				switch {
				case strings.HasPrefix(line, "server_name "):
					values = strings.Fields(strings.TrimSuffix(strings.TrimPrefix(line, "server_name "), ";"))
				case strings.HasPrefix(strings.ToLower(line), "servername "):
					values = strings.Fields(line)[1:]
				case candidate.mode == Caddy && strings.Contains(line, "{"):
					values = strings.FieldsFunc(strings.SplitN(line, "{", 2)[0], func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
				}
				for _, value := range values {
					value = strings.ToLower(strings.TrimSpace(value))
					if value != "" && !strings.HasPrefix(value, ":") && !strings.ContainsAny(value, "/{}") {
						hosts[value] = path
					}
				}
			}
			return nil
		})
	}
	return hosts, mode
}

func probeLoopback(port int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	return listener.Close() == nil
}
