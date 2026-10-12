package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/glnarayanan/mithra/internal/app"
	"github.com/glnarayanan/mithra/internal/database"
	"github.com/glnarayanan/mithra/internal/demo"
	"github.com/glnarayanan/mithra/internal/imports"
	"github.com/glnarayanan/mithra/internal/installer"
)

// containerProxyMode is the only mode in which Mithra binds a non-loopback TCP
// address. A platform proxy (Dokploy's Traefik) on a private Docker network
// terminates TLS and forwards to it.
const containerProxyMode = "container"

const (
	defaultBackupTime      = "03:30"
	backupCatchUpAge       = 24 * time.Hour
	backupCatchUpDelay     = time.Minute
	defaultParserLifetime  = 5 * time.Minute
	healthcheckTimeout     = 3 * time.Second
	parserProbeTimeout     = 3 * time.Second
	maxProbeDocumentBytes  = imports.MaxFileBytes
	credentialDirectoryEnv = "MITHRA_CREDENTIALS_DIRECTORY"
)

func containerMode() bool {
	return strings.TrimSpace(os.Getenv("MITHRA_PROXY_MODE")) == containerProxyMode
}

// maintenanceCommands run beside or instead of the server inside a container.
// They print only non-secret status lines.
var maintenanceCommands = map[string]func(args []string, output io.Writer) error{
	"pdf-parser-probe":    runPDFParserProbe,
	"healthcheck":         runHealthcheck,
	"generate-master-key": runGenerateMasterKey,
	"status":              runStatus,
	"backup":              runOfflineBackup,
	"verify-backup":       runVerifyBackup,
	"restore":             runRestore,
	"reset-demo":          runResetDemo,
}

// runPDFParser serves the isolated parser either on a systemd-activated socket
// (no flags) or on a socket it creates itself (--listen), recycling the process
// after --max-lifetime the way RuntimeMaxSec does for the systemd unit.
func runPDFParser(args []string) error {
	flags := flag.NewFlagSet("mithra pdf-parser", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listenPath := flags.String("listen", "", "absolute Unix socket path to create")
	lifetime := flags.Duration("max-lifetime", defaultParserLifetime, "exit after this long so the supervisor starts a fresh process")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("pdf-parser does not accept arguments")
	}
	if *listenPath == "" {
		listener, err := imports.SystemdListener()
		if err != nil {
			return errors.New("open PDF parser socket")
		}
		defer listener.Close()
		return imports.ServePDFParser(listener)
	}
	if *lifetime <= 0 {
		return errors.New("pdf-parser lifetime must be positive")
	}
	listener, err := imports.ListenPDFParser(*listenPath)
	if err != nil {
		return errors.New("open PDF parser socket")
	}
	defer listener.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *lifetime)
	defer cancel()
	return imports.ServePDFParserFor(ctx, listener)
}

