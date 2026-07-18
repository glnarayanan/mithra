// Package installer owns Mithra's bounded shared-VPS lifecycle.
package installer

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type Operation string

const (
	Install     Operation = "install"
	Upgrade     Operation = "upgrade"
	Reconfigure Operation = "reconfigure"
	Backup      Operation = "backup"
	Restore     Operation = "restore"
	Status      Operation = "status"
	Uninstall   Operation = "uninstall"
	Purge       Operation = "purge"
)

type ProxyMode string

const (
	AppOnly ProxyMode = "app-only"
	Caddy   ProxyMode = "caddy"
	Nginx   ProxyMode = "nginx"
	Apache  ProxyMode = "apache"
)

type Options struct {
	Operation     Operation
	Root          string
	Domain        string
	Proxy         ProxyMode
	Port          int
	AllowedEmails []string
	ResendFrom    string
	Archive       string
	ConfirmPurge  bool
}

type HostFacts struct {
	OS, Arch                    string
	Systemd, SQLite             bool
	Commands                    map[string]string
	Listeners                   map[int]string
	VHosts                      map[string]string
	DetectedProxy               ProxyMode
	AppPortAvailable            bool
	MithraInstalled, DBExists   bool
	KeyExists, BackupExists     bool
	MigrationClean, SQLiteClean bool
	ArchiveValid, JournalValid  bool
	KeyMatches, AllowlistValid  bool
	FreeBytes                   uint64
	ArivuPaths                  []string
	ArivuBaseline               ArivuBaseline
	CaddyImportsOwnedDir        bool
}

type ArivuBaseline struct {
	Artifacts     map[string]string
	Identity      string
	ServiceActive bool
	HealthOK      bool
}

type Plan struct {
	Options       Options
	Proxy         ProxyMode
	Listener      string
	Actions       []string
	Mutations     []string
	Preserved     []string
	PurgeTarget   []string
	ArivuBaseline ArivuBaseline
}

type Paths struct {
	Root, Binary, Installer, Version, Config, Credentials, MasterKey, ResendKey string
	Data, Database, Sources, Journal, Backups                                   string
	Service, BackupService, BackupTimer, Socket, Proxy                          string
	OwnedManifest                                                               string
}

func OwnedPaths(root string, proxy ProxyMode) Paths {
	if root == "" {
		root = "/"
	}
	join := func(path string) string {
		if root == "/" {
			return path
		}
		return filepath.Join(root, strings.TrimPrefix(path, "/"))
	}
	proxyPath := ""
	switch proxy {
	case Caddy:
		proxyPath = join("/etc/caddy/conf.d/mithra.caddy")
	case Nginx:
		proxyPath = join("/etc/nginx/conf.d/mithra.conf")
	case Apache:
		proxyPath = join("/etc/apache2/conf-enabled/mithra.conf")
	}
	return Paths{
		Root: root, Binary: join("/usr/local/bin/mithra"), Installer: join("/usr/local/bin/mithra-installer"), Version: join("/etc/mithra/version"),
		Config: join("/etc/mithra/mithra.env"), Credentials: join("/etc/mithra/credentials"), MasterKey: join("/etc/mithra/credentials/master.key"), ResendKey: join("/etc/mithra/credentials/resend.key"),
		Data: join("/var/lib/mithra"), Database: join("/var/lib/mithra/mithra.sqlite3"), Sources: join("/var/lib/mithra/sources"), Journal: join("/var/lib/mithra/deletion.journal"), Backups: join("/var/backups/mithra"),
		Service: join("/etc/systemd/system/mithra.service"), BackupService: join("/etc/systemd/system/mithra-backup.service"), BackupTimer: join("/etc/systemd/system/mithra-backup.timer"), Socket: join("/run/mithra/mithra.sock"), Proxy: proxyPath, OwnedManifest: join("/etc/mithra/owned-files.json"),
	}
}

