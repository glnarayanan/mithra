package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/glnarayanan/mithra/internal/installer"
)

// commandSpec is deliberately small: it is the single public command registry
// for dispatch, help, and generated completions.
type commandSpec struct {
	name, summary string
	operation     installer.Operation
	flags         []string
}

var commandSpecs = []commandSpec{
	{"install", "install Mithra", installer.Install, []string{"--domain", "--proxy", "--port", "--allowed-emails", "--plunk-key-file", "--plunk-from"}},
	{"upgrade", "install a verified newer release", installer.Upgrade, []string{"--domain", "--proxy", "--port", "--allowed-emails"}},
	{"reconfigure", "change Mithra configuration and Plunk credentials", installer.Reconfigure, []string{"--domain", "--proxy", "--port", "--allowed-emails", "--plunk-key-file", "--plunk-from"}},
	{"backup", "create an encrypted backup", installer.Backup, nil},
	{"restore", "restore an authenticated backup", installer.Restore, []string{"--domain", "--proxy", "--port", "--allowed-emails", "--archive"}},
	{"status", "report Mithra status", installer.Status, []string{"--proxy", "--port"}},
	{"reset-demo", "replace judge demo data (advanced)", "reset-demo", []string{"--owner-email", "--partner-email", "--owner-password-file", "--partner-password-file", "--master-key-file"}},
	{"uninstall", "remove Mithra runtime and preserve recovery data", installer.Uninstall, []string{"--proxy"}},
	{"purge", "remove retained Mithra recovery data", installer.Purge, []string{"--proxy", "--confirm-purge"}},
}

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

type installerFlags struct {
	root, domain, proxy, emails, archive, artifact, candidateInstaller, manifest, signature, releaseVersion, plunkPath, plunkFrom, ownerEmail, partnerEmail, ownerPasswordPath, partnerPasswordPath, masterKeyPath *string
	port                                                                                                                                                                                                           *int
	confirm                                                                                                                                                                                                        *bool
	help                                                                                                                                                                                                           *bool
}

type parsedInstallerCommand struct {
	name      string
	operation installer.Operation
	planOnly  bool
	flags     installerFlags
}

func parseInstallerCommand(args []string) (parsedInstallerCommand, error) {
	if len(args) == 0 {
		return parsedInstallerCommand{}, usageError{errors.New("operation required: help, plan, install, upgrade, reconfigure, backup, restore, status, reset-demo, uninstall, or purge")}
	}
	name := args[0]
	if name == "-h" || name == "--help" || name == "help" || name == "version" || name == "completion" || name == "verify-backup" {
		return parsedInstallerCommand{name: name}, nil
	}
	planOnly := name == "plan"
	operationName := name
	remaining := args[1:]
	if planOnly {
		operationName = "install"
		if len(remaining) > 0 && !strings.HasPrefix(remaining[0], "-") {
			operationName, remaining = remaining[0], remaining[1:]
		}
		if len(remaining) > 0 && (remaining[0] == "--help" || remaining[0] == "-h") {
			return parsedInstallerCommand{name: "help-plan"}, nil
		}
		if operationName != "install" && operationName != "upgrade" && operationName != "reconfigure" && operationName != "backup" && operationName != "restore" && operationName != "uninstall" && operationName != "purge" {
			return parsedInstallerCommand{}, usageError{fmt.Errorf("plan does not support operation %q", operationName)}
		}
	}
	spec, ok := installerCommand(operationName)
	if !ok {
		return parsedInstallerCommand{}, usageError{fmt.Errorf("unknown command %q", operationName)}
	}
	flags, set := commandFlagSet(spec)
	if err := set.Parse(remaining); err != nil {
		if errors.Is(err, flag.ErrHelp) || (flags.help != nil && *flags.help) {
			return parsedInstallerCommand{name: "help-" + spec.name}, nil
		}
		return parsedInstallerCommand{}, usageError{err}
	}
	if flags.help != nil && *flags.help {
		return parsedInstallerCommand{name: "help-" + spec.name}, nil
	}
	if set.NArg() != 0 {
		return parsedInstallerCommand{}, usageError{errors.New("unexpected positional arguments")}
	}
	if err := validateRoot(*flags.root); err != nil {
		return parsedInstallerCommand{}, usageError{err}
	}
	return parsedInstallerCommand{name: spec.name, operation: spec.operation, planOnly: planOnly, flags: flags}, nil
}

