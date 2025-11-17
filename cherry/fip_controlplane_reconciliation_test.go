package cherry

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestServerNameFromConfig(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"http://kubernetes.default.svc:80", "kubernetes.default.svc"},
		{"https://kubernetes.default.svc:443", "kubernetes.default.svc"},
		{"https://kubernetes.default.svc:443/base/path", "kubernetes.default.svc"},
		{"http://kubernetes.default.svc", "kubernetes.default.svc"},
		{"https://kubernetes.default.svc", "kubernetes.default.svc"},
		{"https://kubernetes.default.svc/base/path", "kubernetes.default.svc"},
		{"kubernetes.default.svc:443", "kubernetes.default.svc"},
		{"kubernetes.default.svc", "kubernetes.default.svc"},
		{"https://1.2.3.4:6443", "1.2.3.4"},
		{"http://1.2.3.4:6443", "1.2.3.4"},
		{"1.2.3.4:6443", "1.2.3.4"},
		{"1.2.3.4", "1.2.3.4"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			cfg := rest.Config{Host: tt.in}
			if got, want := serverNameFromConfig(&cfg), tt.out; got != want {
				t.Errorf("hostname %q, want %q", got, want)
			}
		})
	}
}
