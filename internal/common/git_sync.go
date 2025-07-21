package common

import (
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
)

type GitSyncResources struct {
	GitSyncContainers     []corev1.Container
	GitSyncInitContainers []corev1.Container
	GitSyncVolumes        []corev1.Volume
	GitSyncVolumeMounts   []corev1.VolumeMount
	GitContentFolders     []string
}

func NewGitSyncResources(
	gitSyncs []nifiv1alpha1.GitSyncSpec,
	image *util.Image,
) *GitSyncResources {
	// TODO: Implement the logic to create GitSync resources based on the provided gitSyncs and image.
	panic("NewGitSyncResources is not implemented yet")
}