// runPDFParserProbe proves the parser is accepting connections, and with
// --file that it extracts text from a real PDF through the sandbox.
func runPDFParserProbe(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mithra pdf-parser-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socketPath := flags.String("socket", environmentDefault("MITHRA_PDF_PARSER_SOCKET", defaultPDFParserSocket), "parser Unix socket")
	documentPath := flags.String("file", "", "PDF to extract through the parser")
	sample := flags.Bool("sample", false, "extract a built-in one-page PDF through the parser")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*sample && *documentPath != "") {
		return errors.New("pdf-parser-probe accepts --socket and either --file or --sample")
	}
	if *documentPath == "" && !*sample {
		connection, err := net.DialTimeout("unix", *socketPath, parserProbeTimeout)
		if err != nil {
			return errors.New("PDF parser is unreachable")
		}
		_ = connection.Close()
		fmt.Fprintln(output, "pdf_parser=reachable")
		return nil
	}
	content := probeSamplePDF()
	if !*sample {
		var err error
		content, err = readBoundedFile(*documentPath, maxProbeDocumentBytes)
		if err != nil {
			return errors.New("probe document is unreadable")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fragments, err := imports.SocketPDFParser{Path: *socketPath}.Extract(ctx, content, imports.Limits{MaxPages: imports.MaxPDFPages, MaxText: imports.MaxPDFTextBytes})
	if err != nil {
		return fmt.Errorf("PDF parser probe failed: %w", err)
	}
	fmt.Fprintf(output, "pdf_parser=ok pages=%d\n", len(fragments))
	return nil
}

// probeSamplePDF is a minimal one-page text PDF, so a deployment can prove the
// sandboxed parser end to end without shipping or mounting a document.
func probeSamplePDF() []byte {
	stream := "BT /F1 11 Tf 72 720 Td (Mithra parser probe) Tj ET\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var output strings.Builder
	output.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for index, object := range objects {
		offsets[index] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(output.String())
}

func readBoundedFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(content) == 0 || len(content) > limit {
		return nil, errors.New("file is empty or too large")
	}
	return content, nil
}

// runHealthcheck is the image HEALTHCHECK: the slim runtime has no curl.
func runHealthcheck(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("healthcheck does not accept arguments")
	}
	address := environmentDefault("MITHRA_ADDR", "127.0.0.1:8090")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("MITHRA_ADDR is invalid")
	}
	client := http.Client{Timeout: healthcheckTimeout}
	response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
	if err != nil {
		return errors.New("Mithra is not answering")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Mithra is not ready: HTTP %d", response.StatusCode)
	}
	fmt.Fprintln(output, "status=ready")
	return nil
}

// runGenerateMasterKey prints one new base64url master key for a credential
// file mount. It never writes the key anywhere itself.
func runGenerateMasterKey(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("generate-master-key does not accept arguments")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return errors.New("generate master key")
	}
	defer clear(key)
	fmt.Fprintln(output, base64.RawURLEncoding.EncodeToString(key))
	return nil
}

// dataConfig is the storage configuration shared by serve and the maintenance
// commands, read from the same environment.
type dataConfig struct {
	databasePath, sourceRoot, backupDir, lockFile string
}

func loadDataConfig() (dataConfig, error) {
	config := dataConfig{
		databasePath: environmentDefault("MITHRA_DB", "data/mithra.sqlite3"),
		sourceRoot:   environmentDefault("MITHRA_SOURCE_DIR", ""),
		backupDir:    environmentDefault("MITHRA_BACKUP_DIR", ""),
		lockFile:     environmentDefault("MITHRA_LOCK_FILE", ""),
	}
	databasePath, err := filepath.Abs(config.databasePath)
	if err != nil {
		return config, errors.New("database path is invalid")
	}
	config.databasePath = databasePath
	if config.sourceRoot == "" {
		config.sourceRoot = filepath.Join(filepath.Dir(databasePath), "sources")
	}
	if config.sourceRoot, err = filepath.Abs(config.sourceRoot); err != nil {
		return config, errors.New("source directory is invalid")
	}
	return config, nil
}

func (c dataConfig) paths() installer.Paths {
	return installer.Paths{
		Data:     filepath.Dir(c.databasePath),
		Database: c.databasePath,
		Sources:  c.sourceRoot,
		Journal:  filepath.Join(filepath.Dir(c.sourceRoot), "deletion.journal"),
		Backups:  c.backupDir,
	}
}

func loadMasterKey() ([]byte, error) {
	credential, err := readCredentialFile(environmentDefault("MITHRA_MASTER_KEY_FILE", ""))
	if err != nil {
		return nil, err
	}
	return decodeMasterKey(credential)
}

// databaseKeyFor applies the at-rest policy: container mode always encrypts
// and refuses a binary without SQLCipher or an unencrypted database; other
// modes encrypt new databases whenever the binary supports it and keep opening
// an existing plain SQLite install.
func databaseKeyFor(path string, masterKey []byte, requireEncryption bool) ([]byte, error) {
	if requireEncryption {
		if !database.EncryptionAvailable() {
			return nil, failStartup("database_encryption", database.ErrEncryptionUnavailable)
		}
		plaintext, err := database.IsPlaintext(path)
		if err != nil {
			return nil, failStartup("database", errors.New("inspect database"))
		}
		if plaintext {
			return nil, failStartup("database_plaintext", database.ErrPlaintextDatabase)
		}
	}
	key, err := database.KeyForFile(path, masterKey)
	if err != nil {
		return nil, failStartup("database", errors.New("derive database key"))
	}
	return key, nil
}

