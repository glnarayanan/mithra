package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/internal/demo"
	"github.com/glnarayanan/mithra/internal/installer"
)

// Set by the release build with -X main.publisherKeyBase64=... . An unset key
// deliberately makes installation impossible rather than weakening trust.
var publisherKeyBase64 string

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mithra-installer:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("operation required: plan, install, upgrade, reconfigure, backup, restore, status, reset-demo, uninstall, or purge")
	}
	operation := installer.Operation(args[0])
	if operation == "plan" {
		operation = installer.Install
	}
	flags := flag.NewFlagSet("mithra-installer", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	root := flags.String("root", "/", "sandbox root (tests only; real installations use /)")
	domain := flags.String("domain", "", "canonical hostname")
	proxy := flags.String("proxy", "", "app-only, caddy, nginx, or apache")
	port := flags.Int("port", 8090, "app-only loopback port")
	emails := flags.String("allowed-emails", "", "comma-separated allowlisted adults")
	archive := flags.String("archive", "", "backup archive")
	confirm := flags.Bool("confirm-purge", false, "confirm the printed exact recovery targets")
	artifactPath := flags.String("artifact", "", "verified Mithra binary candidate")
	manifestPath := flags.String("manifest", "", "canonical signed release manifest")
	signaturePath := flags.String("signature", "", "detached Ed25519 signature")
	resendPath := flags.String("resend-key-file", "", "input Resend credential file")
	resendFrom := flags.String("resend-from", "", "validated Resend sender identity")
	ownerEmail := flags.String("owner-email", "", "seed household owner email")
	partnerEmail := flags.String("partner-email", "", "seed household partner email")
	masterKeyPath := flags.String("master-key-file", "", "retained master-key credential file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	allowed := splitEmails(*emails)
	basePaths := installer.OwnedPaths(*root, "")
	configuredProxy, configuredDomain, configuredPort := installedRuntime(basePaths.Config)
	if *proxy == "" && configuredProxy != "" {
		*proxy = string(configuredProxy)
	}
	if *domain == "" {
		*domain = configuredDomain
	}
	if *port == 8090 && configuredPort != 0 {
		*port = configuredPort
	}
	facts := installer.DetectHost(ctx, *root, *domain, *port)
	paths := installer.OwnedPaths(*root, installer.ProxyMode(*proxy))
	if args[0] == "reset-demo" {
		if len(allowed) == 0 {
			allowed = installedAllowlist(paths.Config)
		}
		if !emailAllowed(allowed, *ownerEmail) || !emailAllowed(allowed, *partnerEmail) {
			return errors.New("both demo accounts must already be in the installed allowlist")
		}
		if *masterKeyPath == "" {
			*masterKeyPath = paths.MasterKey
		}
		key, err := readMasterKey(*masterKeyPath)
		if err != nil {
			return err
		}
		defer clear(key)
		restart, err := quiesce(ctx, *root)
		if err != nil {
			return err
		}
		receipt, err := demo.Reset(ctx, demo.Config{DatabasePath: paths.Database, SourceRoot: paths.Sources, BackupRoot: paths.Backups, OwnerEmail: *ownerEmail, PartnerEmail: *partnerEmail, MasterKey: key})
		if restartErr := restart(); err != nil || restartErr != nil {
			return errors.Join(err, restartErr)
		}
		return json.NewEncoder(os.Stdout).Encode(receipt)
	}
	if operation == installer.Upgrade || operation == installer.Reconfigure {
		facts.MigrationClean, facts.SQLiteClean, facts.KeyMatches, facts.BackupExists = false, false, false, false
		if installer.DatabasePreflight(ctx, paths.Database) == nil {
			facts.MigrationClean, facts.SQLiteClean = true, true
		}
		if key, keyErr := readMasterKey(paths.MasterKey); keyErr == nil {
			latest := installer.LatestBackup(paths.Backups)
			if latest != "" && installer.VerifyBackupArchive(latest, key) == nil {
				facts.KeyMatches, facts.BackupExists = true, true
			}
			clear(key)
		}
	}
	switch operation {
	case installer.Backup:
		key, err := readMasterKey(paths.MasterKey)
		if err != nil {
			return err
		}
		restart, err := quiesce(ctx, *root)
		if err != nil {
			clear(key)
			return err
		}
		archive, err := installer.CreateBackup(ctx, paths, key, time.Now())
		clear(key)
		if restartErr := restart(); err != nil || restartErr != nil {
			return errors.Join(err, restartErr)
		}
		fmt.Println(archive)
		return nil
	case installer.Restore:
		key, err := readMasterKey(paths.MasterKey)
		if err != nil {
			return err
		}
		restart, err := quiesce(ctx, *root)
		if err != nil {
			clear(key)
			return err
		}
		plan := installer.Plan{Options: installer.Options{Root: *root, Domain: *domain, Port: *port}, Proxy: installer.ProxyMode(*proxy)}
		if plan.Proxy == "" {
			plan.Proxy = facts.DetectedProxy
			if plan.Proxy == "" {
				plan.Proxy = installer.AppOnly
			}
		}
		health := func() error {
			if *root == "/" {
				if err := exec.CommandContext(ctx, "systemctl", "start", "mithra.service").Run(); err != nil {
					return err
				}
			}
			if err := localPlanHealth(ctx, plan); err != nil {
				if *root == "/" {
					_ = exec.CommandContext(ctx, "systemctl", "stop", "mithra.service").Run()
				}
				return err
			}
			if plan.Proxy != installer.AppOnly {
				if err := webHealth(ctx, "https://"+plan.Options.Domain+"/healthz"); err != nil {
					if *root == "/" {
						_ = exec.CommandContext(ctx, "systemctl", "stop", "mithra.service").Run()
					}
					return err
				}
			}
			if *root == "/" {
				if err := exec.CommandContext(ctx, "systemctl", "start", "mithra-backup.timer").Run(); err != nil {
					_ = exec.CommandContext(ctx, "systemctl", "stop", "mithra.service").Run()
					return err
				}
			}
			return installer.VerifyArivuBaseline(ctx, *root, facts.ArivuBaseline)
		}
		err = installer.RestoreBackup(ctx, paths, *archive, key, allowed, health)
		clear(key)
		if err != nil {
			return errors.Join(err, restart())
		}
		return nil
	case installer.Status:
		statusProxy := installer.ProxyMode(*proxy)
		if statusProxy == "" {
			statusProxy = facts.DetectedProxy
			if statusProxy == "" {
				statusProxy = installer.AppOnly
			}
		}
		statusPaths := installer.OwnedPaths(*root, statusProxy)
		report := installer.InspectStatus(statusPaths, listener(string(statusProxy), *port), versionOf(statusPaths.Version))
		if *root == "/" {
			report.ServiceHealthy = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "mithra.service").Run() == nil
			report.BackupTimerActive = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "mithra-backup.timer").Run() == nil
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	plan, err := installer.BuildPlan(installer.Options{Operation: operation, Root: *root, Domain: *domain, Proxy: installer.ProxyMode(*proxy), Port: *port, AllowedEmails: allowed, ResendFrom: *resendFrom, Archive: *archive, ConfirmPurge: *confirm}, facts)
	if err != nil {
		return err
	}
	if args[0] == "plan" {
		return json.NewEncoder(os.Stdout).Encode(plan)
	}
	switch operation {
	case installer.Install, installer.Upgrade, installer.Reconfigure:
		return installRelease(ctx, plan, *artifactPath, *manifestPath, *signaturePath, *resendPath)
	case installer.Uninstall:
		if *root == "/" {
			if output, stopErr := exec.CommandContext(ctx, "systemctl", "disable", "--now", "mithra.service", "mithra-backup.timer").CombinedOutput(); stopErr != nil {
				return fmt.Errorf("stop Mithra before uninstall: %s: %w", strings.TrimSpace(string(output)), stopErr)
			}
		}
		if err := installer.RemoveRuntime(plan); err != nil {
			return err
		}
		if *root == "/" {
			if err := exec.CommandContext(ctx, "systemctl", "daemon-reload").Run(); err != nil {
				return err
			}
			if err := validateAndReloadProxy(ctx, plan.Proxy, false); err != nil && plan.Proxy != installer.AppOnly {
				return err
			}
		}
		return installer.VerifyArivuBaseline(ctx, *root, facts.ArivuBaseline)
	case installer.Purge:
		return installer.PurgeRecovery(plan)
	default:
		return fmt.Errorf("operation %q is not directly executable", operation)
	}
}

func installRelease(ctx context.Context, plan installer.Plan, artifactPath, manifestPath, signaturePath, resendPath string) error {
	if artifactPath == "" || manifestPath == "" || signaturePath == "" || ((plan.Options.Operation == installer.Install || plan.Options.Operation == installer.Reconfigure) && resendPath == "") {
		return errors.New("artifact, manifest, signature, and operation-required resend-key-file are required")
	}
	key, err := installer.DecodePublisherKey(publisherKeyBase64)
	if err != nil {
		return errors.New("this installer was not built with the pinned publisher key")
	}
	artifact, err := readBounded(artifactPath, 256<<20)
	if err != nil {
		return err
	}
	manifest, err := readBounded(manifestPath, 128<<10)
	if err != nil {
		return err
	}
	signature, err := readBounded(signaturePath, 1<<10)
	if err != nil {
		return err
	}
	var resend []byte
	if resendPath != "" {
		resend, err = readBounded(resendPath, 8<<10)
		if err != nil {
			return err
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	installerBinary, err := readBounded(self, 256<<20)
	if err != nil {
		return err
	}
	validate := func() error { return nil }
	if plan.Options.Root == "/" {
		validate = func() error { return activate(ctx, plan) }
	}
	paths := installer.OwnedPaths(plan.Options.Root, plan.Proxy)
	var masterKey []byte
	var rollbackArchive string
	var recoveryRestart func() error
	if plan.Options.Operation == installer.Upgrade || plan.Options.Operation == installer.Reconfigure {
		masterKey, err = readMasterKey(paths.MasterKey)
		if err != nil {
			return err
		}
		defer clear(masterKey)
		recoveryRestart, err = quiesce(ctx, plan.Options.Root)
		if err != nil {
			return err
		}
		rollbackArchive, err = installer.CreateBackup(ctx, paths, masterKey, time.Now())
		if err != nil {
			return errors.Join(err, recoveryRestart())
		}
		if err := installer.RehearseMigrations(ctx, paths.Database); err != nil {
			return errors.Join(err, recoveryRestart())
		}
	}
	freshDirectories := map[string]bool{}
	if plan.Options.Operation == installer.Install {
		for _, directory := range []string{filepath.Dir(paths.Config), paths.Data, paths.Backups, filepath.Dir(paths.Socket)} {
			freshDirectories[directory] = pathExists(directory)
		}
	}
	if err := installer.InstallRelease(plan, installer.ReleaseInstall{ArtifactName: filepath.Base(artifactPath), InstallerName: filepath.Base(self), Artifact: artifact, Installer: installerBinary, Manifest: manifest, Signature: signature, PublisherKey: key, ResendCredential: string(resend), Validate: validate}); err != nil {
		if rollbackArchive != "" {
			_ = exec.CommandContext(ctx, "systemctl", "stop", "mithra.service").Run()
			rollbackErr := installer.RestoreGeneration(paths, rollbackArchive, masterKey, func() error {
				if plan.Options.Root != "/" {
					return nil
				}
				return exec.CommandContext(ctx, "systemctl", "start", "mithra.service", "mithra-backup.timer").Run()
			})
			if rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
		} else if recoveryRestart != nil {
			err = errors.Join(err, recoveryRestart())
		} else if plan.Options.Root == "/" {
			_ = exec.CommandContext(ctx, "systemctl", "disable", "--now", "mithra.service", "mithra-backup.timer").Run()
		}
		if plan.Options.Operation == installer.Install {
			for directory, existed := range freshDirectories {
				if !existed {
					_ = os.RemoveAll(directory)
				}
			}
		}
		return err
	}
	if plan.Options.Operation == installer.Install {
		masterKey, err = readMasterKey(paths.MasterKey)
		if err != nil {
			return err
		}
		defer clear(masterKey)
		restart, err := quiesce(ctx, plan.Options.Root)
		if err != nil {
			return err
		}
		_, backupErr := installer.CreateBackup(ctx, paths, masterKey, time.Now())
		return errors.Join(backupErr, restart())
	}
	return nil
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func quiesce(ctx context.Context, root string) (func() error, error) {
	if root != "/" {
		return func() error { return nil }, nil
	}
	output, err := exec.CommandContext(ctx, "systemctl", "stop", "mithra.service", "mithra-backup.timer").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("quiesce Mithra: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return func() error {
		output, err := exec.CommandContext(context.Background(), "systemctl", "start", "mithra.service", "mithra-backup.timer").CombinedOutput()
		if err != nil {
			return fmt.Errorf("restart Mithra: %s: %w", strings.TrimSpace(string(output)), err)
		}
		return nil
	}, nil
}

func activate(ctx context.Context, plan installer.Plan) error {
	identity, err := ensureIdentity(ctx, plan.Proxy)
	if err != nil {
		return err
	}
	fail := func(activationErr error) error {
		return errors.Join(activationErr, rollbackIdentity(ctx, identity))
	}
	for _, command := range [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "--now", "mithra.service"}} {
		if output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); err != nil {
			return fail(fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err))
		}
	}
	if err := localPlanHealth(ctx, plan); err != nil {
		return fail(err)
	}
	if err := validateAndReloadProxy(ctx, plan.Proxy, identity.membershipAdded); err != nil {
		return fail(err)
	}
	if plan.Proxy != installer.AppOnly {
		if err := webHealth(ctx, "https://"+plan.Options.Domain+"/healthz"); err != nil {
			return fail(err)
		}
	}
	output, err := exec.CommandContext(ctx, "systemctl", "enable", "--now", "mithra-backup.timer").CombinedOutput()
	if err != nil {
		return fail(fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err))
	}
	if err := installer.VerifyArivuBaseline(ctx, plan.Options.Root, plan.ArivuBaseline); err != nil {
		return fail(err)
	}
	return nil
}

