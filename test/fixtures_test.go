package test

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"testing"

	cherrygo "github.com/cherryservers/cherrygo/v3"
)

const (
	apiTokenVar        = "CHERRY_TEST_API_TOKEN"
	teamIDVar          = "CHERRY_TEST_TEAM_ID"
	sshKeyIDVar        = "CHERRY_TEST_SSH_KEY_ID"
	serverImage        = "ubuntu_24_04_64bit"
	masterServerPlan   = "B1-4-4gb-80s-shared"
	masterServerRegion = "LT-Siauliai"
	userDataPath       = "./testdata/cloud-config/init-microk8s.yaml"
)

var cherryClient *cherrygo.Client
var cpNodeFixture *cherrygo.Server

type config struct {
	apiToken string
	teamID   int
	sshKeys  []string
}

// Loads configuration from env vars.
func loadConfig() (config, error) {
	apiToken := os.Getenv(apiTokenVar)
	teamID, err := strconv.Atoi(os.Getenv(teamIDVar))
	if err != nil {
		return config{}, fmt.Errorf("failed to parse project ID: %w", err)
	}
	sshKeys := []string{os.Getenv(sshKeyIDVar)}
	return config{apiToken, teamID, sshKeys}, nil
}

func initCherryClient(apiToken string) error {
	var err error
	cherryClient, err = cherrygo.NewClient(cherrygo.WithAuthToken(apiToken))
	if err != nil {
		return fmt.Errorf("failed to initialize cherrygo client: %w", err)
	}
	return nil
}

// Provisions a Cherry Servers server with kubernetes running.
func serverWithK8S(projectID int, sshKeys []string) (*cherrygo.Server, error) {
	userDataRaw, err := os.ReadFile(userDataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read user data file: %w", err)
	}

	srv, _, err := cherryClient.Servers.Create(&cherrygo.CreateServer{
		ProjectID: projectID,
		Plan:      masterServerPlan,
		Region:    masterServerRegion,
		Image:     serverImage,
		UserData:  base64.StdEncoding.EncodeToString(userDataRaw),
		SSHKeys:   sshKeys,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to provision master server instance: %w", err)
	}

	return &srv, nil
}

func TestMain(m *testing.M) {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load test config: %v", err)
		os.Exit(1)
	}

	err = initCherryClient(cfg.apiToken)
	if err != nil {
		fmt.Fprint(os.Stderr, err.Error())
		os.Exit(1)
	}

	project, _, err := cherryClient.Projects.Create(cfg.teamID, &cherrygo.CreateProject{
		Name: "kubernetes-ccm-test", Bgp: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create project: %v", err)
		os.Exit(1)
	}

	cpNode, err := serverWithK8S(project.ID, cfg.sshKeys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to provision k8s control plane node: %v", err)
		os.Exit(1)
	}

	// wait for control plane node to become active
	expBackoff(func() (bool, error) {
		srv, _, err := cherryClient.Servers.Get(cpNode.ID, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error while waiting for control plane node to become active: %v", err)
			cherryClient.Servers.Delete(cpNode.ID)
			os.Exit(1)
		}
		cpNode = &srv
		if cpNode.State == "active" {
			return true, nil
		}
		return false, nil
	}, defaultExpBackoffConfig())

	cpNodeFixture = cpNode

	code := m.Run()

	// can't use defer, because os.Exit bypasses it
	cherryClient.Servers.Delete(cpNode.ID)
	os.Exit(code)

}
