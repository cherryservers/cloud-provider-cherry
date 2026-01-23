package metallb

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/cherryservers/cloud-provider-cherry/cherry/loadbalancers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func assertPeersEqual(t *testing.T, got, want []Peer) {
	if len(got) != len(want) {
		t.Fatalf("got %d peers, want %d", len(got), len(want))
	}

	for i := range got {
		if got[i].MyASN != want[i].MyASN {
			t.Errorf("MyASN: %d, want: %d, peer index %d", got[i].MyASN, want[i].MyASN, i)
		}
		if got[i].ASN != want[i].ASN {
			t.Errorf("ASN: %d, want: %d, peer index %d", got[i].ASN, want[i].ASN, i)
		}
		if got[i].Addr != want[i].Addr {
			t.Errorf("Addr: %q, want: %q, peer index %d", got[i].Addr, want[i].Addr, i)
		}
		if got[i].Port != want[i].Port {
			t.Errorf("Port: %d, want: %d, peer index %d", got[i].Port, want[i].Port, i)
		}
		if got[i].SrcAddr != want[i].SrcAddr {
			t.Errorf("SrcAddr: %q, want: %q, peer index %d",
				got[i].SrcAddr, want[i].SrcAddr, i)
		}
		if got[i].HoldTime != want[i].HoldTime {
			t.Errorf("HoldTime: %q, want: %q, peer index %d",
				got[i].HoldTime, want[i].HoldTime, i)
		}
		if got[i].RouterID != want[i].RouterID {
			t.Errorf("RouterID: %q, want: %q, peer index %d",
				got[i].RouterID, want[i].RouterID, i)
		}
		if !reflect.DeepEqual(got[i].NodeSelectors, want[i].NodeSelectors) {
			t.Errorf("NodeSelectors: %+v, want: %+v, peer index %d",
				got[i].NodeSelectors, want[i].NodeSelectors, i)
		}
		if got[i].Password != want[i].Password {
			t.Errorf("Password: %q, want: %q, peer index %d",
				got[i].Password, want[i].Password, i)
		}
		if got[i].Name != want[i].Name {
			t.Errorf("Name: %q, want: %q, peer index %d", got[i].Name, want[i].Name, i)
		}
	}
}

