package test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"testing"

	cherrygo "github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
)

const (
	apiTokenVar  = "CHERRY_TEST_API_TOKEN"
	teamIDVar    = "CHERRY_TEST_TEAM_ID"
	serverImage  = "ubuntu_24_04_64bit"
	serverPlan   = "B1-4-4gb-80s-shared"
	serverRegion = "LT-Siauliai"
	userDataPath = "./testdata/cloud-config/init-microk8s.yaml"
)

var cherryClient *cherrygo.Client
var sshFixture *sshCmdRunner
var cpNodeFixture *cherrygo.Server

type config struct {
	apiToken string
	teamID   int
}

type sshCmdRunner struct {
	signer ssh.Signer
}

// run a command via SSH at the given address using bash.
// On a non-zero exit code, the response string contains stderr.
func (s sshCmdRunner) run(addr, cmd string) (string, error) {
	const port = "22"

	cfg := ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(s.signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	client, err := ssh.Dial("tcp", addr+":"+port, &cfg)
	if err != nil {
		return "", fmt.Errorf("failed ssh dial: %w", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to establish session: %w", err)
	}
	defer session.Close()

	var b bytes.Buffer
	var eb bytes.Buffer
	session.Stdout = &b
	session.Stderr = &eb
	if err := session.Run("bash -lc " + strconv.Quote(cmd)); err != nil {
		return eb.String(), fmt.Errorf("failed to run cmd: %w", err)
	}

	return b.String(), nil
}

// Loads configuration from env vars.
func loadConfig() (config, error) {
	apiToken := os.Getenv(apiTokenVar)
	teamID, err := strconv.Atoi(os.Getenv(teamIDVar))
	if err != nil {
		return config{}, fmt.Errorf("failed to parse project ID: %w", err)
	}
	return config{apiToken, teamID}, nil
}

func initCherryClient(apiToken string) error {
	var err error
	cherryClient, err = cherrygo.NewClient(cherrygo.WithAuthToken(apiToken))
	if err != nil {
		return fmt.Errorf("failed to initialize cherrygo client: %w", err)
	}
	return nil
}

func sshSigner() (ssh.Signer, error) {
	_, pri, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 keys: %w", err)
	}

	sig, err := ssh.NewSignerFromSigner(pri)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key signer: %w", err)
	}

	return sig, nil
}

func serverPublicIP(srv cherrygo.Server) (string, error) {
	for _, ip := range srv.IPAddresses {
		if ip.Type == "primary-ip" {
			return ip.Address, nil
		}
	}
	return "", fmt.Errorf("server %d has no public ip", srv.ID)
}

// Provisions a Cherry Servers server and waits for it k8s to be running.
func createServerWithK8s(projectID int, sshKeys []string) (*cherrygo.Server, error) {
	userDataRaw, err := os.ReadFile(userDataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read user data file: %w", err)
	}

	srv, _, err := cherryClient.Servers.Create(&cherrygo.CreateServer{
		ProjectID: projectID,
		Plan:      serverPlan,
		Region:    serverRegion,
		Image:     serverImage,
		UserData:  base64.StdEncoding.EncodeToString(userDataRaw),
		SSHKeys:   sshKeys,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create server: %w", err)
	}

	expBackoff(func() (bool, error) {
		srv, _, err = cherryClient.Servers.Get(srv.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get server: %w", err)
		}
		if srv.State == "active" {
			return true, nil
		}
		return false, nil
	}, defaultExpBackoffConfig())

	ip, err := serverPublicIP(srv)
	if err != nil {
		return nil, err
	}

	expBackoff(func() (bool, error) {
		// Check if kube-api is reachable. Non-zero exit code will be returned if not.
		_, err = sshFixture.run(ip, "microk8s kubectl get nodes --no-headers")
		if err != nil {
			return false, nil
		}
		return true, nil
	}, defaultExpBackoffConfig())

	return &srv, nil
}

type sweeper struct {
	projectID int
	sshKeyID  int
}

func (s sweeper) sweep() {
	if s.projectID != 0 {
		cherryClient.Projects.Delete(s.projectID)
	}
	if s.sshKeyID != 0 {
		cherryClient.SSHKeys.Delete(s.sshKeyID)
	}

}

// Print error message to stderr and exit with code 1.
// The sweeper hook is used to dispose of lingering resources.
func failInit(msg string, s sweeper) {
	fmt.Fprint(os.Stderr, msg)
	s.sweep()
	os.Exit(1)
}

func TestMain(m *testing.M) {
	sw := sweeper{}

	cfg, err := loadConfig()
	if err != nil {
		failInit(fmt.Sprintf("failed to load test config: %v", err), sw)
	}

	err = initCherryClient(cfg.apiToken)
	if err != nil {
		failInit(err.Error(), sw)
	}

	sig, err := sshSigner()
	if err != nil {
		failInit(fmt.Sprintf("failed to generate SSH signer: %v", err), sw)
	}

	sshFixture = &sshCmdRunner{sig}

	pub := ssh.MarshalAuthorizedKey(sig.PublicKey())
	pub = pub[:len(pub)-1] // strip newline
	sshKey, _, err := cherryClient.SSHKeys.Create(&cherrygo.CreateSSHKey{
		Label: "kubernetes-ccm-test",
		Key:   string(pub),
	})
	if err != nil {
		failInit(fmt.Sprintf("failed to create SSH key on cherry servers: %v", err), sw)
	}

	sw.sshKeyID = sshKey.ID

	project, _, err := cherryClient.Projects.Create(cfg.teamID, &cherrygo.CreateProject{
		Name: "kubernetes-ccm-test", Bgp: true})
	if err != nil {
		failInit(fmt.Sprintf("failed to create project: %v", err), sw)
	}

	sw.projectID = project.ID

	cpNode, err := createServerWithK8s(project.ID, []string{strconv.Itoa(sshKey.ID)})
	if err != nil {
		failInit(fmt.Sprintf("failed to provision k8s control plane node: %v", err), sw)
	}

	cpNodeFixture = cpNode

	code := m.Run()

	// can't use defer, because os.Exit bypasses it
	sw.sweep()
	os.Exit(code)

}