// dataLock is held by serve for its whole life, so a maintenance command that
// must not run against live data refuses instead of racing it.
type dataLock struct{ file *os.File }

func acquireDataLock(path string) (*dataLock, error) {
	if strings.TrimSpace(path) == "" {
		return &dataLock{}, nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("open Mithra data lock")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("Mithra is running; stop the app service before this command")
	}
	return &dataLock{file: file}, nil
}

func (l *dataLock) Release() {
	if l != nil && l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
		l.file = nil
	}
}

// runStatus reports non-secret facts about the instance. It reads the
// database read-only, so it is safe beside a running server.
func runStatus(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("status does not accept arguments")
	}
	config, err := loadDataConfig()
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "version=%s\n", buildVersion)
	fmt.Fprintf(output, "proxy_mode=%s\n", environmentDefault("MITHRA_PROXY_MODE", "app-only"))
	fmt.Fprintf(output, "database_encryption_available=%t\n", database.EncryptionAvailable())
	masterKey, keyErr := loadMasterKey()
	fmt.Fprintf(output, "master_key=%s\n", presence(keyErr == nil))
	switch plaintext, err := database.IsPlaintext(config.databasePath); {
	case err != nil:
		fmt.Fprintln(output, "database=unreadable")
	case !fileExists(config.databasePath):
		fmt.Fprintln(output, "database=absent")
	case plaintext:
		fmt.Fprintln(output, "database=plaintext")
	default:
		fmt.Fprintln(output, "database=encrypted")
	}
	if fileExists(config.databasePath) && keyErr == nil {
		if err := installer.DatabasePreflightWithKey(context.Background(), config.databasePath, masterKey); err != nil {
			fmt.Fprintln(output, "database_check=failed")
		} else {
			fmt.Fprintln(output, "database_check=ok")
		}
	}
	if config.backupDir != "" {
		latest := installer.LatestBackup(config.backupDir)
		if latest == "" {
			fmt.Fprintln(output, "latest_backup=none")
		} else if info, err := os.Stat(latest); err == nil {
			fmt.Fprintf(output, "latest_backup=%s age=%s\n", filepath.Base(latest), time.Since(info.ModTime()).Round(time.Minute))
		}
	}
	socketPath := environmentDefault("MITHRA_PDF_PARSER_SOCKET", defaultPDFParserSocket)
	if socketPath != "local" {
		connection, err := net.DialTimeout("unix", socketPath, parserProbeTimeout)
		if err == nil {
			_ = connection.Close()
		}
		fmt.Fprintf(output, "pdf_parser=%s\n", map[bool]string{true: "reachable", false: "unreachable"}[err == nil])
	}
	clear(masterKey)
	return nil
}

func presence(ok bool) string {
	if ok {
		return "present"
	}
	return "missing"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// runOfflineBackup creates a backup while the server is stopped. A running
// server takes the same backup on SIGUSR1 (`docker compose kill -s SIGUSR1`).
func runOfflineBackup(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("backup does not accept arguments; signal a running server with SIGUSR1 instead")
	}
	config, err := loadDataConfig()
	if err != nil {
		return err
	}
	if config.backupDir == "" {
		return errors.New("MITHRA_BACKUP_DIR is required")
	}
	lock, err := acquireDataLock(config.lockFile)
	if err != nil {
		return err
	}
	defer lock.Release()
	masterKey, err := loadMasterKey()
	if err != nil {
		return err
	}
	defer clear(masterKey)
	archive, err := installer.CreateBackup(context.Background(), config.paths(), masterKey, time.Now())
	if err != nil {
		return errors.New("create backup")
	}
	if err := installer.VerifyBackupArchive(archive, masterKey); err != nil {
		return errors.New("verify backup")
	}
	fmt.Fprintf(output, "backup=%s verified=true\n", filepath.Base(archive))
	return nil
}

