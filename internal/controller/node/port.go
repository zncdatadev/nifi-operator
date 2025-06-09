package node

import corev1 "k8s.io/api/core/v1"

var (
	Ports = []corev1.ContainerPort{
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
)

func getPort(name string) int32 {
	for _, port := range Ports {
		if port.Name == name {
			return port.ContainerPort
		}
	}
	return 0
}
