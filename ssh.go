package main

import (
	"bytes"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHClient wraps a connected SSH session for running one-off commands.
type SSHClient struct {
	client *ssh.Client
	cmdTO  time.Duration
}

func NewSSHClient(host, username, password string, cfg *Config) (*SSHClient, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", cfg.SSH.Port))

	sshCfg := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // NOTE: replace with known_hosts verification in production
		Timeout:         time.Duration(cfg.SSH.ConnectTimeoutSeconds) * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	return &SSHClient{
		client: client,
		cmdTO:  time.Duration(cfg.SSH.CommandTimeoutSeconds) * time.Second,
	}, nil
}

// Run executes a single command over a fresh session and returns combined stdout+stderr.
func (c *SSHClient) Run(command string) (string, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("creating ssh session: %w", err)
	}
	defer session.Close()

	var outBuf bytes.Buffer
	session.Stdout = &outBuf
	session.Stderr = &outBuf

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case err := <-done:
		_ = err
		return outBuf.String(), nil
	case <-time.After(c.cmdTO):
		session.Close()
		return outBuf.String(), fmt.Errorf("command timed out after %s: %s", c.cmdTO, command)
	}
}

func (c *SSHClient) Close() error {
	return c.client.Close()
}