func runVerifyBackup(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mithra verify-backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archive := flags.String("archive", "", "backup archive to authenticate")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *archive == "" {
		return errors.New("verify-backup requires --archive")
	}
	masterKey, err := loadMasterKey()
	if err != nil {
		return err
	}
	defer clear(masterKey)
	if err := installer.VerifyBackupArchive(*archive, masterKey); err != nil {
		return errors.New("backup does not verify with this master key")
	}
	fmt.Fprintln(output, "verified=true")
	return nil
}

// runRestore replaces the data generation with an authenticated archive. Like
// the installer's restore it clears passwords, sessions and provider keys.
func runRestore(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mithra restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	archive := flags.String("archive", "", "backup archive to restore")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *archive == "" {
		return errors.New("restore requires --archive")
	}
	config, err := loadDataConfig()
	if err != nil {
		return err
	}
	allowedEmails, err := parseAllowedEmails(environmentDefault("ALLOWED_EMAILS", ""))
	if err != nil {
		return err
	}
	lock, err := acquireDataLock(config.lockFile)
	if err != nil {
		return err
	}
	defer lock.Release()
	masterKey, err := loadMasterKey()
	if err != nil {
		return err
	}
	defer clear(masterKey)
	ctx := context.Background()
	if err := installer.RestoreBackup(ctx, config.paths(), *archive, masterKey, allowedEmails, func() error {
		return installer.DatabasePreflightWithKey(ctx, config.databasePath, masterKey)
	}); err != nil {
		return fmt.Errorf("restore failed; current data kept: %w", err)
	}
	fmt.Fprintln(output, "restored=true")
	return nil
}

// runResetDemo replaces the marked synthetic household while the server is
// stopped. Passwords are read from private files, as with the installer.
func runResetDemo(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mithra reset-demo", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	ownerEmail := flags.String("owner-email", "", "allowlisted demo owner email")
	partnerEmail := flags.String("partner-email", "", "allowlisted demo partner email")
	ownerPasswordFile := flags.String("owner-password-file", "", "optional private owner password file")
	partnerPasswordFile := flags.String("partner-password-file", "", "optional private partner password file")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *ownerEmail == "" || *partnerEmail == "" {
		return errors.New("reset-demo requires --owner-email and --partner-email")
	}
	if (*ownerPasswordFile == "") != (*partnerPasswordFile == "") {
		return errors.New("reset-demo needs both password files or neither")
	}
	config, err := loadDataConfig()
	if err != nil {
		return err
	}
	if config.backupDir == "" {
		return errors.New("MITHRA_BACKUP_DIR is required")
	}
	allowedEmails, err := parseAllowedEmails(environmentDefault("ALLOWED_EMAILS", ""))
	if err != nil {
		return err
	}
	if !containsEmail(allowedEmails, *ownerEmail) || !containsEmail(allowedEmails, *partnerEmail) {
		return errors.New("both demo emails must be in ALLOWED_EMAILS")
	}
	lock, err := acquireDataLock(config.lockFile)
	if err != nil {
		return err
	}
	defer lock.Release()
	masterKey, err := loadMasterKey()
	if err != nil {
		return err
	}
	defer clear(masterKey)
	var ownerPassword, partnerPassword []byte
	if *ownerPasswordFile != "" {
		owner, err := readCredentialFile(*ownerPasswordFile)
		if err != nil {
			return errors.New("owner password file is invalid")
		}
		partner, err := readCredentialFile(*partnerPasswordFile)
		if err != nil {
			return errors.New("partner password file is invalid")
		}
		ownerPassword, partnerPassword = []byte(owner), []byte(partner)
	}
	receipt, err := demo.Reset(context.Background(), demo.Config{
		DatabasePath: config.databasePath, SourceRoot: config.sourceRoot, BackupRoot: config.backupDir,
		OwnerEmail: *ownerEmail, PartnerEmail: *partnerEmail,
		OwnerPassword: ownerPassword, PartnerPassword: partnerPassword,
		MasterKey: masterKey,
	})
	if err != nil {
		return errors.New("demo reset failed; previous data kept")
	}
	fmt.Fprintf(output, "household=%s fixture=%s shared=%d backup=%s\n", receipt.HouseholdID, receipt.FixtureVersion, receipt.SharedRecords, filepath.Base(receipt.BackupArchive))
	return nil
}

