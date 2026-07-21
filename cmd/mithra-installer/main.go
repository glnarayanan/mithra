package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/internal/demo"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/installer"
)

// Set by the release build with -X main.publisherKeyBase64=... . An unset key
// deliberately makes installation impossible rather than weakening trust.
var publisherKeyBase64 string

// Set by the release build with -X main.buildVersion=... .
var buildVersion = "dev"

func main() {
	os.Exit(execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string) error {
	return runWithOutput(ctx, args, os.Stdout)
}

func execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	parsed, err := parseInstallerCommand(args)
	if err != nil {
		fmt.Fprintln(stderr, "mithra-installer:", err)
		return 2
	}
	switch parsed.name {
	case "-h", "--help", "help":
		command := ""
		if parsed.name == "help" {
			if len(args) > 2 {
				fmt.Fprintln(stderr, "mithra-installer: help accepts at most one command")
				return 2
			}
			if len(args) > 1 {
				command = args[1]
			}
			if command == "--help" || command == "-h" {
				command = ""
			}
		} else if len(args) != 1 {
			fmt.Fprintln(stderr, "mithra-installer: help accepts no arguments")
			return 2
		}
		if err := installerHelp(command, stdout); err != nil {
			fmt.Fprintln(stderr, "mithra-installer:", err)
			return 2
		}
		return 0
	case "version":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_ = installerHelp("version", stdout)
			return 0
		}
		if len(args) != 1 {
			fmt.Fprintln(stderr, "mithra-installer: version accepts no arguments")
			return 2
		}
		fmt.Fprintln(stdout, buildVersion)
		return 0
	case "completion":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_ = installerHelp("completion", stdout)
			return 0
		}
		if len(args) != 2 {
			fmt.Fprintln(stderr, "mithra-installer: completion requires bash, zsh, or fish")
			return 2
		}
		if err := completion(args[1], stdout); err != nil {
			fmt.Fprintln(stderr, "mithra-installer:", err)
			return 2
		}
		return 0
	case "verify-backup":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			_ = installerHelp("verify-backup", stdout)
			return 0
		}
		if err := runVerifyBackup(args[1:], stdout); err != nil {
			fmt.Fprintln(stderr, "mithra-installer:", err)
			var usage usageError
			if errors.As(err, &usage) {
				return 2
			}
			return 1
		}
		return 0
	default:
		if strings.HasPrefix(parsed.name, "help-") {
			_ = installerHelp(strings.TrimPrefix(parsed.name, "help-"), stdout)
			return 0
		}
	}
	if err := runParsed(ctx, parsed, stdout); err != nil {
		fmt.Fprintln(stderr, "mithra-installer:", err)
		return 1
	}
	return 0
}

func runWithOutput(ctx context.Context, args []string, output io.Writer) error {
	parsed, err := parseInstallerCommand(args)
	if err != nil {
		return err
	}
	if parsed.name == "-h" || parsed.name == "--help" || parsed.name == "help" || parsed.name == "version" || parsed.name == "completion" {
		return nil
	}
	if parsed.name == "verify-backup" {
		return runVerifyBackup(args[1:], output)
	}
	if strings.HasPrefix(parsed.name, "help-") {
		return nil
	}
	return runParsed(ctx, parsed, output)
}

