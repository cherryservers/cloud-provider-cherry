package cherry

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
)

const (
	apiKeyName                    = "CHERRY_API_KEY"
	projectIDName                 = "CHERRY_PROJECT_ID"
	regionName                    = "CHERRY_REGION_NAME"
	loadBalancerSettingName       = "CHERRY_LOAD_BALANCER"
	envVarAnnotationLocalASN      = "CHERRY_ANNOTATION_LOCAL_ASN"
	envVarAnnotationPeerASN       = "CHERRY_ANNOTATION_PEER_ASN"
	envVarAnnotationPeerIP        = "CHERRY_ANNOTATION_PEER_IP"
	envVarAnnotationSrcIP         = "CHERRY_ANNOTATION_SRC_IP"
	envVarAnnotationFIPRegion     = "CHERRY_ANNOTATION_FIP_REGION"
	envVarFIPTag                  = "CHERRY_FIP_TAG"
	envVarAPIServerPort           = "CHERRY_API_SERVER_PORT"
	envVarBGPNodeSelector         = "CHERRY_BGP_NODE_SELECTOR"
	envVarFIPHealthCheckUseHostIP = "CHERRY_FIP_HEALTH_CHECK_USE_HOST_IP"
)

// Config configuration for a provider, includes authentication token, project ID ID, and optional override URL to talk to a different Cherry Servers API endpoint
type Config struct {
	AuthToken               string  `json:"apiKey"`
	ProjectID               int     `json:"projectId"`
	BaseURL                 *string `json:"base-url,omitempty"`
	LoadBalancerSetting     string  `json:"loadbalancer"`
	Region                  string  `json:"region,omitempty"`
	AnnotationLocalASN      string  `json:"annotationLocalASN,omitempty"`
	AnnotationPeerASN       string  `json:"annotationPeerASN,omitempty"`
	AnnotationPeerIP        string  `json:"annotationPeerIP,omitempty"`
	AnnotationSrcIP         string  `json:"annotationSrcIP,omitempty"`
	AnnotationFIPRegion     string  `json:"annotationFIPRegion,omitempty"`
	FIPTag                  string  `json:"fipTag,omitempty"`
	APIServerPort           int32   `json:"apiServerPort,omitempty"`
	BGPNodeSelector         string  `json:"bgpNodeSelector,omitempty"`
	FIPHealthCheckUseHostIP bool    `json:"fipHealthCheckUseHostIP,omitempty"`
}

// String converts the Config structure to a string, while masking hidden fields.
// Is not 100% a String() conversion, as it adds some intelligence to the output,
// and masks sensitive data
func (c Config) Strings() []string {
	ret := []string{}
	if c.AuthToken != "" {
		ret = append(ret, "authToken: '<masked>'")
	} else {
		ret = append(ret, "authToken: ''")
	}
	ret = append(ret, fmt.Sprintf("projectID: '%d'", c.ProjectID))
	if c.LoadBalancerSetting == "" {
		ret = append(ret, "loadbalancer config: disabled")
	} else {
		ret = append(ret, fmt.Sprintf("load balancer config: ''%s", c.LoadBalancerSetting))
	}
	ret = append(ret, fmt.Sprintf("region: '%s'", c.Region))
	ret = append(ret, fmt.Sprintf("Floating IP Tag: '%s'", c.FIPTag))
	ret = append(ret, fmt.Sprintf("API Server Port: '%d'", c.APIServerPort))
	ret = append(ret, fmt.Sprintf("BGP Node Selector: '%s'", c.BGPNodeSelector))

	return ret
}

