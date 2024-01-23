package cherry

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/cherryservers/cherrygo"
	cherryServer "github.com/cherryservers/cloud-provider-cherry/cherry/server"
	"github.com/cherryservers/cloud-provider-cherry/cherry/server/store"
	retryablehttp "github.com/hashicorp/go-retryablehttp"

	clientset "k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	restclient "k8s.io/client-go/rest"
	cloudprovider "k8s.io/cloud-provider"
)

const (
	token           = "12345678"
	nodeName        = "ccm-test"
	validRegionCode = "L2"
	validRegionName = "LT-Test-2"
	validPlanName   = "hourly"
)

// mockControllerClientBuilder mock implementation of https://pkg.go.dev/k8s.io/cloud-provider#ControllerClientBuilder
// so we can pass it to cloud.Initialize()
type mockControllerClientBuilder struct {
}

//nolint:revive // ignore unused error
func (m mockControllerClientBuilder) Config(name string) (*restclient.Config, error) {
	return &restclient.Config{}, nil
}

//nolint:revive // ignore unused error
func (m mockControllerClientBuilder) ConfigOrDie(name string) *restclient.Config {
	return &restclient.Config{}
}

//nolint:revive // ignore unused error
func (m mockControllerClientBuilder) Client(name string) (clientset.Interface, error) {
	return k8sfake.NewSimpleClientset(), nil
}

//nolint:revive // ignore unused error
func (m mockControllerClientBuilder) ClientOrDie(name string) clientset.Interface {
	return k8sfake.NewSimpleClientset()
}

type apiServerError struct {
	t *testing.T
}

func (a *apiServerError) Error(err error) {
	a.t.Fatal(err)
}

// create a valid cloud with a client
func testGetValidCloud(t *testing.T, LoadBalancerSetting string) (*cloud, *store.Memory) {
	// mock endpoint so our client can handle it
	backend := store.NewMemory()
	fake := cherryServer.CherryServer{
		Store: backend,
		ErrorHandler: &apiServerError{
			t: t,
		},
	}
	// ensure we have a single region
	_, _ = backend.CreateRegion(validRegionName, validRegionCode)
	project, err := backend.CreateProject("testproject", false)
	if err != nil {
		t.Fatalf("failed to create backend project: %v", err)
	}
	ts := httptest.NewServer(fake.CreateHandler())

	url, _ := url.Parse(ts.URL)
	urlString := url.String()

	client, err := constructClient(token, urlString)
	if err != nil {
		t.Fatalf("unable to construct testing cherryservers API client: %v", err)
	}

	// now just need to create a client
	config := Config{
		ProjectID:           project.ID,
		LoadBalancerSetting: LoadBalancerSetting,
	}
	c, _ := newCloud(config, client)
	ccb := &mockControllerClientBuilder{}
	c.Initialize(ccb, nil)

	return c.(*cloud), backend
}

func TestLoadBalancerDefaultDisabled(t *testing.T) {
	vc, _ := testGetValidCloud(t, "")
	response, supported := vc.LoadBalancer()
	var (
		expectedSupported = false
		expectedResponse  = response
	)
	if supported != expectedSupported {
		t.Errorf("supported returned %v instead of expected %v", supported, expectedSupported)
	}
	if response != expectedResponse {
		t.Errorf("value returned %v instead of expected %v", response, expectedResponse)
	}
}

func TestLoadBalancerMetalLB(t *testing.T) {
	t.Skip("Test needs a k8s client to work")
	vc, _ := testGetValidCloud(t, "metallb:///metallb-system/config")
	response, supported := vc.LoadBalancer()
	var (
		expectedSupported = true
		expectedResponse  = response
	)
	if supported != expectedSupported {
		t.Errorf("supported returned %v instead of expected %v", supported, expectedSupported)
	}
	if response != expectedResponse {
		t.Errorf("value returned %v instead of expected %v", response, expectedResponse)
	}
}

func TestLoadBalancerEmpty(t *testing.T) {
	t.Skip("Test needs a k8s client to work")
	vc, _ := testGetValidCloud(t, "empty://")
	response, supported := vc.LoadBalancer()
	var (
		expectedSupported = true
		expectedResponse  = response
	)
	if supported != expectedSupported {
		t.Errorf("supported returned %v instead of expected %v", supported, expectedSupported)
	}
	if response != expectedResponse {
		t.Errorf("value returned %v instead of expected %v", response, expectedResponse)
	}
}

func TestLoadBalancerKubeVIP(t *testing.T) {
	t.Skip("Test needs a k8s client to work")
	vc, _ := testGetValidCloud(t, "kube-vip://")
	response, supported := vc.LoadBalancer()
	var (
		expectedSupported = true
		expectedResponse  = response
	)
	if supported != expectedSupported {
		t.Errorf("supported returned %v instead of expected %v", supported, expectedSupported)
	}
	if response != expectedResponse {
		t.Errorf("value returned %v instead of expected %v", response, expectedResponse)
	}
}

func TestInstances(t *testing.T) {
	vc, _ := testGetValidCloud(t, "")
	response, supported := vc.Instances()
	expectedSupported := false
	expectedResponse := cloudprovider.Instances(nil)
	if supported != expectedSupported {
		t.Errorf("supported returned %v instead of expected %v", supported, expectedSupported)
	}
	if response != expectedResponse {
		t.Errorf("value returned %v instead of expected %v", response, expectedResponse)
	}
}

func TestClusters(t *testing.T) {
	vc, _ := testGetValidCloud(t, "")
	response, supported := vc.Clusters()
	var (
		expectedSupported = false
		expectedResponse  cloudprovider.Clusters // defaults to nil
	)
	if supported != expectedSupported {
		t.Errorf("supported returned %v instead of expected %v", supported, expectedSupported)
	}
	if response != expectedResponse {
		t.Errorf("value returned %v instead of expected %v", response, expectedResponse)
	}

}

func TestRoutes(t *testing.T) {
	vc, _ := testGetValidCloud(t, "")
	response, supported := vc.Routes()
	var (
		expectedSupported = false
		expectedResponse  cloudprovider.Routes // defaults to nil
	)
	if supported != expectedSupported {
		t.Errorf("supported returned %v instead of expected %v", supported, expectedSupported)
	}
	if response != expectedResponse {
		t.Errorf("value returned %v instead of expected %v", response, expectedResponse)
	}

}
func TestProviderName(t *testing.T) {
	vc, _ := testGetValidCloud(t, "")
	name := vc.ProviderName()
	if name != ProviderName {
		t.Errorf("returned %s instead of expected %s", name, ProviderName)
	}
}

func TestHasClusterID(t *testing.T) {
	vc, _ := testGetValidCloud(t, "")
	cid := vc.HasClusterID()
	expectedCid := true
	if cid != expectedCid {
		t.Errorf("returned %v instead of expected %v", cid, expectedCid)
	}

}

// builds a Cherry Servers client
func constructClient(authToken, baseURL string) (*cherrygo.Client, error) {
	client := retryablehttp.NewClient()
	var opts []cherrygo.ClientOpt

	// TODO - fix this by providing alternate URL
	opts = append(opts, cherrygo.WithAuthToken(authToken), cherrygo.WithHTTPClient(client.StandardClient()))
	if baseURL != "" {
		opts = append(opts, cherrygo.WithURL(baseURL))
	}
	return cherrygo.NewClient(opts...)
}
