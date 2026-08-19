package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

func printHelp() {
	fmt.Fprintf(os.Stderr, `NETSCOUT nGenius PAM Deployment Tool

USAGE:
  ng1-deploy [flags]

DESCRIPTION:
  Deploys/updates NetScout nGenius PAM authentication configuration
  across a list of devices defined in config.yaml, and adds or removes
  users from those devices based on the users_add / users_delete lists.

FLAGS:
  --config <path>   Path to config.yaml. Overrides all other lookup
                     locations (env var, current directory, pointer
                     file, fixed fallback paths).
  --user-add         Add users from config's users_add list. (default)
  --user-delete      Delete users from config's users_delete list.
  --help, -h         Show this help message and exit.

CONFIG FILE LOOKUP ORDER (used when --config is not provided):
  1. --config flag value
  2. NG1_CONFIG_PATH environment variable
  3. ./config.yaml (current directory)
  4. ./config.location or <binary-dir>/config.location pointer file
  5. /etc/ng1-deploy/config.yaml
  6. /opt/ng1-deploy/config.yaml

CREDENTIALS:
  SSH username/password are prompted interactively by default.
  You can skip prompts by setting:
    NG1_SSH_USER=<username>
    NG1_SSH_PASSWORD=<password>

EXAMPLES:
  ng1-deploy
  ng1-deploy --user-delete
  ng1-deploy --config ./configs/prod.yaml --user-add
  NG1_SSH_USER=root NG1_SSH_PASSWORD=secret ng1-deploy --user-add

`)
}


func main() {
	
	flag.Usage = printHelp
	configPath := flag.String("config", "", "path to config.yaml (overrides all other lookup locations)")
	userAdd := flag.Bool("user-add", false, "add users from config's users_add list (default mode)")
	userDelete := flag.Bool("user-delete", false, "delete users from config's users_delete list")
	flag.Parse()

	if *userAdd && *userDelete {
		fmt.Fprintln(os.Stderr, "error: --user-add and --user-delete are mutually exclusive")
		os.Exit(1)
	}

	mode := ModeAdd
	if *userDelete {
		mode = ModeDelete
	}

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded config from: %s\n", cfg.loadedFrom)

	users := cfg.UsersAdd
	if mode == ModeDelete {
		users = cfg.UsersDelete
	}
	if len(users) == 0 {
		fmt.Fprintf(os.Stderr, "no users configured for mode %q; nothing to do\n", mode)
		os.Exit(1)
	}

	// --- Credentials: always prompt unless overridden by env vars ---
	username, password, err := resolveCredentials()
	if err != nil {
		fmt.Fprintln(os.Stderr, "credential error:", err)
		os.Exit(1)
	}

	logger, err := NewLogger(cfg.Logging.LogDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logger error:", err)
		os.Exit(1)
	}
	defer logger.Close()

	printBanner(mode, len(cfg.Devices), users)

	logger.Summary("===== DEPLOYMENT STARTED (mode=%s, user=%s) =====", mode, username)

	report := NewReport(mode)

	for i, host := range cfg.Devices {
		fmt.Printf("\n[%d/%d] [%s] Connecting to %s ...\n",
			i+1, len(cfg.Devices), time.Now().Format("15:04:05"), host)

		hl, err := NewHostLogger(cfg.Logging.LogDir, host)
		if err != nil {
			logger.Error(host, "failed to create host log: %v", err)
			logger.Summary("%s FAILED (log creation error)", host)
			report.AddResult(HostResult{Host: host, Mode: mode, Err: err, StartTime: time.Now(), EndTime: time.Now()})
			fmt.Printf("    -> ERROR: %v\n", err)
			continue
		}

		start := time.Now()
		client, err := NewSSHClient(host, username, password, cfg)
		if err != nil {
			logger.Error(host, "connection failed: %v", err)
			logger.Summary("%s FAILED (connection error)", host)
			hl.Close()
			report.AddResult(HostResult{Host: host, Mode: mode, Err: err, StartTime: start, EndTime: time.Now()})
			fmt.Printf("    -> ERROR: connection failed: %v\n", err)
			continue
		}

		fmt.Printf("    -> connected, applying configuration and %s users ...\n", mode)

		result := configureHost(client, hl, host, cfg, mode, users)

		client.Close()
		hl.Close()

		report.AddResult(result)

		if result.Err != nil {
			logger.Error(host, "%v", result.Err)
			logger.Summary("%s FAILED: %v", host, result.Err)
			fmt.Printf("    -> ERROR: %v\n", result.Err)
			continue
		}

		if result.Success {
			logger.Summary("%s SUCCESS", host)
			fmt.Printf("    -> SUCCESS (%s)\n", result.Duration().Round(time.Millisecond*10))
		} else {
			logger.Summary("%s VALIDATION FAILED", host)
			fmt.Printf("    -> VALIDATION FAILED (%s)\n", result.Duration().Round(time.Millisecond*10))
		}
	}

	report.Finish()
	logger.Summary("===== DEPLOYMENT COMPLETE =====")

	// --- Print + save final report ---
	rendered := report.Render()
	fmt.Println("\n" + rendered)

	reportPath, err := report.WriteToFile(cfg.Logging.LogDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: failed to write report file:", err)
	} else {
		fmt.Printf("Full report also saved to: %s\n", reportPath)
	}

	_, failed, errored := report.Counts()
	if failed > 0 || errored > 0 {
		os.Exit(1)
	}
}

func printBanner(mode Mode, hostCount int, users []string) {
	fmt.Println(strings.Repeat("=", 70))
	fmt.Println(" NETSCOUT nGenius PAM Deployment Tool")
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("Mode          : %s\n", strings.ToUpper(string(mode)))
	fmt.Printf("Target Hosts  : %d\n", hostCount)
	fmt.Printf("Target Users  : %s\n", strings.Join(users, ", "))
	fmt.Println(strings.Repeat("=", 70))
}

// resolveCredentials prompts for username/password unless env vars are set.
func resolveCredentials() (string, string, error) {
	username := os.Getenv("NG1_SSH_USER")
	password := os.Getenv("NG1_SSH_PASSWORD")

	if username == "" {
		u, err := promptText("Enter SSH username: ")
		if err != nil {
			return "", "", err
		}
		username = strings.TrimSpace(u)
	}

	if username == "" {
		return "", "", fmt.Errorf("username cannot be empty")
	}

	if password == "" {
		p, err := promptPassword(fmt.Sprintf("Enter SSH password for %s: ", username))
		if err != nil {
			return "", "", err
		}
		password = p
	}

	if password == "" {
		return "", "", fmt.Errorf("password cannot be empty")
	}

	return username, password, nil
}

func promptText(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return line, nil
}

func promptPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	bytePw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(bytePw), nil
}