func getConfig(providerConfig io.Reader) (Config, error) {
	// get our token and project
	var config, rawConfig Config
	configBytes, err := ioutil.ReadAll(providerConfig)
	if err != nil {
		return config, fmt.Errorf("failed to read configuration : %w", err)
	}
	err = json.Unmarshal(configBytes, &rawConfig)
	if err != nil {
		return config, fmt.Errorf("failed to process json of configuration file at path %s: %w", providerConfig, err)
	}

	// read env vars; if not set, use rawConfig
	apiToken := os.Getenv(apiKeyName)
	if apiToken == "" {
		apiToken = rawConfig.AuthToken
	}
	config.AuthToken = apiToken

	projectID := os.Getenv(projectIDName)
	if projectID == "" {
		projectID = fmt.Sprintf("%d", rawConfig.ProjectID)
	}
	projID, err := strconv.ParseInt(projectID, 10, 32)
	if err != nil {
		return config, fmt.Errorf("project ID must be a valid integer: %v", err)
	}
	config.ProjectID = int(projID)

	loadBalancerSetting := os.Getenv(loadBalancerSettingName)
	config.LoadBalancerSetting = rawConfig.LoadBalancerSetting
	// rule for processing: any setting in env var overrides setting from file
	if loadBalancerSetting != "" {
		config.LoadBalancerSetting = loadBalancerSetting
	}

	region := os.Getenv(regionName)
	if region == "" {
		region = rawConfig.Region
	}

	if apiToken == "" {
		return config, fmt.Errorf("environment variable %q is required", apiKeyName)
	}

	if projectID == "" {
		return config, fmt.Errorf("environment variable %q is required", projectIDName)
	}

	config.Region = region

	// set the annotations
	config.AnnotationLocalASN = DefaultAnnotationNodeASN
	annotationLocalASN := os.Getenv(envVarAnnotationLocalASN)
	if annotationLocalASN != "" {
		config.AnnotationLocalASN = annotationLocalASN
	}
	config.AnnotationPeerASN = DefaultAnnotationPeerASN
	annotationPeerASN := os.Getenv(envVarAnnotationPeerASN)
	if annotationPeerASN != "" {
		config.AnnotationPeerASN = annotationPeerASN
	}
	config.AnnotationPeerIP = DefaultAnnotationPeerIP
	annotationPeerIP := os.Getenv(envVarAnnotationPeerIP)
	if annotationPeerIP != "" {
		config.AnnotationPeerIP = annotationPeerIP
	}
	config.AnnotationSrcIP = DefaultAnnotationSrcIP
	annotationSrcIP := os.Getenv(envVarAnnotationSrcIP)
	if annotationSrcIP != "" {
		config.AnnotationSrcIP = annotationSrcIP
	}

	config.AnnotationFIPRegion = DefaultAnnotationFIPRegion
	annotationFIPRegion := os.Getenv(envVarAnnotationFIPRegion)
	if annotationFIPRegion != "" {
		config.AnnotationFIPRegion = annotationFIPRegion
	}

	if rawConfig.FIPTag != "" {
		config.FIPTag = rawConfig.FIPTag
	}
	fipTag := os.Getenv(envVarFIPTag)
	if fipTag != "" {
		config.FIPTag = fipTag
	}

	apiServer := os.Getenv(envVarAPIServerPort)
	switch {
	case apiServer != "":
		apiServerNo, err := strconv.Atoi(apiServer)
		if err != nil {
			return config, fmt.Errorf("env var %s must be a number, was %s: %w", envVarAPIServerPort, apiServer, err)
		}
		config.APIServerPort = int32(apiServerNo)
	case rawConfig.APIServerPort != 0:
		config.APIServerPort = rawConfig.APIServerPort
	default:
		// if nothing else set it, we set it to 0, to indicate that it should use whatever the kube-apiserver port is
		config.APIServerPort = 0
	}

	config.BGPNodeSelector = rawConfig.BGPNodeSelector
	if v := os.Getenv(envVarBGPNodeSelector); v != "" {
		config.BGPNodeSelector = v
	}

	if _, err := labels.Parse(config.BGPNodeSelector); err != nil {
		return config, fmt.Errorf("BGP Node Selector must be valid Kubernetes selector: %w", err)
	}

	config.FIPHealthCheckUseHostIP = rawConfig.FIPHealthCheckUseHostIP
	if v := os.Getenv(envVarFIPHealthCheckUseHostIP); v != "" {
		useHostIP, err := strconv.ParseBool(v)
		if err != nil {
			return config, fmt.Errorf("env var %s must be a boolean, was %s: %w", envVarFIPHealthCheckUseHostIP, v, err)
		}
		config.FIPHealthCheckUseHostIP = useHostIP
	}

	return config, nil
}

// printConfig report the config to startup logs
func printConfig(config Config) {
	lines := config.Strings()
	for _, l := range lines {
		klog.Info(l)
	}
}