func commandFlagSet(spec commandSpec) (installerFlags, *flag.FlagSet) {
	set := flag.NewFlagSet("mithra-installer "+spec.name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	f := installerFlags{
		domain: new(string), proxy: new(string), emails: new(string), archive: new(string), artifact: new(string), candidateInstaller: new(string), manifest: new(string), signature: new(string), releaseVersion: new(string), plunkPath: new(string), plunkFrom: new(string), ownerEmail: new(string), partnerEmail: new(string), ownerPasswordPath: new(string), partnerPasswordPath: new(string), masterKeyPath: new(string), port: new(int), confirm: new(bool),
	}
	*f.port = 8090
	f.root = set.String("root", "/", "")
	f.help = set.Bool("help", false, "")
	set.BoolVar(f.help, "h", false, "")
	for _, name := range spec.flags {
		switch name {
		case "--domain":
			f.domain = set.String("domain", "", "")
		case "--proxy":
			f.proxy = set.String("proxy", "", "")
		case "--port":
			f.port = set.Int("port", 8090, "")
		case "--allowed-emails":
			f.emails = set.String("allowed-emails", "", "")
		case "--archive":
			f.archive = set.String("archive", "", "")
		case "--confirm-purge":
			f.confirm = set.Bool("confirm-purge", false, "")
		case "--plunk-key-file":
			f.plunkPath = set.String("plunk-key-file", "", "")
		case "--plunk-from":
			f.plunkFrom = set.String("plunk-from", "", "")
		case "--owner-email":
			f.ownerEmail = set.String("owner-email", "", "")
		case "--partner-email":
			f.partnerEmail = set.String("partner-email", "", "")
		case "--owner-password-file":
			f.ownerPasswordPath = set.String("owner-password-file", "", "")
		case "--partner-password-file":
			f.partnerPasswordPath = set.String("partner-password-file", "", "")
		case "--master-key-file":
			f.masterKeyPath = set.String("master-key-file", "", "")
		}
	}
	// These flags intentionally remain hidden but retain their release-script
	// compatibility on release activation commands only.
	if spec.operation == installer.Install || spec.operation == installer.Upgrade || spec.operation == installer.Reconfigure {
		f.artifact = set.String("artifact", "", "")
		f.candidateInstaller = set.String("candidate-installer", "", "")
		f.manifest = set.String("manifest", "", "")
		f.signature = set.String("signature", "", "")
		f.releaseVersion = set.String("release-version", "", "")
	}
	return f, set
}

func installerCommand(name string) (commandSpec, bool) {
	for _, spec := range commandSpecs {
		if spec.name == name {
			return spec, true
		}
	}
	return commandSpec{}, false
}

func validateRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("--root must be an absolute clean directory")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("--root must name an existing non-symlink directory")
	}
	return nil
}

func installerHelp(command string, out io.Writer) error {
	if command != "" {
		if command == "help" {
			fmt.Fprintln(out, "Usage: mithra-installer help [COMMAND]\n\nShow help for a public Mithra installer command.")
			return nil
		}
		if command == "plan" {
			fmt.Fprintln(out, "Usage: mithra-installer plan [install|upgrade|reconfigure|backup|restore|uninstall|purge] [OPTIONS]\n\nPrint a lifecycle plan without changing the server. Bare plan means install.")
			return nil
		}
		if command == "verify-backup" {
			fmt.Fprintln(out, "Usage: mithra-installer verify-backup --archive PATH\n\nAuthenticate an encrypted backup without changing the service.")
			return nil
		}
		if command == "completion" {
			fmt.Fprintln(out, "Usage: mithra-installer completion bash|zsh|fish\n\nPrint deterministic shell completion to standard output.")
			return nil
		}
		if command == "version" {
			fmt.Fprintln(out, "Usage: mithra-installer version\n\nPrint the installed build version.")
			return nil
		}
		spec, ok := installerCommand(command)
		if !ok {
			return usageError{fmt.Errorf("unknown command %q", command)}
		}
		fmt.Fprintf(out, "Usage: mithra-installer %s [OPTIONS]\n\n%s\n", spec.name, spec.summary)
		if len(spec.flags) > 0 {
			fmt.Fprintf(out, "\nOptions: %s\n", strings.Join(spec.flags, " "))
		}
		return nil
	}
	fmt.Fprintln(out, "Usage: mithra-installer COMMAND [OPTIONS]")
	fmt.Fprintln(out, "\nCommands:")
	for _, spec := range commandSpecs {
		fmt.Fprintf(out, "  %-14s %s\n", spec.name, spec.summary)
	}
	fmt.Fprintln(out, "  plan [OPERATION]  print a lifecycle plan without changing the server")
	fmt.Fprintln(out, "  verify-backup     authenticate an archive without changing the server")
	fmt.Fprintln(out, "  completion        print shell completion")
	fmt.Fprintln(out, "  help [COMMAND]   show command help")
	fmt.Fprintln(out, "  version          print the Mithra installer version")
	return nil
}

func flagTakesValue(option string) bool { return option != "--confirm-purge" }

func publicCommands() []string {
	commands := []string{"help", "version", "plan", "verify-backup", "completion"}
	for _, spec := range commandSpecs {
		commands = append(commands, spec.name)
	}
	sort.Strings(commands)
	return commands
}

