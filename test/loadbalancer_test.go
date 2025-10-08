package test

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/cherryservers/cherrygo/v3"
	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestKubeVip(t *testing.T) {
	// Things we need to do:
	// 1. Create a project(k disabled, to test BGP enabler).
	// 2. Create a local SSH key signer.
	// 3. Create a SSH key on Cherry Servers.
	// 4. Create a server on Cherry Servers.
	// 5. Ensure K8S is running on our server.
	// 6. Deploy kube-vip on our server/node.
	// 7. Start the CCM locally.
	// 8. ***Do the tests***

	// Create a project:
	const testName = "kubernetes-ccm-test-lb-kube-vip"
	ctx := t.Context()

	project, _, err := cherryClientFixture.Projects.Create(*teamIDFixture, &cherrygo.CreateProject{
		Name: testName})
	if err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	// defer cherryClientFixture.Projects.Delete(project.ID)

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
	defer cherryClientFixture.SSHKeys.Delete(sshKey.ID)

	np := newNodeProvisioner(*cherryClientFixture, project.ID, strconv.Itoa(sshKey.ID), *sshRunner)

	// Create a node (server with k8s running):
	node, err := np.provision(ctx)
	if err != nil {
		t.Fatalf("failed to provision test node: %v", err)
	}

	// Generate client for node's kube-api:
	cfg, cleanup, err := node.kubeconfig()
	if err != nil {
		t.Fatalf("failed to generate node kubeconfig: %v", err)
	}
	defer cleanup()

	client, err := k8sClient(cfg)
	if err != nil {
		t.Fatalf("failed to create k8s client: %v", err)
	}

	// Generate config secret for CCM:
	secret, cleanup, err := ccmSecret(cherryClientFixture.AuthToken, region, "", "kube-vip://", project.ID)
	if err != nil {
		t.Fatalf("failed to create config secret for ccm: %v", err)
	}
	defer cleanup()

	// Launch CCM:
	runCcm(ctx, cfg, secret, client)

	// We need a local ASN to deploy kube-vip, but
	// cherry servers only assigns a local ASN
	// to a project once there's a server with BGP enabled.
	// Since the LB controller is supposed to enable BGP on a per-node basis,
	// we enable BGP on the node, get our ASN, and then disable it, so
	// that the enabler can be tested.
	_, _, err = cherryClientFixture.Servers.Update(node.server.ID, &cherrygo.UpdateServer{Bgp: true})
	if err != nil {
		t.Fatalf("failed to enable bgp on server %q: %v", node.server.Hostname, err)
	}

	err = expBackoffWithContext(func() (bool, error) {
		project, _, err = cherryClientFixture.Projects.Get(project.ID, nil)
		if err != nil {
			return false, fmt.Errorf("failed to get project: %w", err)
		}
		if project.Bgp.LocalASN == 0 {
			return false, nil
		}
		return true, nil
	}, defaultExpBackoffConfigWithContext(context.TODO()))
	if err != nil {
		t.Fatalf("couldn't establish project asn: %v", err)
	}

	_, _, err = cherryClientFixture.Servers.Update(node.server.ID, &cherrygo.UpdateServer{Bgp: false})
	if err != nil {
		t.Fatalf("failed to disable bgp on server %q: %v", node.server.Hostname, err)
	}
	project, _, err = cherryClientFixture.Projects.Get(project.ID, nil)
	if err != nil {
		t.Fatalf("failed to get project: %v", err)
	}

	// Deploy kube-vip:
	const kubeVipSaName = "kube-vip"
	const kubeVipName = "kube-vip-ds"
	const kubeVipVersion = "v1.0.1"
	const nameLabel = "app.kubernetes.io/name"
	const versionLabel = "app.kubernetes.io/version"
	const kubeVipNamespace = metav1.NamespaceSystem
	ServerPubIP, err := serverPublicIP(node.server)
	if err != nil {
		t.Fatal(err.Error())
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubeVipSaName,
			Namespace: kubeVipNamespace,
		},
	}
	_, err = client.CoreV1().ServiceAccounts(kubeVipNamespace).Create(context.TODO(), sa, metav1.CreateOptions{})
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

	_, err = client.RbacV1().ClusterRoles().Create(context.TODO(), cr, metav1.CreateOptions{})
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
				Namespace: kubeVipNamespace,
			},
		},
	}

	_, err = client.RbacV1().ClusterRoleBindings().Create(context.TODO(), crb, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to deploy kube-vip role binding: %v", err)
	}

	kubeVipDaemonSet := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    map[string]string{nameLabel: kubeVipName, versionLabel: kubeVipVersion},
			Name:      kubeVipName,
			Namespace: kubeVipNamespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{nameLabel: kubeVipName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:    map[string]string{nameLabel: kubeVipName, versionLabel: kubeVipVersion},
					Name:      kubeVipName,
					Namespace: kubeVipNamespace,
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
									Value: strconv.Itoa(project.Bgp.LocalASN),
								},
								{
									Name:  "bgp_peeraddress",
									Value: node.server.Region.BGP.Hosts[0],
								},
								{
									Name: "bgp_peerpass",
								},
								{
									Name:  "bgp_peeras",
									Value: strconv.Itoa(node.server.Region.BGP.Asn),
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
									Value: ServerPubIP,
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
					ServiceAccountName: kubeVipSaName,
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

	_, err = client.AppsV1().DaemonSets(kubeVipNamespace).Create(context.TODO(), &kubeVipDaemonSet, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to deploy kube-vip DaemonSet: %v", err)
	}

}
