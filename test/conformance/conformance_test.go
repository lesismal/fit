// Package conformance runs the Gateway API's conformance suite against a
// cluster whose GatewayClass, named by -gateway-class, fit serves: run.sh
// starts fit and runs it with the profile fit claims.
package conformance

import (
	"os"
	"testing"

	"sigs.k8s.io/gateway-api/conformance"
)

// TestConformance runs the suite with its own flags (-gateway-class,
// -conformance-profiles, -report-output and the rest). It needs KUBECONFIG,
// the cluster's kubeconfig; it is skipped without it.
func TestConformance(t *testing.T) {
	if os.Getenv("KUBECONFIG") == "" {
		t.Skip("KUBECONFIG is not set")
	}
	conformance.RunConformance(t)
}
