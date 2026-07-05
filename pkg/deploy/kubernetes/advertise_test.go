package kubernetes

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func cornusService(mutate func(*corev1.Service)) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cornus",
			Namespace: "default",
			Labels:    map[string]string{"app": "cornus"},
		},
		Spec: corev1.ServiceSpec{
			Type:      corev1.ServiceTypeClusterIP,
			ClusterIP: "10.0.0.5",
			Ports:     []corev1.ServicePort{{Name: "http", Port: 5000}},
		},
	}
	if mutate != nil {
		mutate(svc)
	}
	return svc
}

func TestAdvertisedRegistry(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		objs  []*corev1.Service
		wantH string
	}{
		{
			name: "nodeport advertises localhost:nodeport",
			objs: []*corev1.Service{cornusService(func(s *corev1.Service) {
				s.Spec.Type = corev1.ServiceTypeNodePort
				s.Spec.Ports[0].NodePort = 30500
			})},
			wantH: "localhost:30500",
		},
		{
			name: "loadbalancer hostname wins",
			objs: []*corev1.Service{cornusService(func(s *corev1.Service) {
				s.Spec.Type = corev1.ServiceTypeLoadBalancer
				s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "lb.example", IP: "1.2.3.4"}}
			})},
			wantH: "lb.example:5000",
		},
		{
			name: "loadbalancer ip when no hostname",
			objs: []*corev1.Service{cornusService(func(s *corev1.Service) {
				s.Spec.Type = corev1.ServiceTypeLoadBalancer
				s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
			})},
			wantH: "1.2.3.4:5000",
		},
		{
			name:  "clusterip is not auto-advertised",
			objs:  []*corev1.Service{cornusService(nil)},
			wantH: "",
		},
		{
			name:  "no service",
			objs:  nil,
			wantH: "",
		},
		{
			name: "headless-only is skipped",
			objs: []*corev1.Service{cornusService(func(s *corev1.Service) {
				s.Spec.ClusterIP = corev1.ClusterIPNone
			})},
			wantH: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			for _, o := range tt.objs {
				if _, err := cs.CoreV1().Services(o.Namespace).Create(ctx, o, metav1.CreateOptions{}); err != nil {
					t.Fatalf("seed service: %v", err)
				}
			}
			b := NewWithClient(cs, "default")
			info, err := b.AdvertisedRegistry(ctx)
			if err != nil {
				t.Fatalf("AdvertisedRegistry: %v", err)
			}
			if info.RegistryHost != tt.wantH {
				t.Fatalf("host = %q, want %q", info.RegistryHost, tt.wantH)
			}
			if tt.wantH != "" && info.RegistryScheme != "http" {
				t.Fatalf("scheme = %q, want http", info.RegistryScheme)
			}
		})
	}
}

func TestAdvertisedRegistryAmbiguous(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	for _, name := range []string{"cornus", "cornus-extra"} {
		svc := cornusService(func(s *corev1.Service) {
			s.Name = name
			s.Spec.Type = corev1.ServiceTypeNodePort
			s.Spec.Ports[0].NodePort = 30500
		})
		if _, err := cs.CoreV1().Services("default").Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	b := NewWithClient(cs, "default")
	info, err := b.AdvertisedRegistry(ctx)
	if err != nil {
		t.Fatalf("AdvertisedRegistry: %v", err)
	}
	if info.RegistryHost != "" {
		t.Fatalf("ambiguous match should yield empty, got %q", info.RegistryHost)
	}
}
