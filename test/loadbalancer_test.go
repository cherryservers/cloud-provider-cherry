package test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/intstr"
	apiwatch "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/watch"
)

func setupProject(t testing.TB, name string) cherrygo.Project {
	t.Helper()

	project, _, err := cherryClientFixture.Projects.Create(*teamIDFixture, &cherrygo.CreateProject{
		Name: name})
	if err != nil {
		t.Fatalf("failed to setup cherry servers project: %v", err)
	}
	t.Cleanup(func() {
		cherryClientFixture.Projects.Delete(project.ID)
	})
	return project
}

func setupNodeProvisioner(t testing.TB, testName string, projectID int) nodeProvisioner {
	t.Helper()

	// Create a SSH key signer:
	sshRunner, err := newSshCmdRunner()
	if err != nil {
		t.Fatalf("failed to create SSH runner: %v", err)
	}

	// Create SSH key on Cherry servers:
	pub := ssh.MarshalAuthorizedKey(sshRunner.signer.PublicKey())
	pub = pub[:len(pub)-1] // strip newline
	sshKey, _, err := cherryClientFixture.SSHKeys.Create(&cherrygo.CreateSSHKey{
		Label: testName,
		Key:   string(pub),
	})
	if err != nil {
		t.Fatalf("failed to create SSH key on cherry servers: %v", err)
	}
	t.Cleanup(func() {
		cherryClientFixture.SSHKeys.Delete(sshKey.ID)
	})
	return nodeProvisioner{
		cherryClient: *cherryClientFixture,
		projectID:    projectID,
		sshKeyID:     strconv.Itoa(sshKey.ID),
		cmdRunner:    *sshRunner,
	}
}

func setupKubeConfig(t testing.TB, n node) string {
	t.Helper()

	cfg, cleanup, err := n.kubeconfig()
	if err != nil {
		t.Fatalf("failed to generate kubeconfig: %v", err)
	}
	t.Cleanup(func() {
		cleanup()
	})
	return cfg
}

func setupCcmSecret(t testing.TB, ccmCfg ccm.Config) string {
	t.Helper()

	secret, cleanup, err := ccmSecret(ccmCfg)
	if err != nil {
		t.Fatalf("failed to setup ccm secret config")
	}
	t.Cleanup(func() {
		cleanup()
	})
	return secret
}

type testEnv struct {
	project         cherrygo.Project
	nodeProvisioner nodeProvisioner
	mainNode        node
	kubeconfig      string // path
	k8sClient       kubernetes.Interface
}

type testEnvConfig struct {
	name         string
	loadBalancer string // optional
	fipTag       string // optional
}

func setupTestEnv(ctx context.Context, t testing.TB, cfg testEnvConfig) *testEnv {
	t.Helper()

	// Setup project:
	project := setupProject(t, cfg.name)

	// Setup node provisioner:
	np := setupNodeProvisioner(t, cfg.name, project.ID)

	// Create a node (server with k8s running):
	node, err := np.provision(t.Context())
	if err != nil {
		t.Fatalf("failed to provision test node: %v", err)
	}

	// Get node kubeconfig:
	kubeCfg := setupKubeConfig(t, *node)

	client, err := k8sClient(kubeCfg)
	if err != nil {
		t.Fatalf("failed to create k8s client: %v", err)
	}

	// Generate config secret for CCM:
	secret := setupCcmSecret(t, ccm.Config{
		AuthToken:           cherryClientFixture.AuthToken,
		Region:              region,
		LoadBalancerSetting: cfg.loadBalancer,
		FIPTag:              cfg.fipTag,
		ProjectID:           project.ID})

	// Cancel child process on interrupt/termination.
	// Should work on Windows as well, see https://pkg.go.dev/os/signal#hdr-Windows.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	// Launch CCM:
	stopped, err := runCcm(ctx, kubeCfg, secret, client)
	if err != nil {
		t.Fatalf("failed to run CCM: %v", err)
	}
	// Stop signal diversion when CCM is stopped.
	go func() {
		<-stopped
		stop()
	}()
	t.Cleanup(func() {
		<-stopped
	})

	return &testEnv{
		project:         project,
		nodeProvisioner: np,
		mainNode:        *node,
		kubeconfig:      kubeCfg,
		k8sClient:       client,
	}
}

