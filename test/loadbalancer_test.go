package test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
	ccm "github.com/cherryservers/cloud-provider-cherry/cherry"
	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
	go func (){
		<-stopped
		stop()
	}()
	t.Cleanup(func () {
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

func setupKubeVipRbac(ctx context.Context, t testing.TB, namespace string, client kubernetes.Interface) (saName string) {
	t.Helper()

	const kubeVipSaName = "kube-vip"

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeVipSaName,
			Namespace: namespace,
		},
	}
	_, err := client.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to deploy kube-vip service account: %v", err)
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

	_, err = client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to deploy kube-vip cluster role: %v", err)
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

	_, err = client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to deploy kube-vip role binding: %v", err)
	}

	return kubeVipSaName
}

type kubeVipConfig struct {
	localAsn    string
	peerAsn     string
	peerAddress string
	routerID    string
}

func setupKubeVip(ctx context.Context, t testing.TB, client kubernetes.Interface, cfg kubeVipConfig) {
	t.Helper()

	const name = "kube-vip-ds"
	const version = "v1.0.1"
	const nameLabel = "app.kubernetes.io/name"
	const versionLabel = "app.kubernetes.io/version"
	const namespace = metav1.NamespaceSystem

	saName := setupKubeVipRbac(ctx, t, namespace, client)

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

	_, err := client.AppsV1().DaemonSets(namespace).Create(ctx, &kubeVipDaemonSet, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to deploy kube-vip DaemonSet: %v", err)
	}
}

func TestKubeVip(t *testing.T) {
	const testName = "kubernetes-ccm-test-lb-kube-vip"
	ctx := t.Context()

	env := setupTestEnv(ctx, t, testEnvConfig{
		name: testName, loadBalancer: "kube-vip://",
	})

	// We need a local ASN to deploy kube-vip.
	ensureProjectAsn(ctx, t, &env.project, env.mainNode.server)

	setupKubeVip(ctx, t, env.k8sClient, kubeVipConfig{
		localAsn:    strconv.Itoa(env.project.Bgp.LocalASN),
		peerAsn:     strconv.Itoa(env.mainNode.server.Region.BGP.Asn),
		peerAddress: env.mainNode.server.Region.BGP.Hosts[0],
		routerID:    env.mainNode.server.IPAddresses[0].Address,
	})

}
