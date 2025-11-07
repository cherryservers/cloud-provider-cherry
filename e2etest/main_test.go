package e2etest

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
)

const (
	apiTokenVar   = "CHERRY_TEST_API_TOKEN"
	teamIDVar     = "CHERRY_TEST_TEAM_ID"
	imagePathVar  = "CCM_IMG_PATH"
	noCleanupVar  = "NO_CLEANUP"
	k8sVersionVar = "K8S_VERSION"
)

var cherryClient *cherrygo.Client
var teamID *int
var ccmImagePath *string
var cleanup *bool
var k8sVersion *string

type config struct {
	apiToken     string
	teamID       int
	ccmImagePath string
	cleanup      bool
	k8sVersion   string
}

// loadConfig loads test configuration from environment variables.
func loadConfig() (config, error) {
	teamID, err := strconv.Atoi(os.Getenv(teamIDVar))
	if err != nil {
		return config{}, fmt.Errorf("failed to parse team ID: %w", err)
	}

	noCleanup := false
	if noCleanupEnv, ok := os.LookupEnv(noCleanupVar); ok {
		noCleanup, err = strconv.ParseBool(noCleanupEnv)
		if err != nil {
			return config{}, fmt.Errorf("failed to parse %s var: %w", noCleanupVar, err)
		}
	}

	k8sVersion := "1.34"
	if k8sVersionEnv, ok := os.LookupEnv(k8sVersionVar); ok {
		k8sVersion = k8sVersionEnv
	}

	return config{
		apiToken:     os.Getenv(apiTokenVar),
		teamID:       teamID,
		ccmImagePath: os.Getenv(imagePathVar),
		cleanup:      !noCleanup,
		k8sVersion:   k8sVersion,
	}, nil
}

func runMain(m *testing.M) int {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("failed to load test config: %v", err)
	}

	cherryClient, err = cherrygo.NewClient(cherrygo.WithAuthToken(cfg.apiToken))
	if err != nil {
		log.Fatalf("failed to initialize cherrygo client: %v", err)
	}

	teamID = &cfg.teamID
	ccmImagePath = &cfg.ccmImagePath
	cleanup = &cfg.cleanup
	k8sVersion = &cfg.k8sVersion

	code := m.Run()
	return code
}

func TestMain(m *testing.M) {
	code := runMain(m)
	os.Exit(code)
}