func containsEmail(emails []string, raw string) bool {
	wanted, err := parseAllowedEmails(raw)
	if err != nil || len(wanted) != 1 {
		return false
	}
	for _, email := range emails {
		if email == wanted[0] {
			return true
		}
	}
	return false
}

// listenContainer binds the explicit host:port behind the platform proxy.
func listenContainer(address, socketPath string) (net.Listener, error) {
	if strings.TrimSpace(socketPath) != "" {
		return nil, errors.New("container mode listens on MITHRA_ADDR, not a Unix socket")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return nil, errors.New("container mode requires MITHRA_ADDR as host:port")
	}
	if host != "" && net.ParseIP(host) == nil {
		return nil, errors.New("container mode requires a literal IP listen address")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", address, err)
	}
	return listener, nil
}

// parseTrustedProxies accepts only private or loopback networks: the platform
// proxy reaches Mithra over a private Docker network, never a public one.
func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var networks []netip.Prefix
	for _, value := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		network, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return nil, errors.New("trusted proxy network is invalid")
		}
		network = network.Masked()
		if !network.Addr().IsPrivate() && !network.Addr().IsLoopback() {
			return nil, errors.New("trusted proxy networks must be private")
		}
		if (network.Addr().Is4() && network.Bits() < 8) || (network.Addr().Is6() && network.Bits() < 7) {
			return nil, errors.New("trusted proxy network is too broad")
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return nil, errors.New("container mode requires MITHRA_TRUSTED_PROXIES")
	}
	return networks, nil
}

// backupSchedule runs one verified backup a day at a fixed UTC time, catches
// up shortly after start when the newest archive is over a day old (like
// systemd's Persistent=true), and on SIGUSR1.
type backupSchedule struct {
	application *app.App
	directory   string
	hour, min   int
	now         func() time.Time
	logger      *log.Logger
}

func parseBackupTime(raw string) (hour, minute int, enabled bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultBackupTime
	}
	if strings.EqualFold(raw, "off") {
		return 0, 0, false, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 0, 0, false, errors.New("MITHRA_BACKUP_TIME must be HH:MM in UTC or off")
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, false, errors.New("MITHRA_BACKUP_TIME must be HH:MM in UTC or off")
	}
	return hour, minute, true, nil
}

func (s backupSchedule) nextRun(after time.Time) time.Time {
	after = after.UTC()
	next := time.Date(after.Year(), after.Month(), after.Day(), s.hour, s.min, 0, 0, time.UTC)
	if !next.After(after) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func (s backupSchedule) catchUpDue(now time.Time) bool {
	latest := installer.LatestBackup(s.directory)
	if latest == "" {
		return true
	}
	info, err := os.Stat(latest)
	return err != nil || now.Sub(info.ModTime()) > backupCatchUpAge
}

// runWith serves manual requests always and the daily schedule when enabled.
func (s backupSchedule) runWith(ctx context.Context, manual <-chan os.Signal, scheduled bool) {
	var tick <-chan time.Time
	var timer *time.Timer
	if scheduled {
		delay := time.Until(s.nextRun(s.now()))
		if s.catchUpDue(s.now()) {
			delay = backupCatchUpDelay
		}
		timer = time.NewTimer(delay)
		defer timer.Stop()
		tick = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-manual:
			s.backup(ctx, "manual")
		case <-tick:
			s.backup(ctx, "scheduled")
			timer.Reset(time.Until(s.nextRun(s.now())))
		}
	}
}

func (s backupSchedule) backup(ctx context.Context, trigger string) {
	archive, err := s.application.Backup(ctx, s.directory, s.now())
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Printf("error_code=backup_failed trigger=%s", trigger)
		}
		return
	}
	s.logger.Printf("event=backup_verified trigger=%s archive=%s", trigger, filepath.Base(archive))
}
