package main

import (
	"fmt"
	"strings"
	"time"
)

type Mode string

const (
	ModeAdd    Mode = "add"
	ModeDelete Mode = "delete"
)

type ValidationResult struct {
	Name   string
	Passed bool
	Detail string
}

type UserOpResult struct {
	User    string
	Mode    Mode
	Message string
	Changed bool // true if an actual add/delete happened, false if it was a no-op
	Err     error
}

type HostResult struct {
	Host        string
	Mode        Mode
	StartTime   time.Time
	EndTime     time.Time
	Success     bool
	Validations []ValidationResult
	UserResults []UserOpResult
	Err         error
}

func (r *HostResult) Duration() time.Duration {
	return r.EndTime.Sub(r.StartTime)
}

// runCmd runs a command, logs it, and returns output.
func runCmd(client *SSHClient, hl *HostLogger, cmd string) (string, error) {
	hl.WriteCommand(cmd)
	out, err := client.Run(cmd)
	hl.WriteOutput(out)
	if err != nil {
		return out, err
	}
	return out, nil
}

func backupFile(client *SSHClient, hl *HostLogger, filename string) error {
	const backupDir = "/root/ng1_auth_backup"

	if _, err := runCmd(client, hl, fmt.Sprintf("mkdir -p %s", backupDir)); err != nil {
		return err
	}

	base := filename
	if idx := strings.LastIndex(filename, "/"); idx >= 0 {
		base = filename[idx+1:]
	}

	backupName := fmt.Sprintf("%s/%s.%s.bak", backupDir, base, time.Now().Format("20060102_150405"))

	cmd := fmt.Sprintf("cp -p %s %s 2>/dev/null || true", filename, backupName)
	_, err := runCmd(client, hl, cmd)
	return err
}

// checkUserExists returns true if the user exists on the remote host.
func checkUserExists(client *SSHClient, hl *HostLogger, user string) (bool, error) {
	out, err := runCmd(client, hl, fmt.Sprintf("id %s >/dev/null 2>&1 && echo EXISTS || echo MISSING", user))
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "EXISTS"), nil
}

// addUser adds a user if not present.
func addUser(client *SSHClient, hl *HostLogger, user string) UserOpResult {
	res := UserOpResult{User: user, Mode: ModeAdd}

	exists, err := checkUserExists(client, hl, user)
	if err != nil {
		res.Err = err
		return res
	}
	if exists {
		res.Message = fmt.Sprintf("user %q already exists on device, no action taken", user)
		res.Changed = false
		return res
	}

	if _, err := runCmd(client, hl, fmt.Sprintf("useradd -ou 0 -g 0 %s", user)); err != nil {
		res.Err = err
		return res
	}

	exists, err = checkUserExists(client, hl, user)
	if err != nil {
		res.Err = err
		return res
	}
	if !exists {
		res.Err = fmt.Errorf("attempted to add user %q but it is not present after useradd", user)
		return res
	}

	res.Message = fmt.Sprintf("user %q added to device", user)
	res.Changed = true
	return res
}

// deleteUser removes a user if present.
func deleteUser(client *SSHClient, hl *HostLogger, user string) UserOpResult {
	res := UserOpResult{User: user, Mode: ModeDelete}

	exists, err := checkUserExists(client, hl, user)
	if err != nil {
		res.Err = err
		return res
	}
	if !exists {
		res.Message = fmt.Sprintf("user %q doesn't exist on device, no action taken", user)
		res.Changed = false
		return res
	}

	if _, err := runCmd(client, hl, fmt.Sprintf("userdel -r %s 2>/dev/null || userdel %s", user, user)); err != nil {
		res.Err = err
		return res
	}

	exists, err = checkUserExists(client, hl, user)
	if err != nil {
		res.Err = err
		return res
	}
	if exists {
		res.Err = fmt.Errorf("attempted to delete user %q but it is still present after userdel", user)
		return res
	}

	res.Message = fmt.Sprintf("user %q deleted from device", user)
	res.Changed = true
	return res
}

func checkBool(client *SSHClient, hl *HostLogger, name, cmd, expected string) ValidationResult {
	out, err := runCmd(client, hl, cmd)
	if err != nil {
		return ValidationResult{Name: name, Passed: false, Detail: err.Error()}
	}
	passed := strings.Contains(out, expected)
	return ValidationResult{Name: name, Passed: passed, Detail: strings.TrimSpace(out)}
}

