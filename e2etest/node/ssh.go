package node

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"io"
	"strconv"

	"golang.org/x/crypto/ssh"
)

type sshCmdRunner struct {
	signer ssh.Signer
}

func (s sshCmdRunner) dial(addr string) (*ssh.Client, error) {
	const port = "22"

	cfg := ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(s.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", addr+":"+port, &cfg)
	if err != nil {
		return nil, fmt.Errorf("failed ssh dial: %w", err)
	}
	return client, nil
}

// Run a command via SSH at the given address using bash.
// On a non-zero exit code, the response string contains stderr.
// Passing nil stdin is fine.
func (s sshCmdRunner) run(addr, cmd string, stdin io.Reader) (string, error) {
	client, err := s.dial(addr)
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to establish session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	session.Stdin = stdin

	if err := session.Run("bash -lc " + strconv.Quote(cmd)); err != nil {
		return stderr.String(), fmt.Errorf("failed to run cmd: %w", err)
	}

	return stdout.String(), nil
}

func newSSHCmdRunner() (*sshCmdRunner, error) {
	_, pri, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 keys: %w", err)
	}

	sig, err := ssh.NewSignerFromSigner(pri)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key signer: %w", err)
	}

	s := sshCmdRunner{sig}
	return &s, nil
}