func TestPeersFromNodesNative(t *testing.T) {
	const labelKey = "kubernetes.io/hostname"

	tests := []struct {
		title string
		nodes []loadbalancers.Node
		peers []Peer
	}{
		{title: "empty", nodes: []loadbalancers.Node{}, peers: nil},
		{title: "single node",
			nodes: []loadbalancers.Node{
				{
					Name:     "test-name",
					SourceIP: "127.0.0.1",
					LocalASN: 12345,
					PeerASN:  23456,
					Password: "test-password",
					Peers: []loadbalancers.Peer{
						{Address: "46.166.166.122", Port: 179},
						{Address: "46.166.166.123", Port: 180},
					},
					Region: "test-region",
				},
			}, peers: []Peer{
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.122",
					Port:          179,
					SrcAddr:       "127.0.0.1",
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: []NodeSelector{{MatchLabels: map[string]string{labelKey: "test-name"}}},
					Password:      "test-password",
					Name:          "test-name-0",
				},
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.123",
					Port:          180,
					SrcAddr:       "127.0.0.1",
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: []NodeSelector{{MatchLabels: map[string]string{labelKey: "test-name"}}},
					Password:      "test-password",
					Name:          "test-name-1",
				},
			}},
		{title: "multi node",
			nodes: []loadbalancers.Node{
				{
					Name:     "test-first-name",
					SourceIP: "127.0.0.1",
					LocalASN: 12345,
					PeerASN:  23456,
					Password: "test-first-password",
					Peers: []loadbalancers.Peer{
						{Address: "46.166.166.122", Port: 179},
						{Address: "46.166.166.123", Port: 180},
					},
					Region: "test-first-region",
				},
				{
					Name:     "test-second-name",
					SourceIP: "127.0.0.2",
					LocalASN: 34567,
					PeerASN:  45678,
					Password: "test-second-password",
					Peers: []loadbalancers.Peer{
						{Address: "195.189.96.10", Port: 179},
						{Address: "195.189.96.11", Port: 180},
					},
					Region: "test-second-region",
				},
			}, peers: []Peer{
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.122",
					Port:          179,
					SrcAddr:       "127.0.0.1",
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: []NodeSelector{{MatchLabels: map[string]string{labelKey: "test-first-name"}}},
					Password:      "test-first-password",
					Name:          "test-first-name-0",
				},
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.123",
					Port:          180,
					SrcAddr:       "127.0.0.1",
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: []NodeSelector{{MatchLabels: map[string]string{labelKey: "test-first-name"}}},
					Password:      "test-first-password",
					Name:          "test-first-name-1",
				},
				{
					MyASN:         34567,
					ASN:           45678,
					Addr:          "195.189.96.10",
					Port:          179,
					SrcAddr:       "127.0.0.2",
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: []NodeSelector{{MatchLabels: map[string]string{labelKey: "test-second-name"}}},
					Password:      "test-second-password",
					Name:          "test-second-name-0",
				},
				{
					MyASN:         34567,
					ASN:           45678,
					Addr:          "195.189.96.11",
					Port:          180,
					SrcAddr:       "127.0.0.2",
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: []NodeSelector{{MatchLabels: map[string]string{labelKey: "test-second-name"}}},
					Password:      "test-second-password",
					Name:          "test-second-name-1",
				},
			}},
	}

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := PeersFromNodesNative(tt.nodes)
			want := tt.peers

			assertPeersEqual(t, got, want)
		})
	}
}

func TestPeersFromNodesFRR(t *testing.T) {
	const (
		hostnameLabelKey = "kubernetes.io/hostname"
		regionLabelKey   = "topology.kubernetes.io/region"
	)

	var nodeSelectors = func(region string) []NodeSelector {
		return []NodeSelector{
			{MatchLabels: map[string]string{
				regionLabelKey:   region,
				hostnameLabelKey: "selector-test"},
				MatchExpressions: []SelectorRequirements{
					{Key: hostnameLabelKey,
						Operator: "In",
						Values:   []string{"selector-test"},
					},
				}}}
	}

	tests := []struct {
		title string
		nodes []loadbalancers.Node
		peers []Peer
	}{
		{title: "empty", nodes: []loadbalancers.Node{}, peers: nil},
		{title: "single node",
			nodes: []loadbalancers.Node{
				{
					Name:     "test-name",
					LocalASN: 12345,
					PeerASN:  23456,
					Peers: []loadbalancers.Peer{
						{Address: "46.166.166.122", Port: 179},
						{Address: "46.166.166.123", Port: 180},
					},
					Region: "test-region",
				},
			}, peers: []Peer{
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.122",
					Port:          179,
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: nodeSelectors("test-region"),
					Name:          "test-region-0",
					EBGPMultiHop:  true,
				},
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.123",
					Port:          180,
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: nodeSelectors("test-region"),
					Name:          "test-region-1",
					EBGPMultiHop:  true,
				},
			}},
		{title: "multi node",
			nodes: []loadbalancers.Node{
				{
					Name:     "test-first-name",
					LocalASN: 12345,
					PeerASN:  23456,
					Peers: []loadbalancers.Peer{
						{Address: "46.166.166.122", Port: 179},
						{Address: "46.166.166.123", Port: 180},
					},
					Region: "test-first-region",
				},
				{
					Name:     "test-second-name",
					LocalASN: 34567,
					PeerASN:  45678,
					Peers: []loadbalancers.Peer{
						{Address: "195.189.96.10", Port: 179},
						{Address: "195.189.96.11", Port: 180},
					},
					Region: "test-second-region",
				},
				{
					Name:     "test-third-name",
					LocalASN: 34567,
					PeerASN:  45678,
					Peers: []loadbalancers.Peer{
						{Address: "195.189.96.10", Port: 179},
						{Address: "195.189.96.11", Port: 180},
					},
					Region: "test-second-region",
				},
			}, peers: []Peer{
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.122",
					Port:          179,
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: nodeSelectors("test-first-region"),
					Name:          "test-first-region-0",
					EBGPMultiHop:  true,
				},
				{
					MyASN:         12345,
					ASN:           23456,
					Addr:          "46.166.166.123",
					Port:          180,
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: nodeSelectors("test-first-region"),
					Name:          "test-first-region-1",
					EBGPMultiHop:  true,
				},
				{
					MyASN:         34567,
					ASN:           45678,
					Addr:          "195.189.96.10",
					Port:          179,
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: nodeSelectors("test-second-region"),
					Name:          "test-second-region-0",
					EBGPMultiHop:  true,
				},
				{
					MyASN:         34567,
					ASN:           45678,
					Addr:          "195.189.96.11",
					Port:          180,
					HoldTime:      holdTime,
					RouterID:      "",
					NodeSelectors: nodeSelectors("test-second-region"),
					Name:          "test-second-region-1",
					EBGPMultiHop:  true,
				},
			}},
	}

	ls, err := metav1.ParseToLabelSelector(
		"kubernetes.io/hostname==selector-test," +
			"kubernetes.io/hostname in (selector-test)")
	if err != nil {
		t.Fatalf("invalid label selector: %v", err)
	}
	f := NewPeersFromNodesFRRFunc(*ls)

	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := f(tt.nodes)
			want := tt.peers

			assertPeersEqual(t, got, want)
		})
	}
}