func localHealth(ctx context.Context, port int) error {
	return webHealth(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
}

func localPlanHealth(ctx context.Context, plan installer.Plan) error {
	if plan.Proxy == installer.AppOnly {
		return localHealth(ctx, plan.Options.Port)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", "/run/mithra/mithra.sock")
	}}
	defer transport.CloseIdleConnections()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://mithra/healthz", nil)
	response, err := (&http.Client{Timeout: 5 * time.Second, Transport: transport}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("local socket health returned %s", response.Status)
	}
	return nil
}

func webHealth(ctx context.Context, target string) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned %s", response.Status)
	}
	return nil
}

type identityState struct {
	userCreated, membershipAdded bool
	proxyUser                    string
	directories                  []directoryState
}

type directoryState struct {
	path     string
	existed  bool
	mode     os.FileMode
	uid, gid int
}

func ensureIdentity(ctx context.Context, proxy installer.ProxyMode) (identityState, error) {
	state := identityState{}
	if exec.CommandContext(ctx, "id", "-u", "mithra").Run() != nil {
		if output, err := exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--home-dir", "/var/lib/mithra", "--shell", "/usr/sbin/nologin", "mithra").CombinedOutput(); err != nil {
			return state, fmt.Errorf("create Mithra service identity: %s: %w", strings.TrimSpace(string(output)), err)
		}
		state.userCreated = true
	}
	for _, directory := range []string{"/var/lib/mithra", "/var/lib/mithra/sources", "/var/backups/mithra", "/etc/mithra/credentials"} {
		directoryBefore := directoryState{path: directory}
		if info, err := os.Lstat(directory); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return state, fmt.Errorf("Mithra directory is unsafe: %s", directory)
			}
			directoryBefore.existed, directoryBefore.mode = true, info.Mode().Perm()
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				directoryBefore.uid, directoryBefore.gid = int(stat.Uid), int(stat.Gid)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return state, err
		}
		state.directories = append(state.directories, directoryBefore)
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return state, err
		}
		if output, err := exec.CommandContext(ctx, "chown", "mithra:mithra", directory).CombinedOutput(); err != nil {
			return state, fmt.Errorf("own %s: %s: %w", directory, strings.TrimSpace(string(output)), err)
		}
	}
	var candidates []string
	switch proxy {
	case installer.Caddy:
		candidates = []string{"caddy"}
	case installer.Nginx:
		candidates = []string{"www-data", "nginx"}
	case installer.Apache:
		candidates = []string{"www-data", "apache"}
	}
	for _, candidate := range candidates {
		if exec.CommandContext(ctx, "id", "-u", candidate).Run() == nil {
			state.proxyUser = candidate
			break
		}
	}
	if len(candidates) > 0 && state.proxyUser == "" {
		return state, fmt.Errorf("proxy service identity not found (tried %s)", strings.Join(candidates, ", "))
	}
	if state.proxyUser != "" {
		groups, err := exec.CommandContext(ctx, "id", "-nG", state.proxyUser).Output()
		if err != nil {
			return state, err
		}
		if !containsField(string(groups), "mithra") {
			if output, err := exec.CommandContext(ctx, "usermod", "-a", "-G", "mithra", state.proxyUser).CombinedOutput(); err != nil {
				return state, fmt.Errorf("grant proxy socket access: %s: %w", strings.TrimSpace(string(output)), err)
			}
			state.membershipAdded = true
		}
	}
	return state, nil
}

