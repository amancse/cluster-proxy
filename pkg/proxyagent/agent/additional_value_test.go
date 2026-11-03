package agent

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakekube "k8s.io/client-go/kubernetes/fake"

	addonv1beta1 "open-cluster-management.io/api/addon/v1beta1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	proxyv1alpha1 "open-cluster-management.io/cluster-proxy/pkg/apis/proxy/v1alpha1"
	"open-cluster-management.io/cluster-proxy/pkg/common"
	"open-cluster-management.io/cluster-proxy/pkg/constant"

	fakeruntime "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetNodeSelector(t *testing.T) {
	testcases := []struct {
		name string
		// input}
		cluster *clusterv1.ManagedCluster
		err     error
		expect  map[string]string
	}{
		{
			name:    "managedCluster is not local-cluster, expect empty nodeSelector",
			cluster: newCluster("cluster", false),
			expect:  map[string]string{},
		},
		{
			name: "managedCluster is local-cluster, but no annotation, expect empty nodeSelector",
			cluster: newClusterWithLabelsAnnotations("cluster", false,
				map[string]string{common.SelfManagedClusterLabelKey: "true"}, nil),
			expect: map[string]string{},
		},
		{
			name: "managedCluster is local-cluster with incorrect annotation, expect err",
			cluster: newClusterWithLabelsAnnotations("cluster", false,
				map[string]string{common.SelfManagedClusterLabelKey: "true"},
				map[string]string{
					annotationNodeSelector: "kubernetes.io/os=linux",
				}),
			err: fmt.Errorf("incorrect annotation"),
		},
		{
			name: "managedCluster is local-cluster with correct annotation, expect nodeSelector",
			cluster: newClusterWithLabelsAnnotations("local-cluster", false,
				map[string]string{common.SelfManagedClusterLabelKey: "true"},
				map[string]string{
					annotationNodeSelector: `{"kubernetes.io/os":"linux"}`,
				}),
			expect: map[string]string{
				"kubernetes.io/os": "linux",
			},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			actual, err := getNodeSelector(testcase.cluster)
			if err != nil && testcase.err == nil {
				t.Errorf("expected no error, but got %v", err)
			}
			// compare actual and expected map
			for k, v := range testcase.expect {
				if actual[k] != v {
					t.Errorf("expected %v, but got %v", testcase.expect, actual)
				}
			}
			for k, v := range actual {
				if testcase.expect[k] != v {
					t.Errorf("expected %v, but got %v", testcase.expect, actual)
				}
			}
		})
	}
}

func newClusterWithLabelsAnnotations(name string, accepted bool, labels map[string]string, annotations map[string]string) *clusterv1.ManagedCluster {
	return &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: clusterv1.ManagedClusterSpec{
			HubAcceptsClient: accepted,
		},
	}
}

