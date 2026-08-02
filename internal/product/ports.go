package product

import corev1 "k8s.io/api/core/v1"

// Ports is the NiFi container port set. Order matters twice: it is the Gen 2
// rendered order, and the framework attaches its TCP readiness probe to the
// first entry (http — the web port that means "this node can serve").
var Ports = []corev1.ContainerPort{
	{
		Name:          "http",
		ContainerPort: 8088,
	},
	{
		Name:          "https",
		ContainerPort: 9443,
	},
	{
		Name:          "protocol",
		ContainerPort: 9088,
	},
	{
		Name:          "balance",
		ContainerPort: 6243,
	},
	{
		Name:          "metrics",
		ContainerPort: 8081,
	},
}

// GetPort returns the container port number for the named NiFi port.
func GetPort(name string) int32 {
	for _, port := range Ports {
		if port.Name == name {
			return port.ContainerPort
		}
	}
	return 0
}