// configureHost performs the PAM setup + requested user operations on a single host.
func configureHost(client *SSHClient, hl *HostLogger, host string, cfg *Config, mode Mode, users []string) HostResult {
	result := HostResult{Host: host, Mode: mode, StartTime: time.Now()}
	hl.WriteHeader(host)

	defer func() {
		result.EndTime = time.Now()
	}()

	// --- Backups ---
	for _, f := range []string{
		"/etc/pam.d/sshd",
		"/etc/pam.d/sudo",
		"/etc/ssh/sshd_config",
	} {
		if err := backupFile(client, hl, f); err != nil {
			result.Err = fmt.Errorf("backing up %s: %w", f, err)
			return result
		}
	}

	out, err := runCmd(client, hl, `if [ -f /etc/pam.d/pam_ng1_auth ]; then echo EXISTS; else echo MISSING; fi`)
	if err != nil {
		result.Err = err
		return result
	}
	if strings.Contains(out, "EXISTS") {
		if err := backupFile(client, hl, "/etc/pam.d/pam_ng1_auth"); err != nil {
			result.Err = err
			return result
		}
	}

	// --- Create PAM file ---
	pamLine := cfg.PAMLine()
	createPamCmd := fmt.Sprintf("cat > /etc/pam.d/pam_ng1_auth << 'EOF'\n%s\nEOF", pamLine)
	if _, err := runCmd(client, hl, createPamCmd); err != nil {
		result.Err = err
		return result
	}
	if _, err := runCmd(client, hl, "chmod 644 /etc/pam.d/pam_ng1_auth"); err != nil {
		result.Err = err
		return result
	}

	// --- Wire into sshd / sudo PAM stacks ---
	sshdCmd := "sed -i '/pam_ng1_auth/d' /etc/pam.d/sshd && sed -i '1i auth include pam_ng1_auth' /etc/pam.d/sshd"
	if _, err := runCmd(client, hl, sshdCmd); err != nil {
		result.Err = err
		return result
	}

	sudoCmd := "sed -i '/pam_ng1_auth/d' /etc/pam.d/sudo && sed -i '1i auth include pam_ng1_auth' /etc/pam.d/sudo"
	if _, err := runCmd(client, hl, sudoCmd); err != nil {
		result.Err = err
		return result
	}

	usePamCmd := `grep -q '^UsePAM yes' /etc/ssh/sshd_config || sed -i 's/^#*UsePAM.*/UsePAM yes/' /etc/ssh/sshd_config`
	if _, err := runCmd(client, hl, usePamCmd); err != nil {
		result.Err = err
		return result
	}

	// --- User operations (add or delete) ---
	for _, user := range users {
		var uRes UserOpResult

		switch mode {
		case ModeDelete:
			uRes = deleteUser(client, hl, user)
		default:
			uRes = addUser(client, hl, user)
		}

		if uRes.Err != nil {
			result.Err = fmt.Errorf("user %q operation failed: %w", user, uRes.Err)
			result.UserResults = append(result.UserResults, uRes)
			return result
		}

		result.UserResults = append(result.UserResults, uRes)
		hl.WriteLine("\nUSER RESULT: %s\n", uRes.Message)
	}

	// --- Restart sshd ---
	if _, err := runCmd(client, hl, "service sshd restart || systemctl restart sshd"); err != nil {
		result.Err = err
		return result
	}

	// --- Validation ---
	validations := []ValidationResult{}

	validations = append(validations, checkBool(client, hl, "pam_ng1_auth Exists",
		"test -f /etc/pam.d/pam_ng1_auth && echo PASS || echo FAIL", "PASS"))

	validations = append(validations, checkBool(client, hl, "SSHD PAM Enabled",
		"head -1 /etc/pam.d/sshd", "pam_ng1_auth"))

	validations = append(validations, checkBool(client, hl, "SUDO PAM Enabled",
		"head -1 /etc/pam.d/sudo", "pam_ng1_auth"))

	validations = append(validations, checkBool(client, hl, "UsePAM Enabled",
		"grep '^UsePAM yes' /etc/ssh/sshd_config", "UsePAM yes"))

	validations = append(validations, checkBool(client, hl, "SSHD Running",
		"pgrep sshd >/dev/null && echo PASS || echo FAIL", "PASS"))

	// Checks that the configured auth port is reachable from the device
	// itself (loopback check), since devices are now the only IP source.
	ngCheckCmd := fmt.Sprintf(
		"timeout 5 bash -c '</dev/tcp/127.0.0.1/%s' && echo PASS || echo FAIL",
		cfg.Port,
	)
	validations = append(validations, checkBool(client, hl, "Auth Port Reachable (local)", ngCheckCmd, "PASS"))

	for _, user := range users {
		name := fmt.Sprintf("User %s (%s)", user, mode)
		exists, err := checkUserExists(client, hl, user)

		var passed bool
		var detail string
		if err != nil {
			passed = false
			detail = err.Error()
		} else {
			if mode == ModeDelete {
				passed = !exists
			} else {
				passed = exists
			}
			detail = fmt.Sprintf("exists=%v", exists)
		}

		validations = append(validations, ValidationResult{Name: name, Passed: passed, Detail: detail})
	}

	// --- write validation summary to host log ---
	hl.WriteLine("\n====================================================\n")
	hl.WriteLine("VALIDATION RESULTS\n")

	failed := 0
	for _, v := range validations {
		status := "PASS"
		if !v.Passed {
			status = "FAIL"
			failed++
		}
		hl.WriteLine("%s : %s (%s)\n", v.Name, status, v.Detail)
	}

	hl.WriteFooter()

	result.Validations = validations
	result.Success = failed == 0

	return result
}