// TestGetClusterProxyAdditionalValueFunc_AdditionalServiceProxyArgs verifies that
// AdditionalServiceProxyArgs set on ManagedProxyConfiguration are wired through to
// the Helm values so the service-proxy sidecar receives them as container args.
//
// Previously the field existed in the Go type and the Helm chart template but the
// glue code in additional_values.go never read it, so the custom args were lost.
func TestGetClusterProxyAdditionalValueFunc_AdditionalServiceProxyArgs(t *testing.T) {
	tests := []struct {
		name               string
		additionalArgs     []string
		expectKey          bool   // whether "additionalServiceProxyArgs" should appear in values
		expectLen          int    // expected length of the slice in values
	}{
		{
			name:           "no additionalServiceProxyArgs — key absent from values",
			additionalArgs: nil,
			expectKey:      false,
		},
		{
			name:           "empty additionalServiceProxyArgs — key absent from values",
			additionalArgs: []string{},
			expectKey:      false,
		},
		{
			name: "QPS and Burst args are wired through to values",
			additionalArgs: []string{
				"--kube-api-qps=100",
				"--kube-api-burst=200",
			},
			expectKey: true,
			expectLen: 2,
		},
		{
			name: "token-review-cache-ttl arg is wired through to values",
			additionalArgs: []string{
				"--token-review-cache-ttl=30s",
			},
			expectKey: true,
			expectLen: 1,
		},
		{
			name: "multiple tuning args are all wired",
			additionalArgs: []string{
				"--kube-api-qps=100",
				"--kube-api-burst=200",
				"--token-review-cache-ttl=30s",
			},
			expectKey: true,
			expectLen: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Build a ManagedProxyConfiguration with the test args.
			proxyConfig := &proxyv1alpha1.ManagedProxyConfiguration{
				ObjectMeta: metav1.ObjectMeta{
					Name: ManagedClusterConfigurationName,
				},
				Spec: proxyv1alpha1.ManagedProxyConfigurationSpec{
					ProxyAgent: proxyv1alpha1.ManagedProxyConfigurationProxyAgent{
						Image:                    "quay.io/test/cluster-proxy:latest",
						AdditionalServiceProxyArgs: tc.additionalArgs,
					},
					ProxyServer: proxyv1alpha1.ManagedProxyConfigurationProxyServer{
						Namespace: "test",
						Entrypoint: &proxyv1alpha1.ManagedProxyConfigurationProxyServerEntrypoint{
							Type: proxyv1alpha1.EntryPointTypeHostname,
							Hostname: &proxyv1alpha1.EntryPointHostname{
								Value: "proxy.example.com",
							},
						},
					},
				},
			}

			// Build a fake runtime client with the proxy config.
			scheme := runtime.NewScheme()
			if err := proxyv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatalf("failed to add scheme: %v", err)
			}
			runtimeClient := fakeruntime.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(proxyConfig).
				Build()

			// Build a fake kube client with the server cert secret (required by the func).
			serverCertSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      constant.ServerCertSecretName,
					Namespace: "test-ns",
				},
				Data: map[string][]byte{
					"tls.crt": []byte("fake-cert"),
					"tls.key": []byte("fake-key"),
				},
			}
			kubeClient := fakekube.NewSimpleClientset(serverCertSecret)

			// Build the cluster and addon objects required by the values func signature.
			cluster := newClusterForValueTest("local-cluster", true)
			addon := &addonv1beta1.ManagedClusterAddOn{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-proxy",
					Namespace: "local-cluster",
				},
			}

			// Invoke the function under test with enableServiceProxy=true so the
			// additionalServiceProxyArgs branch is exercised.
			valuesFunc := GetClusterProxyAdditionalValueFunc(
				runtimeClient,
				kubeClient,
				"test-ns",
				true,  // enableServiceProxy
				false, // enableNetworkPolicies
			)
			values, err := valuesFunc(cluster, addon)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, exists := values["additionalServiceProxyArgs"]
			if tc.expectKey && !exists {
				t.Errorf("expected values[\"additionalServiceProxyArgs\"] to be present, but it was absent")
				return
			}
			if !tc.expectKey && exists {
				t.Errorf("expected values[\"additionalServiceProxyArgs\"] to be absent, but got %v", got)
				return
			}
			if tc.expectKey {
				args, ok := got.([]string)
				if !ok {
					t.Fatalf("values[\"additionalServiceProxyArgs\"] has wrong type %T", got)
				}
				if len(args) != tc.expectLen {
					t.Errorf("expected %d args, got %d: %v", tc.expectLen, len(args), args)
				}
				for i, want := range tc.additionalArgs {
					if args[i] != want {
						t.Errorf("arg[%d]: want %q, got %q", i, want, args[i])
					}
				}
			}
		})
	}
}

func newClusterForValueTest(name string, accepted bool) *clusterv1.ManagedCluster {
	return &clusterv1.ManagedCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       clusterv1.ManagedClusterSpec{HubAcceptsClient: accepted},
	}
}