// Ensures project has non-zero ASN.
func ensureProjectAsn(ctx context.Context, t testing.TB, project *cherrygo.Project, projectServer cherrygo.Server) {
	// We need a local ASN to deploy kube-vip, but
	// cherry servers only assigns a local ASN
	// to a project once there's a server with BGP enabled.
	// Since the LB controller is supposed to enable BGP on a per-node basis,
	// we enable BGP on the node, get our ASN, and then disable it, so
	// that the enabler can be tested.
	t.Helper()

	_, _, err := cherryClientFixture.Servers.Update(projectServer.ID, &cherrygo.UpdateServer{Bgp: true})
	if err != nil {
		t.Fatalf("failed to enable bgp on server %q: %v", projectServer.Hostname, err)
	}

	err = expBackoffWithContext(func() (bool, error) {
		*project, _, err = cherryClientFixture.Projects.Get(project.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get project: %w", err)
		}
		if project.Bgp.LocalASN == 0 {
			return false, nil
		}
		return true, nil
	}, defaultExpBackoffConfigWithContext(ctx))
	if err != nil {
		t.Fatalf("couldn't establish project asn: %v", err)
	}

	_, _, err = cherryClientFixture.Servers.Update(projectServer.ID, &cherrygo.UpdateServer{Bgp: false})
	if err != nil {
		t.Fatalf("failed to disable bgp on server %q: %v", projectServer.Hostname, err)
	}
	*project, _, err = cherryClientFixture.Projects.Get(project.ID, nil)
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}
}

type kubeObjectHelpers struct {
	t      testing.TB
	client kubernetes.Interface
}

func (k *kubeObjectHelpers) setupKubeVipRbac(ctx context.Context, namespace string) (saName string) {
	k.t.Helper()

	const kubeVipSaName = "kube-vip"

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeVipSaName,
			Namespace: namespace,
		},
	}
	_, err := k.client.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		k.t.Fatalf("failed to deploy kube-vip service account: %v", err)
	}

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "system:kube-vip-role",
			Annotations: map[string]string{
				"rbac.authorization.kubernetes.io/autoupdate": "true",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"services/status"}, Verbs: []string{"update"}},
			{APIGroups: []string{""}, Resources: []string{"services", "endpoints"}, Verbs: []string{"list", "get", "watch", "update"}},
			{APIGroups: []string{""}, Resources: []string{"nodes"}, Verbs: []string{"list", "get", "watch", "update", "patch"}},
			{APIGroups: []string{"coordination.k8s.io"}, Resources: []string{"leases"}, Verbs: []string{"list", "get", "watch", "update", "create"}},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"list", "get", "watch", "update"}},
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"list"}},
		},
	}

	_, err = k.client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	if err != nil {
		k.t.Fatalf("failed to deploy kube-vip cluster role: %v", err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "system:kube-vip-binding",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:kube-vip-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      kubeVipSaName,
				Namespace: namespace,
			},
		},
	}

	_, err = k.client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if err != nil {
		k.t.Fatalf("failed to deploy kube-vip role binding: %v", err)
	}

	return kubeVipSaName
}

type kubeVipConfig struct {
	localAsn    string
	peerAsn     string
	peerAddress string
	routerID    string
}

func (k *kubeObjectHelpers) setupKubeVip(ctx context.Context, cfg kubeVipConfig) {
	k.t.Helper()

	const name = "kube-vip-ds"
	const version = "v1.0.1"
	const nameLabel = "app.kubernetes.io/name"
	const versionLabel = "app.kubernetes.io/version"
	const namespace = metav1.NamespaceSystem

	saName := k.setupKubeVipRbac(ctx, namespace)

	kubeVipDaemonSet := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    map[string]string{nameLabel: name, versionLabel: version},
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{nameLabel: name},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:    map[string]string{nameLabel: name, versionLabel: version},
					Name:      name,
					Namespace: namespace,
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      controlPlaneNodeLabel,
												Operator: corev1.NodeSelectorOpExists,
											},
										},
									},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Args: []string{"manager"},
							Env: []corev1.EnvVar{
								{
									Name:  "vip_arp",
									Value: "false",
								},
								{
									Name:  "port",
									Value: "6443",
								},
								{
									Name: "vip_nodename",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "spec.nodeName",
										},
									},
								},
								{
									Name:  "vip_interface",
									Value: "lo",
								},
								{
									Name:  "dns_mode",
									Value: "first",
								},
								{
									Name:  "svc_enable",
									Value: "true",
								},
								{
									Name:  "svc_leasename",
									Value: "plndr-svcs-lock",
								},
								{
									Name:  "bgp_enable",
									Value: "true",
								},
								{
									Name:  "bgp_as",
									Value: cfg.localAsn,
								},
								{
									Name:  "bgp_peeraddress",
									Value: cfg.peerAddress,
								},
								{
									Name: "bgp_peerpass",
								},
								{
									Name:  "bgp_peeras",
									Value: cfg.peerAsn,
								},
								{
									Name: "vip_address",
								},
								{
									Name:  "prometheus_server",
									Value: ":2112",
								},
								{
									Name:  "bgp_routerid",
									Value: cfg.routerID,
								},
							},
							Image:           "ghcr.io/kube-vip/kube-vip:v1.0.1",
							ImagePullPolicy: "IfNotPresent",
							Name:            "kube-vip",
							SecurityContext: &corev1.SecurityContext{
								Capabilities: &corev1.Capabilities{
									Add: []corev1.Capability{
										"NET_ADMIN",
										"NET_RAW",
									},
									Drop: []corev1.Capability{
										"ALL",
									},
								},
							},
						},
					},
					HostNetwork:        true,
					ServiceAccountName: saName,
					Tolerations: []corev1.Toleration{
						{
							Effect:   corev1.TaintEffectNoSchedule,
							Operator: corev1.TolerationOpExists,
						},
						{
							Effect:   corev1.TaintEffectNoExecute,
							Operator: corev1.TolerationOpExists,
						},
					},
				},
			},
		},
	}

	_, err := k.client.AppsV1().DaemonSets(namespace).Create(ctx, &kubeVipDaemonSet, metav1.CreateOptions{})
	if err != nil {
		k.t.Fatalf("failed to deploy kube-vip DaemonSet: %v", err)
	}
}

