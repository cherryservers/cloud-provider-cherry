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
	projectIDVar       = "CHERRY_TEST_PROJECT_ID"
	sshKeyIDVar        = "CHERRY_TEST_SSH_KEY_ID"
	serverImage        = "ubuntu_24_04_64bit"
	masterServerPlan   = "B1-4-4gb-80s-shared"
	masterServerRegion = "LT-Siauliai"
	userDataPath       = "./testdata/cloud-config/init-microk8s.yaml"
)

var cherryClient *cherrygo.Client
var cpNodeFixture *cherrygo.Server

func initCherryClient() error {
	apiToken := os.Getenv(apiTokenVar)
	var err error
	cherryClient, err = cherrygo.NewClient(cherrygo.WithAuthToken(apiToken))
	if err != nil {
		return fmt.Errorf("failed to initialize cherrygo client: %w", err)
	}
	return nil
}

// provision a Cherry Servers server with kubernetes running
func serverWithK8S() (*cherrygo.Server, error) {
	sshKeys := []string{os.Getenv(sshKeyIDVar)}
	projectID, err := strconv.Atoi(os.Getenv(projectIDVar))
	if err != nil {
		return nil, fmt.Errorf("failed to parse project ID: %w", err)
	}

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
	
	err := initCherryClient(); if err != nil {
		fmt.Fprint(os.Stderr, err.Error())
		os.Exit(1)
	}

	cpNode, err := serverWithK8S()
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