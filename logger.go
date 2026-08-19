package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Logger handles the shared Summary.log and Errors.log files.
type Logger struct {
	mu         sync.Mutex
	logDir     string
	summaryLog *os.File
	errorLog   *os.File
}

func NewLogger(logDir string) (*Logger, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating log dir %q: %w", logDir, err)
	}

	summaryPath := filepath.Join(logDir, "Summary.log")
	errorPath := filepath.Join(logDir, "Errors.log")

	summaryFile, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening summary log: %w", err)
	}

	errorFile, err := os.OpenFile(errorPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		summaryFile.Close()
		return nil, fmt.Errorf("opening error log: %w", err)
	}

	return &Logger{
		logDir:     logDir,
		summaryLog: summaryFile,
		errorLog:   errorFile,
	}, nil
}

func timestamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func (l *Logger) Summary(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.summaryLog, "[%s] %s\n", timestamp(), msg)
}

func (l *Logger) Error(host, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(l.errorLog, "[%s] %s : %s\n", timestamp(), host, msg)
}

func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.summaryLog.Close()
	l.errorLog.Close()
}

// HostLogger writes a detailed per-host transcript log.
type HostLogger struct {
	file *os.File
}

func sanitizeHostName(host string) string {
	safe := host
	for _, ch := range []string{":", "/", "\\"} {
		safe = strings.ReplaceAll(safe, ch, "_")
	}
	return safe
}

func NewHostLogger(logDir, host string) (*HostLogger, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating log dir %q: %w", logDir, err)
	}

	safeHost := sanitizeHostName(host)
	path := filepath.Join(logDir, fmt.Sprintf("%s.log", safeHost))

	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening host log %q: %w", path, err)
	}

	return &HostLogger{file: f}, nil
}

func (h *HostLogger) WriteHeader(host string) {
	fmt.Fprintf(h.file, "\nHOST: %s\n", host)
	fmt.Fprintf(h.file, "START: %s\n\n", timestamp())
}

func (h *HostLogger) WriteFooter() {
	fmt.Fprintf(h.file, "\nEND: %s\n", timestamp())
}

func (h *HostLogger) WriteCommand(cmd string) {
	fmt.Fprint(h.file, "\n====================================================\n")
	fmt.Fprintf(h.file, "COMMAND:\n%s\n", cmd)
}

func (h *HostLogger) WriteOutput(output string) {
	fmt.Fprintf(h.file, "\nOUTPUT:\n%s\n", output)
	h.file.Sync()
}

func (h *HostLogger) WriteLine(format string, args ...interface{}) {
	fmt.Fprintf(h.file, format, args...)
}

func (h *HostLogger) Close() {
	h.file.Close()
}