func rollbackIdentity(ctx context.Context, state identityState) error {
	_ = exec.CommandContext(ctx, "systemctl", "disable", "--now", "mithra.service", "mithra-backup.timer").Run()
	var failures []error
	if state.membershipAdded && state.proxyUser != "" {
		if output, err := exec.CommandContext(ctx, "gpasswd", "-d", state.proxyUser, "mithra").CombinedOutput(); err != nil {
			failures = append(failures, fmt.Errorf("remove proxy group membership: %s: %w", strings.TrimSpace(string(output)), err))
		}
	}
	for index := len(state.directories) - 1; index >= 0; index-- {
		directory := state.directories[index]
		if !directory.existed {
			if err := os.RemoveAll(directory.path); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err := os.Chown(directory.path, directory.uid, directory.gid); err != nil {
			failures = append(failures, err)
		}
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			failures = append(failures, err)
		}
	}
	if state.userCreated {
		if output, err := exec.CommandContext(ctx, "userdel", "mithra").CombinedOutput(); err != nil {
			failures = append(failures, fmt.Errorf("remove Mithra identity: %s: %w", strings.TrimSpace(string(output)), err))
		}
	}
	return errors.Join(failures...)
}

func containsField(value, field string) bool {
	for _, candidate := range strings.Fields(value) {
		if candidate == field {
			return true
		}
	}
	return false
}