func runParsed(ctx context.Context, parsed parsedInstallerCommand, output io.Writer) error {
	operation := parsed.operation
	f := parsed.flags
	root, domain, proxy, port, emails, archive, confirm := f.root, f.domain, f.proxy, f.port, f.emails, f.archive, f.confirm
	artifactPath, candidateInstallerPath, manifestPath, signaturePath, releaseVersion := f.artifact, f.candidateInstaller, f.manifest, f.signature, f.releaseVersion
	if operation == installer.Upgrade && *artifactPath == "" && *candidateInstallerPath == "" && *manifestPath == "" && *signaturePath == "" && *releaseVersion == "" {
		return runAutomaticUpgrade(ctx, parsed, output)
	}
	plunkPath, plunkFrom, ownerEmail, partnerEmail, ownerPasswordPath, partnerPasswordPath, masterKeyPath := f.plunkPath, f.plunkFrom, f.ownerEmail, f.partnerEmail, f.ownerPasswordPath, f.partnerPasswordPath, f.masterKeyPath
	allowed := splitEmails(*emails)
	basePaths := installer.OwnedPaths(*root, "")
	configuredProxy, configuredDomain, configuredPort := installedRuntime(basePaths.Config)
	if configuredProxy == "" {
		configuredProxy = installer.InferOwnedProxyMode(*root)
	}
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
	if (operation == installer.Upgrade || operation == installer.Restore) && len(allowed) == 0 {
		allowed = installedAllowlist(paths.Config)
	}
	if parsed.planOnly && (operation == installer.Upgrade || operation == installer.Reconfigure || operation == installer.Restore) {
		preflightPlanFacts(ctx, operation, paths, *archive, &facts)
	}
	if parsed.planOnly {
		plan, err := installer.BuildPlan(installer.Options{Operation: operation, Root: *root, Domain: *domain, Proxy: installer.ProxyMode(*proxy), PreviousProxy: configuredProxy, Port: *port, AllowedEmails: allowed, PlunkFrom: *plunkFrom, Archive: *archive, ConfirmPurge: *confirm || operation == installer.Purge}, facts)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(plan)
	}
	if parsed.name == "reset-demo" {
		if len(allowed) == 0 {
			allowed = installedAllowlist(paths.Config)
		}
		if !emailAllowed(allowed, *ownerEmail) || !emailAllowed(allowed, *partnerEmail) {
			return errors.New("both demo accounts must already be in the installed allowlist")
		}
		if *masterKeyPath == "" {
			*masterKeyPath = paths.MasterKey
		}
		if (*ownerPasswordPath == "") != (*partnerPasswordPath == "") {
			return errors.New("reset-demo requires both password files or neither")
		}
		var ownerPassword, partnerPassword []byte
		if *ownerPasswordPath != "" {
			var err error
			ownerPassword, err = readDemoPasswordFile(*ownerPasswordPath)
			if err != nil {
				return err
			}
			defer clear(ownerPassword)
			partnerPassword, err = readDemoPasswordFile(*partnerPasswordPath)
			if err != nil {
				return err
			}
			defer clear(partnerPassword)
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
		receipt, err := demo.Reset(ctx, demo.Config{DatabasePath: paths.Database, SourceRoot: paths.Sources, BackupRoot: paths.Backups, OwnerEmail: *ownerEmail, PartnerEmail: *partnerEmail, OwnerPassword: ownerPassword, PartnerPassword: partnerPassword, MasterKey: key})
		if restartErr := restart(); err != nil || restartErr != nil {
			return errors.Join(err, restartErr)
		}
		return json.NewEncoder(output).Encode(receipt)
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
		if len(allowed) == 0 {
			allowed = installedAllowlist(paths.Config)
		}
		key, err := readMasterKey(paths.MasterKey)
		if err != nil {
			return err
		}
		owner, err := serviceOwnership(*root)
		if err != nil {
			clear(key)
			return err
		}
		prepared, err := installer.PrepareRestore(ctx, paths, *archive, key, allowed, owner)
		if err != nil {
			clear(key)
			return err
		}
		defer prepared.Cleanup()
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
			if err := waitForHealth(ctx, func(checkCtx context.Context) error {
				return localPlanHealth(checkCtx, plan)
			}); err != nil {
				if *root == "/" {
					_ = exec.CommandContext(ctx, "systemctl", "stop", "mithra.service").Run()
				}
				return err
			}
			if plan.Proxy != installer.AppOnly {
				if err := waitForHealth(ctx, func(checkCtx context.Context) error {
					return webHealth(checkCtx, "https://"+plan.Options.Domain+"/healthz")
				}); err != nil {
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
		err = installer.RestorePrepared(ctx, paths, prepared, key, allowed, owner, health)
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
			report.ServiceActive = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "mithra.service").Run() == nil
			if report.ServiceActive {
				report.ServiceHealthy = localPlanHealth(ctx, installer.Plan{Options: installer.Options{Root: *root, Port: *port}, Proxy: statusProxy}) == nil
			}
			report.BackupTimerActive = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "mithra-backup.timer").Run() == nil
			report.PDFParserActive = exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "mithra-pdf-parser.socket").Run() == nil
		}
		return json.NewEncoder(output).Encode(report)
	}
	plan, err := installer.BuildPlan(installer.Options{Operation: operation, Root: *root, Domain: *domain, Proxy: installer.ProxyMode(*proxy), PreviousProxy: configuredProxy, Port: *port, AllowedEmails: allowed, PlunkFrom: *plunkFrom, Archive: *archive, ConfirmPurge: *confirm}, facts)
	if err != nil {
		return err
	}
	switch operation {
	case installer.Install, installer.Upgrade, installer.Reconfigure:
		return installRelease(ctx, plan, *artifactPath, *candidateInstallerPath, *manifestPath, *signaturePath, *releaseVersion, *plunkPath)
	case installer.Uninstall:
		if *root == "/" {
			if output, stopErr := exec.CommandContext(ctx, "systemctl", "disable", "--now", "mithra.service", "mithra-backup.timer", "mithra-pdf-parser.service", "mithra-pdf-parser.socket").CombinedOutput(); stopErr != nil {
				return fmt.Errorf("stop Mithra before uninstall: %s: %w", strings.TrimSpace(string(output)), stopErr)
			}
		}
		if err := installer.RemoveRuntime(plan); err != nil {
			return err
		}
		if *root == "/" {
			if err := removeParserIdentity(ctx); err != nil {
				return err
			}
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

// preflightPlanFacts reads existing recovery evidence only. Planning must not
// quiesce the service or create a staging generation in the owned data tree.
func preflightPlanFacts(ctx context.Context, operation installer.Operation, paths installer.Paths, archive string, facts *installer.HostFacts) {
	if operation == installer.Upgrade || operation == installer.Reconfigure {
		facts.MigrationClean, facts.SQLiteClean, facts.KeyMatches, facts.BackupExists = false, false, false, false
		if installer.DatabasePreflight(ctx, paths.Database) == nil {
			facts.MigrationClean, facts.SQLiteClean = true, true
		}
		if key, err := readMasterKey(paths.MasterKey); err == nil {
			latest := installer.LatestBackup(paths.Backups)
			if latest != "" && installer.VerifyBackupArchive(latest, key) == nil {
				facts.KeyMatches, facts.BackupExists = true, true
			}
			clear(key)
		}
	}
	if operation != installer.Restore {
		return
	}
	facts.ArchiveValid, facts.JournalValid, facts.KeyMatches = false, false, false
	key, err := readMasterKey(paths.MasterKey)
	if err != nil {
		return
	}
	defer clear(key)
	if installer.VerifyBackupArchive(archive, key) != nil {
		return
	}
	facts.ArchiveValid, facts.KeyMatches = true, true
	journal, err := imports.NewDeletionJournal(paths.Journal, key)
	if err != nil {
		return
	}
	if _, err = journal.ReadAll(); err == nil {
		facts.JournalValid = true
	}
}

func installRelease(ctx context.Context, plan installer.Plan, artifactPath, candidateInstallerPath, manifestPath, signaturePath, requestedVersion, plunkPath string) error {
	if (plan.Options.Operation == installer.Install || plan.Options.Operation == installer.Reconfigure) && plunkPath == "" {
		return errors.New("operation-required plunk-key-file is required")
	}
	var artifact, candidateInstaller, manifest, signature []byte
	var publisherKey ed25519.PublicKey
	var err error
	if plan.Options.Operation != installer.Reconfigure {
		if artifactPath == "" || manifestPath == "" || signaturePath == "" {
			return errors.New("artifact, manifest, and signature are required")
		}
		if plan.Options.Operation != installer.Install && (candidateInstallerPath == "" || requestedVersion == "") {
			return errors.New("candidate-installer and release-version are required for upgrade")
		}
		if candidateInstallerPath != "" && requestedVersion == "" {
			return errors.New("release-version is required with candidate-installer")
		}
		if candidateInstallerPath != "" && buildVersion != "dev" && buildVersion != requestedVersion {
			return errors.New("candidate installer version does not match the requested tag")
		}
		artifact, err = readBounded(artifactPath, 256<<20)
		if err != nil {
			return err
		}
		manifest, err = readBounded(manifestPath, 128<<10)
		if err != nil {
			return err
		}
		signature, err = readBounded(signaturePath, 1<<10)
		if err != nil {
			return err
		}
		var keyErr error
		publisherKey, keyErr = installer.DecodePublisherKey(publisherKeyBase64)
		if keyErr != nil {
			return errors.New("this installer was not built with the pinned publisher key")
		}
		if _, verifyErr := installer.VerifyRelease(manifest, signature, publisherKey, filepath.Base(artifactPath), artifact); verifyErr != nil {
			return verifyErr
		}
		candidateName := filepath.Base(candidateInstallerPath)
		if candidateInstallerPath != "" {
			candidateInstaller, err = readBounded(candidateInstallerPath, 256<<20)
			if err != nil {
				return err
			}
		} else {
			candidateName, candidateInstaller, err = signedRunningInstaller(manifest, signature, publisherKey)
			if err != nil {
				return err
			}
		}
		verified, verifyErr := installer.VerifyRelease(manifest, signature, publisherKey, candidateName, candidateInstaller)
		if verifyErr != nil {
			return verifyErr
		}
		if candidateInstallerPath == "" && isReleaseBuild(buildVersion) && verified.Version != buildVersion {
			return errors.New("signed release version does not match the running installer build")
		}
		if verifyErr = installer.VerifyReleaseVersion(verified, requestedVersion, versionOf(installer.OwnedPaths(plan.Options.Root, plan.Proxy).Version)); verifyErr != nil {
			return verifyErr
		}
		candidateInstallerPath = candidateName
	}
	var plunk []byte
	if plunkPath != "" {
		plunk, err = readBounded(plunkPath, 8<<10)
		if err != nil {
			return err
		}
	}
	validate := func() error { return nil }
	if plan.Options.Root == "/" {
		validate = func() error { return activate(ctx, plan) }
	}
	paths := installer.OwnedPaths(plan.Options.Root, plan.Proxy)
	var masterKey []byte
	var rollbackArchive string
	var recoveryRestart func() error
	var rollbackOwner installer.RestoreOwnership
	if plan.Options.Operation == installer.Upgrade || plan.Options.Operation == installer.Reconfigure {
		masterKey, err = readMasterKey(paths.MasterKey)
		if err != nil {
			return err
		}
		defer clear(masterKey)
		rollbackOwner, err = serviceOwnership(plan.Options.Root)
		if err != nil {
			return err
		}
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
	paths = installer.OwnedPaths(plan.Options.Root, plan.Proxy)
	if err := installer.InstallRelease(plan, installer.ReleaseInstall{ArtifactName: filepath.Base(artifactPath), InstallerName: filepath.Base(candidateInstallerPath), Artifact: artifact, Installer: candidateInstaller, Manifest: manifest, Signature: signature, PublisherKey: publisherKey, RequestedVersion: requestedVersion, InstalledVersion: versionOf(paths.Version), PlunkCredential: string(plunk), Validate: validate}); err != nil {
		if rollbackArchive != "" {
			_ = exec.CommandContext(ctx, "systemctl", "stop", "mithra.service").Run()
			rollbackPlan := plan
			if rollbackProxy, rollbackDomain, rollbackPort := installedRuntime(paths.Config); rollbackProxy != "" {
				rollbackPlan.Proxy = rollbackProxy
				rollbackPlan.Options.Domain = rollbackDomain
				if rollbackPort != 0 {
					rollbackPlan.Options.Port = rollbackPort
				}
			}
			rollbackErr := installer.RestoreGenerationWithOwnership(paths, rollbackArchive, masterKey, rollbackOwner, func() error {
				if plan.Options.Root != "/" {
					return nil
				}
				if err := exec.CommandContext(ctx, "systemctl", "start", "mithra.service", "mithra-backup.timer").Run(); err != nil {
					return err
				}
				if err := waitForHealth(ctx, func(checkCtx context.Context) error {
					return localPlanHealth(checkCtx, rollbackPlan)
				}); err != nil {
					return err
				}
				return installer.VerifyArivuBaseline(ctx, plan.Options.Root, plan.ArivuBaseline)
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
	paths := installer.OwnedPaths(plan.Options.Root, plan.Proxy)
	parserEnabled := parserUnitsPresent(paths)
	if !parserEnabled {
		if err := removeParserIdentity(ctx); err != nil {
			return err
		}
	}
	identity, err := ensureIdentity(ctx, plan.Proxy, parserEnabled)
	if err != nil {
		return err
	}
	fail := func(activationErr error) error {
		return errors.Join(activationErr, rollbackIdentity(ctx, identity))
	}
	commands := [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "--now", "mithra.service"}}
	if parserEnabled {
		commands = append(commands, []string{"systemctl", "enable", "--now", "mithra-pdf-parser.socket"})
	}
	for _, command := range commands {
		if output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); err != nil {
			return fail(fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err))
		}
	}
	if err := waitForHealth(ctx, func(checkCtx context.Context) error {
		return localPlanHealth(checkCtx, plan)
	}); err != nil {
		return fail(err)
	}
	if err := validatePlanProxies(ctx, plan, identity.membershipAdded); err != nil {
		return fail(err)
	}
	if plan.Proxy != installer.AppOnly {
		if err := waitForHealth(ctx, func(checkCtx context.Context) error {
			return webHealth(checkCtx, "https://"+plan.Options.Domain+"/healthz")
		}); err != nil {
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

func parserUnitsPresent(paths installer.Paths) bool {
	return pathExists(paths.PDFParserService) && pathExists(paths.PDFParserSocket)
}

func localHealth(ctx context.Context, port int) error {
	return webHealth(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
}

func waitForHealth(ctx context.Context, check func(context.Context) error) error {
	const (
		timeout  = 30 * time.Second
		interval = 250 * time.Millisecond
	)

	healthCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		err := check(healthCtx)
		if err == nil {
			return nil
		}
		select {
		case <-healthCtx.Done():
			return fmt.Errorf("health readiness timeout: %w", err)
		case <-ticker.C:
		}
	}
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
	userCreated, parserUserCreated, membershipAdded bool
	proxyUser                                       string
	directories                                     []directoryState
}

type directoryState struct {
	path     string
	existed  bool
	mode     os.FileMode
	uid, gid int
}

func ensureIdentity(ctx context.Context, proxy installer.ProxyMode, parserEnabled bool) (identityState, error) {
	state := identityState{}
	if exec.CommandContext(ctx, "id", "-u", "mithra").Run() != nil {
		if output, err := exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--home-dir", "/var/lib/mithra", "--shell", "/usr/sbin/nologin", "mithra").CombinedOutput(); err != nil {
			return state, fmt.Errorf("create Mithra service identity: %s: %w", strings.TrimSpace(string(output)), err)
		}
		state.userCreated = true
	}
	if parserEnabled {
		parserExists, err := systemUserExists(ctx, "mithra-pdf")
		if err != nil {
			return state, err
		}
		if !parserExists {
			if output, err := exec.CommandContext(ctx, "useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--gid", "mithra", "mithra-pdf").CombinedOutput(); err != nil {
				return state, fmt.Errorf("create PDF parser identity: %s: %w", strings.TrimSpace(string(output)), err)
			}
			state.parserUserCreated = true
		} else {
			groups, err := exec.CommandContext(ctx, "id", "-nG", "mithra-pdf").Output()
			if err != nil {
				return state, fmt.Errorf("inspect PDF parser identity: %w", err)
			}
			if !containsField(string(groups), "mithra") {
				return state, errors.New("PDF parser identity must be in the mithra group")
			}
		}
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
	_ = exec.CommandContext(ctx, "systemctl", "disable", "--now", "mithra.service", "mithra-backup.timer", "mithra-pdf-parser.service", "mithra-pdf-parser.socket").Run()
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
	if state.parserUserCreated {
		if output, err := exec.CommandContext(ctx, "userdel", "mithra-pdf").CombinedOutput(); err != nil {
			failures = append(failures, fmt.Errorf("remove PDF parser identity: %s: %w", strings.TrimSpace(string(output)), err))
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

func systemUserExists(ctx context.Context, name string) (bool, error) {
	output, err := exec.CommandContext(ctx, "id", "-u", name).CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect %s identity: %s: %w", name, strings.TrimSpace(string(output)), err)
}

func removeParserIdentity(ctx context.Context) error {
	exists, err := systemUserExists(ctx, "mithra-pdf")
	if err != nil || !exists {
		return err
	}
	if output, err := exec.CommandContext(ctx, "userdel", "mithra-pdf").CombinedOutput(); err != nil {
		return fmt.Errorf("remove PDF parser identity: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
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

func validatePlanProxies(ctx context.Context, plan installer.Plan, restart bool) error {
	if plan.PreviousProxy != "" && plan.PreviousProxy != plan.Proxy {
		if err := validateAndReloadProxy(ctx, plan.PreviousProxy, false); err != nil {
			return err
		}
	}
	return validateAndReloadProxy(ctx, plan.Proxy, restart)
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

func readDemoPasswordFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("demo password file must be a private regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("demo password file must be owned by the installer user")
	}
	raw, err := readBounded(path, 130)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	raw = bytes.TrimSuffix(raw, []byte("\r"))
	if len(raw) < 12 || len(raw) > 128 || bytes.IndexByte(raw, 0) >= 0 {
		clear(raw)
		return nil, errors.New("demo password must be 12 to 128 bytes")
	}
	return raw, nil
}

func signedRunningInstaller(manifest, signature []byte, publisherKey ed25519.PublicKey) (string, []byte, error) {
	path, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("locate running installer: %w", err)
	}
	binary, err := readBounded(path, 256<<20)
	if err != nil {
		return "", nil, fmt.Errorf("read running installer: %w", err)
	}
	name, err := signedInstallerArtifact(manifest, signature, publisherKey, binary)
	if err != nil {
		return "", nil, err
	}
	return name, binary, nil
}

func signedInstallerArtifact(manifest, signature []byte, publisherKey ed25519.PublicKey, binary []byte) (string, error) {
	parsed, err := installer.ParseManifest(manifest)
	if err != nil {
		return "", err
	}
	var matches []string
	for name := range parsed.Artifacts {
		if strings.HasPrefix(name, "mithra-installer-") {
			if _, err := installer.VerifyRelease(manifest, signature, publisherKey, name, binary); err == nil {
				matches = append(matches, name)
			}
		}
	}
	if len(matches) != 1 {
		return "", errors.New("running installer does not match exactly one signed installer artifact")
	}
	return matches[0], nil
}

func isReleaseBuild(version string) bool {
	return installer.VerifyReleaseVersion(installer.ReleaseManifest{Version: version}, "", "") == nil
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
	proxy := installer.ProxyMode(values["MITHRA_PROXY_MODE"])
	if proxy == installer.AppOnly || proxy == installer.Caddy || proxy == installer.Nginx || proxy == installer.Apache {
		_, portText, _ := net.SplitHostPort(values["MITHRA_ADDR"])
		port, _ := strconv.Atoi(portText)
		origin := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(values["MITHRA_CANONICAL_ORIGIN"], "https://"), "http://"), "/")
		return proxy, origin, port
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

func serviceOwnership(root string) (installer.RestoreOwnership, error) {
	if root != "/" {
		return installer.RestoreOwnership{}, nil
	}
	account, err := user.Lookup("mithra")
	if err != nil {
		return installer.RestoreOwnership{}, fmt.Errorf("lookup Mithra service identity: %w", err)
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil {
		return installer.RestoreOwnership{}, errors.New("Mithra service identity has invalid uid or gid")
	}
	return installer.RestoreOwnership{UID: uid, GID: gid, Set: true}, nil
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
