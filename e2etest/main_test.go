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
	apiTokenVar  = "CHERRY_TEST_API_TOKEN"
	teamIDVar    = "CHERRY_TEST_TEAM_ID"
	imagePathVar = "CCM_IMAGE_PATH"
)

var cherryClient *cherrygo.Client
var teamID *int
var ccmImagePath *string

type config struct {
	apiToken     string
	teamID       int
	ccmImagePath string
}

// loadConfig loads test configuration from environment variables.
func loadConfig() (config, error) {
	teamID, err := strconv.Atoi(os.Getenv(teamIDVar))
	if err != nil {
		return config{}, fmt.Errorf("failed to parse team ID: %w", err)
	}
	return config{
			apiToken:     os.Getenv(apiTokenVar),
			teamID:       teamID,
			ccmImagePath: os.Getenv(imagePathVar)},
		nil
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

	code := m.Run()
	return code
}

func TestMain(m *testing.M) {
	code := runMain(m)
	os.Exit(code)
}