func validateAndReloadProxy(ctx context.Context, proxy installer.ProxyMode, restart bool) error {
	var command []string
	switch proxy {
	case installer.Caddy:
		command = []string{"caddy", "validate", "--config", "/etc/caddy/Caddyfile"}
	case installer.Nginx:
		command = []string{"nginx", "-t"}
	case installer.Apache:
		command = []string{"apache2ctl", "configtest"}
	default:
		return nil
	}
	if output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("proxy validation: %s: %w", strings.TrimSpace(string(output)), err)
	}
	verb := "reload"
	if restart {
		verb = "restart"
	}
	service := string(proxy)
	if proxy == installer.Apache {
		service = "apache2"
	}
	output, err := exec.CommandContext(ctx, "systemctl", verb, service).CombinedOutput()
	if err != nil {
		return fmt.Errorf("proxy %s: %s: %w", verb, strings.TrimSpace(string(output)), err)
	}
	return nil
}

func readMasterKey(path string) ([]byte, error) {
	raw, err := readBounded(path, 1<<10)
	if err != nil {
		return nil, err
	}
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("retained master key is invalid")
	}
	return key, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, fmt.Errorf("invalid input file %s", path)
	}
	return os.ReadFile(path)
}

func splitEmails(value string) []string {
	var out []string
	seen := map[string]bool{}
	for _, email := range strings.Split(value, ",") {
		email = strings.ToLower(strings.TrimSpace(email))
		if email != "" && !seen[email] {
			seen[email] = true
			out = append(out, email)
		}
	}
	return out
}
func listener(proxy string, port int) string {
	if proxy == string(installer.AppOnly) || proxy == "" {
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	return "/run/mithra/mithra.sock"
}
func versionOf(path string) string {
	out, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func installedRuntime(path string) (installer.ProxyMode, string, int) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return "", "", 0
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	if values["MITHRA_SOCKET"] != "" {
		origin := strings.TrimPrefix(strings.TrimPrefix(values["MITHRA_CANONICAL_ORIGIN"], "https://"), "http://")
		return "", strings.TrimSuffix(origin, "/"), 0
	}
	_, portText, err := net.SplitHostPort(values["MITHRA_ADDR"])
	if err != nil {
		return installer.AppOnly, "", 0
	}
	port, _ := strconv.Atoi(portText)
	return installer.AppOnly, "", port
}

func installedAllowlist(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 64<<10 {
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if value, ok := strings.CutPrefix(line, "ALLOWED_EMAILS="); ok {
			return splitEmails(strings.Trim(strings.TrimSpace(value), `"`))
		}
	}
	return nil
}

func emailAllowed(allowed []string, candidate string) bool {
	candidate = strings.ToLower(strings.TrimSpace(candidate))
	for _, email := range allowed {
		if email == candidate {
			return true
		}
	}
	return false
}