func (k *kubeObjectHelpers) setupNginx(ctx context.Context, namespace string) *appsv1.Deployment {
	k.t.Helper()
	replicas := int32(2)

	deployment := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "nginx-deployment",
			Labels: map[string]string{"app": "nginx"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "nginx"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "nginx"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nginx",
							Image: "nginx:1.29.2",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 80,
								},
							},
						},
					},
				},
			},
		},
	}

	deployed, err := k.client.AppsV1().Deployments(namespace).Create(
		ctx, &deployment, metav1.CreateOptions{})
	if err != nil {
		k.t.Fatalf("failed to deploy nginx: %v", err)
	}

	return deployed
}

type loadBalancerConfig struct {
	name      string
	namespace string
	selector  map[string]string
}

func (k *kubeObjectHelpers) setupLoadBalancer(ctx context.Context, cfg loadBalancerConfig) *corev1.Service {
	lb := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: cfg.name,
		},
		Spec: corev1.ServiceSpec{
			Selector: cfg.selector,
			Ports: []corev1.ServicePort{
				{
					Port:       8765,
					TargetPort: intstr.FromInt(9376),
				},
			},
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}

	deployed, err := k.client.CoreV1().Services(cfg.namespace).Create(ctx, &lb, metav1.CreateOptions{})
	if err != nil {
		k.t.Fatalf("failed to setup load balancer %q: %v", cfg.name, err)
	}
	return deployed
}

func (k *kubeObjectHelpers) loadBalancerFipTags(ctx context.Context, svc *corev1.Service) map[string]string {
	systemNamespace, err := k.client.CoreV1().Namespaces().Get(ctx, metav1.NamespaceSystem, metav1.GetOptions{})
	if err != nil {
		k.t.Fatalf("failed to get system namespace: %v", err)
	}
	clusterID := string(systemNamespace.UID)

	svcChecksum := sha256.Sum256(fmt.Appendf(nil, "%s/%s", svc.Namespace, svc.Name))
	svcRep := base64.StdEncoding.EncodeToString(svcChecksum[:])

	return map[string]string{
		"cluster": clusterID,
		"service": svcRep,
		"usage":   "cloud-provider-cherry-auto",
	}
}

