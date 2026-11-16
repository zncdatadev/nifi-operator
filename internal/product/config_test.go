package product

import (
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/util"
)

// gitSyncCR mirrors the e2e git-sync suite's NifiCluster (static auth,
// k8s-native clustering, one role group) — the CR the Gen 2 parity baseline
// was captured from.
func gitSyncCR() *nifiv1alpha1.NifiCluster {
	return &nifiv1alpha1.NifiCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nificluster-git-sync",
			Namespace: "parity-baseline",
		},
		Spec: nifiv1alpha1.NifiClusterSpec{
			Image: &nifiv1alpha1.ImageSpec{
				Repo:           nifiv1alpha1.DefaultRepository,
				ProductVersion: "2.4.0",
			},
			ClusterConfig: &nifiv1alpha1.ClusterConfigSpec{
				Authentication: []nifiv1alpha1.AuthenticationSpec{
					{AuthenticationClass: "nifi-git-sync-admin"},
				},
				SensitiveProperties: &nifiv1alpha1.SensitivePropertiesSpec{
					KeySecret:    "nifi-git-sync-sensitive-key",
					AutoGenerate: true,
					Algorithm:    "NIFI_ARGON2_AES_GCM_256",
				},
				CreateReportingTaskJob: &nifiv1alpha1.CreateReportingTaskJobSpec{Enable: true},
			},
			Nodes: &nifiv1alpha1.NodesSpec{
				RoleGroups: map[string]nifiv1alpha1.RoleGroupSpec{
					"default": {Replicas: ptr.To(int32(1))},
				},
			},
		},
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(data)
}

// TestBootstrapConfParity locks the rendered bootstrap.conf to the Gen 2
// baseline captured from a live cluster.
func TestBootstrapConfParity(t *testing.T) {
	cr := gitSyncCR()

	rendered, err := util.PlainPropertiesMarshaler{}.Marshal(BootstrapConfig(cr, "default", DefaultGracefulShutdownTimeout))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if want := fixture(t, "bootstrap.conf"); rendered != want {
		t.Errorf("bootstrap.conf drifted from Gen 2 baseline:\n--- want ---\n%s\n--- got ---\n%s", want, rendered)
	}
}

// TestNifiPropertiesParity locks the rendered nifi.properties to the Gen 2
// baseline (static auth contributes no extra properties, so the product layer
// is the whole file).
func TestNifiPropertiesParity(t *testing.T) {
	cr := gitSyncCR()

	rendered, err := util.PlainPropertiesMarshaler{}.Marshal(NifiProperties(cr))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if want := fixture(t, "nifi.properties"); rendered != want {
		t.Errorf("nifi.properties drifted from Gen 2 baseline:\n--- want ---\n%s\n--- got ---\n%s", want, rendered)
	}
}

// TestStateManagementXMLParity locks the rendered state-management.xml to the
// Gen 2 baseline.
func TestStateManagementXMLParity(t *testing.T) {
	cr := gitSyncCR()

	if want, got := fixture(t, "state-management.xml"), StateManagementXML(cr.Spec.ClusterConfig); got != want {
		t.Errorf("state-management.xml drifted from Gen 2 baseline:\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}
