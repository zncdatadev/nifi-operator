package common

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
)

const (
	gitSyncContainerNamePrefix = "git-sync"
	gitSyncVolumeNamePrefix    = "content-from-git"
	gitSyncMountPathPrefix     = "/kubedoop/app/git"
	gitSyncRootDir             = "/tmp/git"
	gitSyncLink                = "current"
	gitSyncBinary              = "/kubedoop/git-sync"
	gitSyncSafeDirOption       = "safe.directory"
)

// GitSyncResources holds all Kubernetes resources generated from GitSyncSpec entries.
type GitSyncResources struct {
	// Sidecar containers providing continuous git synchronization.
	GitSyncContainers []corev1.Container
	// Init containers providing one-time git synchronization before the main container starts.
	GitSyncInitContainers []corev1.Container
	// EmptyDir volumes that hold the synchronized git content.
	GitSyncVolumes []corev1.Volume
	// Volume mounts to expose git content to the main NiFi container.
	GitSyncVolumeMounts []corev1.VolumeMount
	// Absolute paths inside the main container where the synced git content is available.
	GitContentFolders []string
}

// IsGitSyncEnabled returns true when at least one git-sync sidecar is configured.
func (r *GitSyncResources) IsGitSyncEnabled() bool {
	return len(r.GitSyncContainers) > 0
}

// NewGitSyncResources creates GitSyncResources from a list of GitSyncSpec entries.
// The generated containers use the same product image as NiFi, which must include
// the git-sync binary at /kubedoop/git-sync.
func NewGitSyncResources(
	gitSyncs []nifiv1alpha1.GitSyncSpec,
	image *util.Image,
) (*GitSyncResources, error) {
	resources := &GitSyncResources{}

	for i := range gitSyncs {
		gs := &gitSyncs[i]

		volumeName := fmt.Sprintf("%s-%d", gitSyncVolumeNamePrefix, i)
		mountPath := fmt.Sprintf("%s-%d", gitSyncMountPathPrefix, i)

		// Credentials env vars (only added when a secret is referenced).
		var envVars []corev1.EnvVar
		if gs.CredentialsSecret != "" {
			envVars = append(envVars,
				gitSyncEnvVarFromSecret("GITSYNC_USERNAME", gs.CredentialsSecret, "user"),
				gitSyncEnvVarFromSecret("GITSYNC_PASSWORD", gs.CredentialsSecret, "password"),
			)
		}

		// The git-sync containers mount the volume at the git-sync root path so
		// that git-sync can write its working directory there.
		containerVolumeMounts := []corev1.VolumeMount{
			{Name: volumeName, MountPath: gitSyncRootDir},
		}

		sidecarContainer := buildGitSyncContainer(
			fmt.Sprintf("%s-%d", gitSyncContainerNamePrefix, i),
			image, gs, false, envVars, containerVolumeMounts,
		)

		initContainer := buildGitSyncContainer(
			fmt.Sprintf("%s-%d-init", gitSyncContainerNamePrefix, i),
			image, gs, true, envVars, containerVolumeMounts,
		)

		volume := corev1.Volume{
			Name: volumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}

		// The main NiFi container sees the synced content at mountPath.
		gitContentVolumeMount := corev1.VolumeMount{
			Name:      volumeName,
			MountPath: mountPath,
		}

		// The actual content resides under: <mountPath>/current/<gitFolder>
		// git-sync creates a symlink named "current" pointing to the latest revision.
		gitFolder := strings.TrimPrefix(gs.GitFolder, "/")
		gitContentFolder := path.Join(mountPath, gitSyncLink, gitFolder)

		resources.GitSyncContainers = append(resources.GitSyncContainers, sidecarContainer)
		resources.GitSyncInitContainers = append(resources.GitSyncInitContainers, initContainer)
		resources.GitSyncVolumes = append(resources.GitSyncVolumes, volume)
		resources.GitSyncVolumeMounts = append(resources.GitSyncVolumeMounts, gitContentVolumeMount)
		resources.GitContentFolders = append(resources.GitContentFolders, gitContentFolder)
	}

	return resources, nil
}

func buildGitSyncContainer(
	name string,
	image *util.Image,
	gs *nifiv1alpha1.GitSyncSpec,
	oneTime bool,
	envVars []corev1.EnvVar,
	volumeMounts []corev1.VolumeMount,
) corev1.Container {
	return corev1.Container{
		Name:            name,
		Image:           image.String(),
		ImagePullPolicy: image.GetPullPolicy(),
		Command:         []string{"/bin/bash", "-x", "-euo", "pipefail", "-c"},
		Args:            []string{buildGitSyncScript(gs, oneTime)},
		Env:             envVars,
		VolumeMounts:    volumeMounts,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
	}
}

// buildGitSyncScript produces the shell script that is passed as a single -c
// argument to bash inside the git-sync container.
func buildGitSyncScript(gs *nifiv1alpha1.GitSyncSpec, oneTime bool) string {
	branch := gs.Branch
	if branch == "" {
		branch = "main"
	}
	depth := gs.Depth
	if depth == 0 {
		depth = 1
	}
	wait := gs.Wait
	if wait == "" {
		wait = "20s"
	}

	// Fixed arguments that the operator always sets.
	internalArgs := map[string]string{
		"--repo":     gs.Repo,
		"--ref":      branch,
		"--depth":    fmt.Sprintf("%d", depth),
		"--period":   wait,
		"--link":     gitSyncLink,
		"--root":     gitSyncRootDir,
		"--one-time": fmt.Sprintf("%t", oneTime),
	}

	// Internal git-config entries (safe.directory prevents ownership errors).
	internalGitConfigParts := []string{
		fmt.Sprintf("%s:%s", gitSyncSafeDirOption, gitSyncRootDir),
	}

	// Clone the user-supplied gitSyncConfig so we can mutate it safely.
	userConfig := make(map[string]string, len(gs.GitSyncConfig))
	for k, v := range gs.GitSyncConfig {
		userConfig[k] = v
	}

	// The user may supply additional --git-config entries; we append them.
	userGitConfig, hasUserGitConfig := userConfig["--git-config"]
	delete(userConfig, "--git-config")
	if hasUserGitConfig {
		internalGitConfigParts = append(internalGitConfigParts, userGitConfig)
	}
	gitConfigValue := fmt.Sprintf("'%s'", strings.Join(internalGitConfigParts, ","))

	// Merge user-supplied args, but silently ignore any key that we manage.
	finalArgs := make(map[string]string, len(internalArgs)+len(userConfig)+1)
	for k, v := range internalArgs {
		finalArgs[k] = v
	}
	for k, v := range userConfig {
		if _, reserved := internalArgs[k]; !reserved {
			finalArgs[k] = v
		}
	}
	finalArgs["--git-config"] = gitConfigValue

	// Sort keys for a deterministic, reproducible script.
	keys := make([]string, 0, len(finalArgs))
	for k := range finalArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var argParts []string
	for _, k := range keys {
		argParts = append(argParts, fmt.Sprintf("%s=%s", k, finalArgs[k]))
	}
	gitSyncCommand := fmt.Sprintf("%s %s", gitSyncBinary, strings.Join(argParts, " "))

	if oneTime {
		return gitSyncCommand
	}

	return fmt.Sprintf("%s\nprepare_signal_handlers\n%s &\nwait_for_termination $!",
		util.CommonBashTrapFunctions, gitSyncCommand)
}

func gitSyncEnvVarFromSecret(varName, secretName, secretKey string) corev1.EnvVar {
	return corev1.EnvVar{
		Name: varName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  secretKey,
			},
		},
	}
}