func completion(shell string, out io.Writer) error {
	commands := strings.Join(publicCommands(), " ")
	switch shell {
	case "bash":
		fmt.Fprintf(out, "# bash completion for mithra-installer\n_mithra_installer() {\n  local cur=${COMP_WORDS[COMP_CWORD]}\n  local cmd=${COMP_WORDS[1]}\n  local op=${COMP_WORDS[2]}\n  case $cmd in\n")
		for _, spec := range commandSpecs {
			fmt.Fprintf(out, "    %s) COMPREPLY=( $(compgen -W '%s --help -h' -- \"$cur\") ) ;;\n", spec.name, strings.Join(spec.flags, " "))
		}
		fmt.Fprint(out, "    plan)\n      if [[ $COMP_CWORD -eq 2 ]]; then COMPREPLY=( $(compgen -W 'install upgrade reconfigure backup restore uninstall purge' -- \"$cur\") ); return; fi\n      case $op in\n")
		for _, spec := range commandSpecs {
			if spec.operation == installer.Status || spec.operation == "reset-demo" {
				continue
			}
			fmt.Fprintf(out, "        %s) COMPREPLY=( $(compgen -W '%s --help -h' -- \"$cur\") ) ;;\n", spec.name, strings.Join(spec.flags, " "))
		}
		fmt.Fprint(out, "      esac ;;\n")
		fmt.Fprintf(out, "    *) COMPREPLY=( $(compgen -W '%s' -- \"$cur\") ) ;;\n  esac\n}\ncomplete -F _mithra_installer mithra-installer\n", commands)
	case "zsh":
		fmt.Fprint(out, "#compdef mithra-installer\n_mithra_installer() {\n  local -a commands\n  commands=(")
		fmt.Fprintf(out, "%s)\n  case $words[2] in\n", commands)
		for _, spec := range commandSpecs {
			fmt.Fprintf(out, "    %s) _arguments", spec.name)
			for _, option := range spec.flags {
				if flagTakesValue(option) {
					fmt.Fprintf(out, " '%s=[option]:value:'", option)
				} else {
					fmt.Fprintf(out, " '%s[option]'", option)
				}
			}
			fmt.Fprintln(out, " ;;")
		}
		fmt.Fprint(out, "    plan)\n      case $words[3] in\n")
		for _, spec := range commandSpecs {
			if spec.operation == installer.Status || spec.operation == "reset-demo" {
				continue
			}
			fmt.Fprintf(out, "        %s) _arguments", spec.name)
			for _, option := range spec.flags {
				if flagTakesValue(option) {
					fmt.Fprintf(out, " '%s=[option]:value:'", option)
				} else {
					fmt.Fprintf(out, " '%s[option]'", option)
				}
			}
			fmt.Fprintln(out, " ;;")
		}
		fmt.Fprint(out, "        *) _arguments '1:operation:(install upgrade reconfigure backup restore uninstall purge)' ;;\n      esac ;;\n    completion) _arguments '1:shell:(bash zsh fish)' ;;\n    *) _describe 'command' commands ;;\n  esac\n}\ncompdef _mithra_installer mithra-installer\n")
	case "fish":
		for _, command := range publicCommands() {
			fmt.Fprintf(out, "complete -c mithra-installer -f -a %s\n", command)
		}
		fmt.Fprintln(out, "complete -c mithra-installer -n '__fish_seen_subcommand_from plan' -a 'install upgrade reconfigure backup restore uninstall purge'")
		for _, spec := range commandSpecs {
			for _, option := range spec.flags {
				needs := ""
				if flagTakesValue(option) {
					needs = " -r"
				}
				fmt.Fprintf(out, "complete -c mithra-installer -n '__fish_seen_subcommand_from %s' -l %s%s\n", spec.name, strings.TrimPrefix(option, "--"), needs)
				if spec.operation != installer.Status && spec.operation != "reset-demo" {
					fmt.Fprintf(out, "complete -c mithra-installer -n '__fish_seen_subcommand_from plan; and __fish_seen_subcommand_from %s' -l %s%s\n", spec.name, strings.TrimPrefix(option, "--"), needs)
				}
			}
		}
	default:
		return usageError{errors.New("completion requires bash, zsh, or fish")}
	}
	return nil
}

func runVerifyBackup(args []string, out io.Writer) error {
	set := flag.NewFlagSet("mithra-installer verify-backup", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	root := set.String("root", "/", "")
	archive := set.String("archive", "", "")
	if err := set.Parse(args); err != nil {
		return usageError{err}
	}
	if set.NArg() != 0 || strings.TrimSpace(*archive) == "" {
		return usageError{errors.New("verify-backup requires --archive PATH")}
	}
	if err := validateRoot(*root); err != nil {
		return usageError{err}
	}
	key, err := readMasterKey(installer.OwnedPaths(*root, installer.AppOnly).MasterKey)
	if err != nil {
		return err
	}
	defer clear(key)
	if err := installer.VerifyBackupArchive(*archive, key); err != nil {
		return errors.New("backup archive could not be authenticated")
	}
	return json.NewEncoder(out).Encode(struct {
		Archive  string `json:"archive"`
		Verified bool   `json:"verified"`
	}{Archive: filepath.Base(*archive), Verified: true})
}
