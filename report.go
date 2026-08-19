package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Report aggregates HostResults across the whole run for a final summary.
type Report struct {
	Mode      Mode
	StartTime time.Time
	EndTime   time.Time
	Results   []HostResult
}

func NewReport(mode Mode) *Report {
	return &Report{
		Mode:      mode,
		StartTime: time.Now(),
	}
}

func (r *Report) AddResult(res HostResult) {
	r.Results = append(r.Results, res)
}

func (r *Report) Finish() {
	r.EndTime = time.Now()
}

// Counts returns (success, validationFailed, erroredOut) totals.
func (r *Report) Counts() (success, failed, errored int) {
	for _, res := range r.Results {
		switch {
		case res.Err != nil:
			errored++
		case res.Success:
			success++
		default:
			failed++
		}
	}
	return
}

func (r *Report) Render() string {
	var b strings.Builder

	success, failed, errored := r.Counts()

	b.WriteString(strings.Repeat("=", 70) + "\n")
	b.WriteString(" NETSCOUT nGenius PAM Deployment Report\n")
	b.WriteString(strings.Repeat("=", 70) + "\n")
	fmt.Fprintf(&b, "Mode              : %s\n", strings.ToUpper(string(r.Mode)))
	fmt.Fprintf(&b, "Started           : %s\n", r.StartTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Finished          : %s\n", r.EndTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "Duration          : %s\n", r.EndTime.Sub(r.StartTime).Round(time.Second))
	fmt.Fprintf(&b, "Hosts Processed   : %d\n", len(r.Results))
	fmt.Fprintf(&b, "Successful        : %d\n", success)
	fmt.Fprintf(&b, "Validation Failed : %d\n", failed)
	fmt.Fprintf(&b, "Errored           : %d\n", errored)
	b.WriteString(strings.Repeat("-", 70) + "\n\n")

	for _, res := range r.Results {
		status := "SUCCESS"
		switch {
		case res.Err != nil:
			status = "ERROR"
		case !res.Success:
			status = "VALIDATION FAILED"
		}

		fmt.Fprintf(&b, "Host: %s  [%s]\n", res.Host, status)

		if res.Err != nil {
			fmt.Fprintf(&b, "  Error: %v\n", res.Err)
		}

		for _, u := range res.UserResults {
			marker := "-"
			if u.Err != nil {
				fmt.Fprintf(&b, "  %s user %-15s ERROR: %v\n", marker, u.User, u.Err)
			} else {
				fmt.Fprintf(&b, "  %s %s\n", marker, u.Message)
			}
		}

		for _, v := range res.Validations {
			status := "PASS"
			if !v.Passed {
				status = "FAIL"
			}
			fmt.Fprintf(&b, "  [%s] %s\n", status, v.Name)
		}

		b.WriteString("\n")
	}

	b.WriteString(strings.Repeat("=", 70) + "\n")

	return b.String()
}

// WriteToFile saves the rendered report to a timestamped file in logDir.
func (r *Report) WriteToFile(logDir string) (string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}

	name := fmt.Sprintf("report_%s.txt", r.StartTime.Format("20060102_150405"))
	path := filepath.Join(logDir, name)

	if err := os.WriteFile(path, []byte(r.Render()), 0o644); err != nil {
		return "", err
	}

	return path, nil
}