func BuildPlan(opts Options, facts HostFacts) (Plan, error) {
	if opts.Root == "" {
		opts.Root = "/"
	}
	if opts.Port == 0 {
		opts.Port = 8090
	}
	if opts.Port < 1024 || opts.Port > 65535 {
		return Plan{}, errors.New("port must be between 1024 and 65535")
	}
	if facts.OS != "linux" || (facts.Arch != "amd64" && facts.Arch != "arm64") || !facts.Systemd || !facts.SQLite {
		return Plan{}, errors.New("Mithra requires Linux amd64/arm64, systemd, and SQLite support; the installer does not prepare the server")
	}
	if opts.Operation == "" {
		opts.Operation = Install
	}
	if !validOperation(opts.Operation) {
		return Plan{}, fmt.Errorf("unsupported operation %q", opts.Operation)
	}
	proxy := opts.Proxy
	if proxy == "" {
		proxy = facts.DetectedProxy
		if proxy == "" {
			proxy = AppOnly
		}
	}
	if proxy != AppOnly && proxy != Caddy && proxy != Nginx && proxy != Apache {
		return Plan{}, fmt.Errorf("unsupported proxy mode %q", proxy)
	}
	if proxy != AppOnly {
		domain := strings.ToLower(strings.TrimSpace(opts.Domain))
		if domain == "" || strings.ContainsAny(domain, " /\\") {
			return Plan{}, errors.New("a valid domain is required for proxied mode")
		}
		opts.Domain = domain
		if owner := facts.VHosts[domain]; owner != "" && !strings.Contains(strings.ToLower(owner), "mithra") {
			return Plan{}, fmt.Errorf("domain %s is already owned by %s", domain, owner)
		}
		required := map[ProxyMode]string{Caddy: "caddy", Nginx: "nginx", Apache: "apache2ctl"}[proxy]
		if facts.Commands[required] == "" {
			return Plan{}, fmt.Errorf("missing prerequisite %s; the installer does not install server packages", required)
		}
		if proxy == Caddy && !facts.CaddyImportsOwnedDir {
			return Plan{}, errors.New("Caddyfile must already import /etc/caddy/conf.d/*; the installer does not mutate global proxy configuration")
		}
	}
	if opts.Operation == Install {
		if facts.MithraInstalled || facts.DBExists || facts.KeyExists || facts.BackupExists {
			return Plan{}, errors.New("Mithra runtime or recovery state already exists; use upgrade, reconfigure, or an explicit recovery path")
		}
		if facts.FreeBytes > 0 && facts.FreeBytes < 256<<20 {
			return Plan{}, errors.New("at least 256 MiB of free storage is required")
		}
		if proxy == AppOnly && !facts.AppPortAvailable {
			return Plan{}, fmt.Errorf("127.0.0.1:%d is already occupied", opts.Port)
		}
	}
	if opts.Operation == Install || opts.Operation == Reconfigure {
		if len(opts.AllowedEmails) == 0 {
			return Plan{}, errors.New("at least one allowlisted email is required")
		}
		for _, email := range opts.AllowedEmails {
			parsed, err := mail.ParseAddress(strings.TrimSpace(email))
			if err != nil || !strings.EqualFold(parsed.Address, strings.TrimSpace(email)) {
				return Plan{}, fmt.Errorf("invalid allowlisted email %q", email)
			}
		}
		parsed, err := mail.ParseAddress(strings.TrimSpace(opts.ResendFrom))
		if err != nil || parsed.Address == "" {
			return Plan{}, errors.New("a valid Resend sender identity is required")
		}
		opts.ResendFrom = parsed.String()
	}
	if opts.Operation == Upgrade || opts.Operation == Reconfigure {
		if !facts.MithraInstalled || !facts.DBExists || !facts.KeyExists || !facts.BackupExists || !facts.MigrationClean || !facts.SQLiteClean || !facts.KeyMatches {
			return Plan{}, errors.New("upgrade/reconfigure requires a recognized installation, clean migrations and SQLite, the retained key, and a verified backup")
		}
	}
	if opts.Operation == Restore {
		if opts.Archive == "" || !facts.ArchiveValid || !facts.JournalValid || !facts.KeyExists || !facts.KeyMatches || !facts.AllowlistValid {
			return Plan{}, errors.New("restore requires a verified archive, matching retained key, valid deletion journal, and current allowlist")
		}
	}
	paths := OwnedPaths(opts.Root, proxy)
	plan := Plan{Options: opts, Proxy: proxy, Listener: "127.0.0.1:" + strconv.Itoa(opts.Port), ArivuBaseline: facts.ArivuBaseline}
	if proxy != AppOnly {
		plan.Listener = paths.Socket
	}
	plan.Preserved = []string{paths.Data, paths.Backups, paths.Journal, paths.MasterKey}
	plan.PurgeTarget = []string{paths.Data, paths.Backups, paths.MasterKey}
	switch opts.Operation {
	case Install:
		plan.Actions = []string{"verify signed release", "stage immutable binaries and configuration", "create retained master key", "rehearse migrations", "activate service and proxy", "verify health", "create first backup"}
		plan.Mutations = append(ownedRuntime(paths), paths.MasterKey)
	case Upgrade:
		plan.Actions = []string{"verify signed release", "create pre-mutation backup", "rehearse migrations", "drain service", "capture final generation", "activate atomically", "verify health or roll back"}
		plan.Mutations = []string{paths.Binary, paths.Installer, paths.Version, paths.Service, paths.OwnedManifest}
	case Reconfigure:
		plan.Actions = []string{"verify current recovery evidence", "create pre-mutation backup", "stage configuration and Resend credential", "validate proxy", "activate atomically or roll back"}
		plan.Mutations = []string{paths.Config, paths.ResendKey, paths.Service, paths.Proxy, paths.OwnedManifest}
	case Backup:
		plan.Actions = []string{"drain mutations", "snapshot one database/source/journal generation", "authenticate manifest", "rotate successful archives"}
		plan.Mutations = []string{paths.Backups}
	case Restore:
		plan.Actions = []string{"verify and extract into staging", "reconcile deletion journal", "clear access and provider state", "apply current allowlist", "activate atomically", "verify health or roll back"}
		plan.Mutations = []string{paths.Data}
	case Uninstall:
		plan.Actions = []string{"stop service", "remove owned runtime and Resend credential", "preserve recovery state"}
		plan.Mutations = ownedRuntime(paths)
	case Purge:
		if !opts.ConfirmPurge {
			return Plan{}, fmt.Errorf("purge requires explicit confirmation of: %s", strings.Join(plan.PurgeTarget, ", "))
		}
		if facts.MithraInstalled {
			return Plan{}, errors.New("uninstall Mithra before purging retained recovery data")
		}
		plan.Actions = []string{"remove only confirmed Mithra recovery targets"}
		plan.Mutations = append([]string(nil), plan.PurgeTarget...)
	case Status:
		plan.Actions = []string{"report service, version, listener, backup timer, credential presence, and preserved-data state"}
	}
	for _, path := range plan.Mutations {
		if hasArivuSegment(path) || !isOwnedPath(path, paths) {
			return Plan{}, fmt.Errorf("refusing unowned mutation path %s", path)
		}
	}
	sort.Strings(plan.Mutations)
	return plan, nil
}