func TestParseConfigURL(t *testing.T) {
	tests := []struct {
		rawConfig string
		namespace string
		mode      BGPPeerMode
		err       error
	}{
		{
			rawConfig: "metallb:///",
			namespace: "",
			mode:      BGPPeerModeNative,
			err:       nil,
		},
		{
			rawConfig: "metallb:///foonamespace/",
			namespace: "foonamespace",
			mode:      BGPPeerModeNative,
			err:       nil,
		},
		{
			rawConfig: "metallb:///?bgp-peer-mode=frr",
			namespace: "",
			mode:      BGPPeerModeFRR,
			err:       nil,
		},
		{
			rawConfig: "metallb:///test?bgp-peer-mode=frr",
			namespace: "test",
			mode:      BGPPeerModeFRR,
			err:       nil,
		},
		{
			rawConfig: "metallb:///?bgp-peer-mode=native",
			namespace: "",
			mode:      BGPPeerModeNative,
			err:       nil,
		},
		{
			rawConfig: "metallb:///test?bgp-peer-mode=native",
			namespace: "test",
			mode:      BGPPeerModeNative,
			err:       nil,
		},
		{
			rawConfig: "metallb:///?bgp-peer-mode=none",
			namespace: "",
			mode:      BGPPeerModeNone,
			err:       nil,
		},
		{
			rawConfig: "metallb:///test?bgp-peer-mode=none",
			namespace: "test",
			mode:      BGPPeerModeNone,
			err:       nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.rawConfig, func(t *testing.T) {
			url, err := url.Parse(tt.rawConfig)
			if err != nil {
				t.Fatalf("invalid metallb config url: %s", tt.rawConfig)
			}

			namespace, mode, err := ParseConfigURL(url)

			if err != tt.err {
				t.Errorf("error: %q, want %q", err, tt.err)
			}

			if namespace != tt.namespace {
				t.Errorf("namespace: %q, want %q", namespace, tt.namespace)
			}

			if mode != tt.mode {
				t.Errorf("bgp peer mode: %q, want %q", mode, tt.mode)
			}
		})
	}
}