func (k *kubeObjectHelpers) untilLoadBalancerEnsured(ctx context.Context, name, namespace string) {
	k.t.Helper()
	lw := cache.NewListWatchFromClient(k.client.CoreV1().RESTClient(), "services", namespace, fields.Everything())

	_, err := watch.UntilWithSync(ctx, lw, &corev1.Service{}, nil, func(event apiwatch.Event) (done bool, err error) {
		svc, ok := event.Object.(*corev1.Service)
		if !ok {
			return false, fmt.Errorf("unexpected object type: %T", event.Object)
		}
		if svc.ObjectMeta.Name != name {
			return false, nil
		}
		// LB should be ensured, when ingress is set.
		if len(svc.Status.LoadBalancer.Ingress) > 0 {
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		k.t.Fatalf("ingress ip not set for load balancer %q: %v", name, err)
	}

}

func fipsContainTags(fips []cherrygo.IPAddress, wantTags map[string]string) bool {
	return slices.ContainsFunc(fips, func(fip cherrygo.IPAddress) bool {
		if fip.Tags == nil {
			return false
		}
		return maps.Equal(*fip.Tags, wantTags)
	})
}

func assertFipTags(t testing.TB, fips []cherrygo.IPAddress, wantTags map[string]string) {
	if !fipsContainTags(fips, wantTags) {
		var b strings.Builder
		for _, fip := range fips {
			if fip.Type == "floating-ip" {
				fmt.Fprintln(&b, *fip.Tags)
			}
		}
		t.Errorf("fip tags: %s, want one %v", b.String(), wantTags)
	}
}

type loadBalancerSubTester struct {
	firstSvc  *corev1.Service
	secondSvc *corev1.Service
	env       *testEnv
}

func (s loadBalancerSubTester) testFipTags(ctx context.Context, t *testing.T) {
	t.Run("fip tags", func(t *testing.T) {
		kubeHelper := kubeObjectHelpers{t: t, client: s.env.k8sClient}

		fips, _, err := cherryClientFixture.IPAddresses.List(s.env.project.ID, nil)
		if err != nil {
			t.Fatalf("failed to get fips: %v", err)
		}

		wantTags := kubeHelper.loadBalancerFipTags(ctx, s.firstSvc)
		assertFipTags(t, fips, wantTags)

		wantTags = kubeHelper.loadBalancerFipTags(ctx, s.secondSvc)
		assertFipTags(t, fips, wantTags)
	})
}

func (s loadBalancerSubTester) testServerBgpEnabled(ctx context.Context, t *testing.T) {
	t.Run("server bgp enabled", func(t *testing.T) {
		srv, _, err := cherryClientFixture.Servers.Get(s.env.mainNode.server.ID, nil)
		if err != nil {
			t.Fatalf("failed to get server: %v", err)
		}

		if got, want := srv.BGP.Enabled, true; got != want {
			t.Errorf("server %q bgp=%t, want=%t", srv.Name, got, want)
		}
	})
}

func (s loadBalancerSubTester) testProjectBgpEnabled(ctx context.Context, t *testing.T) {
	t.Run("project bgp enabled", func(t *testing.T) {
		project, _, err := cherryClientFixture.Projects.Get(s.env.project.ID, nil)
		if err != nil {
			t.Fatalf("failed to get project: %v", err)
		}

		if got, want := project.Bgp.Enabled, true; got != want {
			t.Errorf("project %q bgp=%t, want=%t", project.Name, got, want)
		}
	})
}

func (s loadBalancerSubTester) testNodeHasAnnotations(ctx context.Context, t *testing.T) {
	t.Run("node has annotations", func(t *testing.T) {
		node, err := s.env.k8sClient.CoreV1().Nodes().Get(ctx, s.env.mainNode.server.Hostname, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("failed to get node: %v", err)
		}

		srv, _, err := cherryClientFixture.Servers.Get(s.env.mainNode.server.ID, nil)
		if err != nil {
			t.Fatalf("failed to get server: %v", err)
		}

		project, _, err := cherryClientFixture.Projects.Get(s.env.project.ID, nil)
		if err != nil {
			t.Fatalf("failed to get project: %v", err)
		}

		for i, peerIp := range srv.Region.BGP.Hosts {
			peerAsnKey := strings.Replace(ccm.DefaultAnnotationPeerASN, "{{n}}", strconv.Itoa(i), 1)
			if got, want := node.Annotations[peerAsnKey], strconv.Itoa(srv.Region.BGP.Asn); got != want {
				t.Errorf("peerAsn=%s, want=%s, key=%s", got, want, peerAsnKey)
			}

			nodeAsnKey := strings.Replace(ccm.DefaultAnnotationNodeASN, "{{n}}", strconv.Itoa(i), 1)
			if got, want := node.Annotations[nodeAsnKey], strconv.Itoa(project.Bgp.LocalASN); got != want {
				t.Errorf("nodeAsn=%s, want=%s, key=%s", got, want, nodeAsnKey)
			}

			peerIpKey := strings.Replace(ccm.DefaultAnnotationPeerIP, "{{n}}", strconv.Itoa(i), 1)
			if got, want := node.Annotations[peerIpKey], peerIp; got != want {
				t.Errorf("peerIp=%s, want=%s, key=%s", got, want, peerIpKey)
			}
		}
	})
}

func (s loadBalancerSubTester) testNodeDoesntHaveAnnotations(ctx context.Context, t *testing.T) {
	t.Run("node doesn't have annotations", func(t *testing.T) {
		node, err := s.env.k8sClient.CoreV1().Nodes().Get(ctx, s.env.mainNode.server.Hostname, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("failed to get node: %v", err)
		}

		for i := range s.env.mainNode.server.Region.BGP.Hosts {
			peerAsnKey := strings.Replace(ccm.DefaultAnnotationPeerASN, "{{n}}", strconv.Itoa(i), 1)
			if got, ok := node.Annotations[peerAsnKey]; ok != false {
				t.Errorf("peerAsn=%s, want: not found, key=%s", got, peerAsnKey)
			}

			nodeAsnKey := strings.Replace(ccm.DefaultAnnotationNodeASN, "{{n}}", strconv.Itoa(i), 1)
			if got, ok := node.Annotations[nodeAsnKey]; ok != false {
				t.Errorf("nodeAsn=%s, want: not found, key=%s", got, nodeAsnKey)
			}

			peerIpKey := strings.Replace(ccm.DefaultAnnotationPeerIP, "{{n}}", strconv.Itoa(i), 1)
			if got, ok := node.Annotations[peerIpKey]; ok != false {
				t.Errorf("peerIp=%s, want: not found, key=%s", got, peerIpKey)
			}
		}
	})
}

func untilFipCount(ctx context.Context, t *testing.T, projectID, count int) error {
	fipRemovedCtx, cancel := context.WithTimeout(ctx, eventTimeout)
	defer cancel()

	return expBackoffWithContext(func() (bool, error) {
		fips, _, err := cherryClientFixture.IPAddresses.List(projectID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get ips: %w", err)
		}

		c := fipCount(fips)
		if c != count {
			return false, nil
		}
		return true, nil
	}, defaultExpBackoffConfigWithContext(fipRemovedCtx))
}

func TestKubeVip(t *testing.T) {
	const testName = "kubernetes-ccm-test-lb-kube-vip"
	ctx := t.Context()

	env := setupTestEnv(ctx, t, testEnvConfig{
		name: testName, loadBalancer: "kube-vip://",
	})

	// We need a local ASN to deploy kube-vip.
	ensureProjectAsn(ctx, t, &env.project, env.mainNode.server)

	kubeHelper := kubeObjectHelpers{t, env.k8sClient}

	kubeHelper.setupKubeVip(ctx, kubeVipConfig{
		localAsn:    strconv.Itoa(env.project.Bgp.LocalASN),
		peerAsn:     strconv.Itoa(env.mainNode.server.Region.BGP.Asn),
		peerAddress: env.mainNode.server.Region.BGP.Hosts[0],
		routerID:    env.mainNode.server.IPAddresses[0].Address,
	})

	const namespace = metav1.NamespaceDefault

	testDeployment := kubeHelper.setupNginx(ctx, namespace)
	selector := testDeployment.Spec.Selector.MatchLabels

	firstSvc := kubeHelper.setupLoadBalancer(ctx, loadBalancerConfig{
		name:      "example-service-1",
		namespace: namespace,
		selector:  selector,
	})

	kubeHelper.untilLoadBalancerEnsured(ctx, "example-service-1", namespace)

	secondSvc := kubeHelper.setupLoadBalancer(ctx, loadBalancerConfig{
		name:      "example-service-2",
		namespace: namespace,
		selector:  selector,
	})

	kubeHelper.untilLoadBalancerEnsured(ctx, "example-service-2", namespace)

	subtester := loadBalancerSubTester{
		firstSvc:  firstSvc,
		secondSvc: secondSvc,
		env:       env,
	}

	subtester.testFipTags(ctx, t)
	subtester.testServerBgpEnabled(ctx, t)
	subtester.testProjectBgpEnabled(ctx, t)
	subtester.testNodeHasAnnotations(ctx, t)

	t.Run("remove first service", func(t *testing.T) {
		err := env.k8sClient.CoreV1().Services(namespace).Delete(ctx, firstSvc.Name, metav1.DeleteOptions{})
		if err != nil {
			t.Fatalf("failed to delete service %q: %v", firstSvc.Name, err)
		}
		err = untilFipCount(ctx, t, env.project.ID, 1)
		if err != nil {
			t.Errorf("fip count not reduced after service removal: %v", err)
		}

		subtester.testNodeHasAnnotations(ctx, t)
	})

	t.Run("remove second services", func(t *testing.T) {
		err := env.k8sClient.CoreV1().Services(namespace).Delete(ctx, secondSvc.Name, metav1.DeleteOptions{})
		if err != nil {
			t.Fatalf("failed to delete service %q: %v", secondSvc.Name, err)
		}
		err = untilFipCount(ctx, t, env.project.ID, 0)
		if err != nil {
			t.Errorf("fip count not reduced after service removal: %v", err)
		}

		subtester.testNodeDoesntHaveAnnotations(ctx, t)
	})
}