func ProbeLoopback(port int) bool {
	return probeLoopback(port)
}

func validOperation(op Operation) bool {
	for _, candidate := range []Operation{Install, Upgrade, Reconfigure, Backup, Restore, Status, Uninstall, Purge} {
		if op == candidate {
			return true
		}
	}
	return false
}

func ownedRuntime(p Paths) []string {
	return []string{p.Binary, p.Installer, p.Version, p.Config, p.ResendKey, p.Service, p.BackupService, p.BackupTimer, p.Proxy, p.OwnedManifest}
}

func isOwnedPath(path string, p Paths) bool {
	if path == "" {
		return true
	}
	for _, owned := range append(ownedRuntime(p), p.Data, p.Backups, p.Journal, p.MasterKey) {
		if path == owned {
			return true
		}
	}
	return false
}

func hasArivuSegment(path string) bool {
	for _, part := range strings.FieldsFunc(filepath.Clean(path), func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.EqualFold(part, "arivu") || strings.EqualFold(part, "arivu.service") {
			return true
		}
	}
	return false
}

func RuntimeFacts(root string, port int) HostFacts {
	paths := OwnedPaths(root, AppOnly)
	facts := HostFacts{OS: runtime.GOOS, Arch: runtime.GOARCH, Commands: map[string]string{}, Listeners: map[int]string{}, VHosts: map[string]string{}, AppPortAvailable: ProbeLoopback(port), MigrationClean: true, SQLiteClean: true, AllowlistValid: true}
	facts.Systemd = exists(rooted(root, "/run/systemd/system")) || root != "/"
	facts.SQLite = true
	facts.MithraInstalled, facts.DBExists, facts.KeyExists = exists(paths.Binary), exists(paths.Database), exists(paths.MasterKey)
	facts.BackupExists = directoryHas(paths.Backups, ".mbackup")
	facts.KeyMatches = facts.KeyExists
	return facts
}

func rooted(root, path string) string {
	if root == "" || root == "/" {
		return path
	}
	return filepath.Join(root, strings.TrimPrefix(path, "/"))
}
func exists(path string) bool { _, err := os.Lstat(path); return err == nil }
func directoryHas(path, suffix string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			return true
		}
	}
	return false
}
