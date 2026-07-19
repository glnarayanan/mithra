package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/glnarayanan/mithra/internal/installer"
)

const officialReleaseBase = "https://github.com/glnarayanan/mithra/releases"

type automaticUpgradeStage struct {
	Version, Application, Candidate, Manifest, Signature string
	directory                                            string
}

func (stage automaticUpgradeStage) cleanup() { _ = os.RemoveAll(stage.directory) }

func prepareAutomaticUpgrade(ctx context.Context, client *http.Client, releaseBase, arch, keyValue string) (automaticUpgradeStage, error) {
	base, err := url.Parse(strings.TrimRight(releaseBase, "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return automaticUpgradeStage{}, errors.New("release source must use HTTPS")
	}
	key, err := installer.DecodePublisherKey(keyValue)
	if err != nil {
		return automaticUpgradeStage{}, errors.New("this installer was not built with the pinned publisher key")
	}
	manifestRaw, err := fetchReleaseFile(ctx, client, base.String()+"/latest/download/RELEASE-MANIFEST", 128<<10)
	if err != nil {
		return automaticUpgradeStage{}, err
	}
	signature, err := fetchReleaseFile(ctx, client, base.String()+"/latest/download/RELEASE-MANIFEST.sig", 1<<10)
	if err != nil {
		return automaticUpgradeStage{}, err
	}
	manifest, err := installer.ParseManifest(manifestRaw)
	if err != nil {
		return automaticUpgradeStage{}, err
	}
	if err := installer.VerifyReleaseVersion(manifest, "", ""); err != nil {
		return automaticUpgradeStage{}, err
	}
	applicationName := "mithra-linux-" + arch
	candidateName := "mithra-installer-linux-" + arch
	downloadBase := base.String() + "/download/" + manifest.Version
	application, err := fetchReleaseFile(ctx, client, downloadBase+"/"+applicationName, 256<<20)
	if err != nil {
		return automaticUpgradeStage{}, err
	}
	candidate, err := fetchReleaseFile(ctx, client, downloadBase+"/"+candidateName, 256<<20)
	if err != nil {
		return automaticUpgradeStage{}, err
	}
	if _, err := installer.VerifyRelease(manifestRaw, signature, key, applicationName, application); err != nil {
		return automaticUpgradeStage{}, err
	}
	if _, err := installer.VerifyRelease(manifestRaw, signature, key, candidateName, candidate); err != nil {
		return automaticUpgradeStage{}, err
	}
	directory, err := os.MkdirTemp("", "mithra-upgrade-")
	if err != nil {
		return automaticUpgradeStage{}, err
	}
	stage := automaticUpgradeStage{
		Version: manifest.Version, Application: filepath.Join(directory, applicationName), Candidate: filepath.Join(directory, candidateName),
		Manifest: filepath.Join(directory, "RELEASE-MANIFEST"), Signature: filepath.Join(directory, "RELEASE-MANIFEST.sig"), directory: directory,
	}
	for path, file := range map[string]struct {
		content []byte
		mode    os.FileMode
	}{
		stage.Application: {application, 0o600},
		stage.Candidate:   {candidate, 0o700},
		stage.Manifest:    {manifestRaw, 0o600},
		stage.Signature:   {signature, 0o600},
	} {
		if err := os.WriteFile(path, file.content, file.mode); err != nil {
			stage.cleanup()
			return automaticUpgradeStage{}, err
		}
	}
	return stage, nil
}

func fetchReleaseFile(ctx context.Context, client *http.Client, location string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	safeClient := *client
	previousRedirect := client.CheckRedirect
	safeClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" {
			return errors.New("release redirect must use HTTPS")
		}
		if previousRedirect != nil {
			return previousRedirect(request, via)
		}
		if len(via) >= 10 {
			return errors.New("too many release redirects")
		}
		return nil
	}
	response, err := safeClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download release asset: %w", err)
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme != "https" || response.StatusCode != http.StatusOK || response.ContentLength > limit {
		return nil, errors.New("release asset download failed")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(content)) > limit {
		return nil, errors.New("release asset download failed")
	}
	return content, nil
}

func runAutomaticUpgrade(ctx context.Context, parsed parsedInstallerCommand, output io.Writer) error {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return errors.New("Mithra supports Linux amd64 and arm64 only")
	}
	stage, err := prepareAutomaticUpgrade(ctx, http.DefaultClient, officialReleaseBase, runtime.GOARCH, publisherKeyBase64)
	if err != nil {
		return err
	}
	defer stage.cleanup()
	args := []string{"upgrade", "--artifact", stage.Application, "--candidate-installer", stage.Candidate, "--manifest", stage.Manifest, "--signature", stage.Signature, "--release-version", stage.Version}
	f := parsed.flags
	if *f.root != "/" {
		args = append(args, "--root", *f.root)
	}
	for _, flag := range [][2]string{{"--domain", *f.domain}, {"--proxy", *f.proxy}, {"--allowed-emails", *f.emails}} {
		if flag[1] != "" {
			args = append(args, flag[0], flag[1])
		}
	}
	if *f.port != 8090 {
		args = append(args, "--port", fmt.Sprint(*f.port))
	}
	command := exec.CommandContext(ctx, stage.Candidate, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, output, os.Stderr
	return command.Run()
}
