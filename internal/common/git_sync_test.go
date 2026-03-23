package common

import (
	"strings"
	"testing"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
)

func makeTestImage() *util.Image {
	return &util.Image{
		ProductName:     "nifi",
		KubedoopVersion: "0.0.1",
		ProductVersion:  "2.2.0",
	}
}

func TestNewGitSyncResources_Empty(t *testing.T) {
	resources, err := NewGitSyncResources(nil, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resources.IsGitSyncEnabled() {
		t.Error("expected IsGitSyncEnabled to be false with no git syncs")
	}
	if len(resources.GitSyncContainers) != 0 {
		t.Error("expected no GitSyncContainers")
	}
	if len(resources.GitSyncInitContainers) != 0 {
		t.Error("expected no GitSyncInitContainers")
	}
	if len(resources.GitSyncVolumes) != 0 {
		t.Error("expected no GitSyncVolumes")
	}
	if len(resources.GitSyncVolumeMounts) != 0 {
		t.Error("expected no GitSyncVolumeMounts")
	}
	if len(resources.GitContentFolders) != 0 {
		t.Error("expected no GitContentFolders")
	}
}

func TestNewGitSyncResources_SingleEntry_Defaults(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{
			Repo:      "https://github.com/example/repo",
			Branch:    "main",
			Depth:     1,
			GitFolder: "/",
			Wait:      "20s",
		},
	}

	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !resources.IsGitSyncEnabled() {
		t.Error("expected IsGitSyncEnabled to be true")
	}
	if len(resources.GitSyncContainers) != 1 {
		t.Errorf("expected 1 GitSyncContainer, got %d", len(resources.GitSyncContainers))
	}
	if len(resources.GitSyncInitContainers) != 1 {
		t.Errorf("expected 1 GitSyncInitContainer, got %d", len(resources.GitSyncInitContainers))
	}
	if len(resources.GitSyncVolumes) != 1 {
		t.Errorf("expected 1 GitSyncVolume, got %d", len(resources.GitSyncVolumes))
	}
	if len(resources.GitSyncVolumeMounts) != 1 {
		t.Errorf("expected 1 GitSyncVolumeMount, got %d", len(resources.GitSyncVolumeMounts))
	}
	if len(resources.GitContentFolders) != 1 {
		t.Errorf("expected 1 GitContentFolder, got %d", len(resources.GitContentFolders))
	}

	// Naming conventions.
	if resources.GitSyncContainers[0].Name != "git-sync-0" {
		t.Errorf("unexpected container name: %s", resources.GitSyncContainers[0].Name)
	}
	if resources.GitSyncInitContainers[0].Name != "git-sync-0-init" {
		t.Errorf("unexpected init container name: %s", resources.GitSyncInitContainers[0].Name)
	}
	if resources.GitSyncVolumes[0].Name != "content-from-git-0" {
		t.Errorf("unexpected volume name: %s", resources.GitSyncVolumes[0].Name)
	}
	if resources.GitSyncVolumeMounts[0].MountPath != "/kubedoop/app/git-0" {
		t.Errorf("unexpected mount path: %s", resources.GitSyncVolumeMounts[0].MountPath)
	}
	// gitFolder "/" strips to "" so the content folder is just .../current
	if resources.GitContentFolders[0] != "/kubedoop/app/git-0/current" {
		t.Errorf("unexpected git content folder: %s", resources.GitContentFolders[0])
	}
}

func TestNewGitSyncResources_GitFolder_SubPath(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{
			Repo:      "https://github.com/example/components",
			Branch:    "develop",
			Depth:     3,
			GitFolder: "/processors",
			Wait:      "1m",
		},
	}

	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "/kubedoop/app/git-0/current/processors"
	if resources.GitContentFolders[0] != expected {
		t.Errorf("expected git content folder %q, got %q", expected, resources.GitContentFolders[0])
	}
}

func TestNewGitSyncResources_CredentialsSecret(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{
			Repo:              "https://github.com/private/repo",
			Branch:            "main",
			Depth:             1,
			GitFolder:         "/",
			Wait:              "20s",
			CredentialsSecret: "my-git-secret",
		},
	}

	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := map[string]bool{}
	for _, e := range resources.GitSyncContainers[0].Env {
		switch e.Name {
		case "GITSYNC_USERNAME":
			if e.ValueFrom.SecretKeyRef.Name != "my-git-secret" || e.ValueFrom.SecretKeyRef.Key != "user" {
				t.Errorf("GITSYNC_USERNAME has wrong secret ref: %+v", e.ValueFrom)
			}
			found["GITSYNC_USERNAME"] = true
		case "GITSYNC_PASSWORD":
			if e.ValueFrom.SecretKeyRef.Name != "my-git-secret" || e.ValueFrom.SecretKeyRef.Key != "password" {
				t.Errorf("GITSYNC_PASSWORD has wrong secret ref: %+v", e.ValueFrom)
			}
			found["GITSYNC_PASSWORD"] = true
		}
	}
	if !found["GITSYNC_USERNAME"] {
		t.Error("GITSYNC_USERNAME env var not found")
	}
	if !found["GITSYNC_PASSWORD"] {
		t.Error("GITSYNC_PASSWORD env var not found")
	}
}

func TestNewGitSyncResources_NoCredentials_NoEnvVars(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{Repo: "https://github.com/public/repo", Branch: "main", Depth: 1, GitFolder: "/", Wait: "20s"},
	}
	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resources.GitSyncContainers[0].Env) != 0 {
		t.Errorf("expected no env vars for public repo, got %v", resources.GitSyncContainers[0].Env)
	}
}

func TestNewGitSyncResources_OneTimeFlag(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{Repo: "https://github.com/example/repo", Branch: "main", Depth: 1, GitFolder: "/", Wait: "20s"},
	}
	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	initScript := resources.GitSyncInitContainers[0].Args[0]
	if !strings.Contains(initScript, "--one-time=true") {
		t.Error("init container script must include --one-time=true")
	}
	sidecarScript := resources.GitSyncContainers[0].Args[0]
	if !strings.Contains(sidecarScript, "--one-time=false") {
		t.Error("sidecar container script must include --one-time=false")
	}
}

func TestNewGitSyncResources_EmptyDirVolume(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{Repo: "https://github.com/example/repo", Branch: "main", Depth: 1, GitFolder: "/", Wait: "20s"},
	}
	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resources.GitSyncVolumes[0].VolumeSource.EmptyDir == nil {
		t.Error("expected EmptyDir volume source")
	}
}

func TestNewGitSyncResources_Resources(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{Repo: "https://github.com/example/repo", Branch: "main", Depth: 1, GitFolder: "/", Wait: "20s"},
	}
	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := resources.GitSyncContainers[0]
	cpuReq := c.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.String() != "100m" {
		t.Errorf("expected CPU request 100m, got %s", cpuReq.String())
	}
	cpuLim := c.Resources.Limits[corev1.ResourceCPU]
	if cpuLim.String() != "200m" {
		t.Errorf("expected CPU limit 200m, got %s", cpuLim.String())
	}
	memReq := c.Resources.Requests[corev1.ResourceMemory]
	if memReq.String() != "64Mi" {
		t.Errorf("expected memory request 64Mi, got %s", memReq.String())
	}
	memLim := c.Resources.Limits[corev1.ResourceMemory]
	if memLim.String() != "64Mi" {
		t.Errorf("expected memory limit 64Mi, got %s", memLim.String())
	}
}

func TestNewGitSyncResources_Multiple(t *testing.T) {
	gitSyncs := []nifiv1alpha1.GitSyncSpec{
		{Repo: "https://github.com/example/repo1", Branch: "main", Depth: 1, GitFolder: "/", Wait: "20s"},
		{Repo: "https://github.com/example/repo2", Branch: "develop", Depth: 2, GitFolder: "/nar", Wait: "30s"},
	}

	resources, err := NewGitSyncResources(gitSyncs, makeTestImage())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resources.GitSyncContainers) != 2 {
		t.Fatalf("expected 2 git-sync containers, got %d", len(resources.GitSyncContainers))
	}
	if resources.GitSyncContainers[1].Name != "git-sync-1" {
		t.Errorf("expected second container name git-sync-1, got %s", resources.GitSyncContainers[1].Name)
	}
	if resources.GitContentFolders[1] != "/kubedoop/app/git-1/current/nar" {
		t.Errorf("unexpected git content folder for second entry: %s", resources.GitContentFolders[1])
	}
}

func TestBuildGitSyncScript_ContainsSafeDirConfig(t *testing.T) {
	gs := &nifiv1alpha1.GitSyncSpec{
		Repo:      "https://github.com/example/repo",
		Branch:    "main",
		Depth:     1,
		GitFolder: "/",
		Wait:      "20s",
	}
	script := buildGitSyncScript(gs, false)
	if !strings.Contains(script, "safe.directory:/tmp/git") {
		t.Errorf("expected safe.directory config in script, got:\n%s", script)
	}
}

func TestBuildGitSyncScript_SidecarHasTrapFunctions(t *testing.T) {
	gs := &nifiv1alpha1.GitSyncSpec{
		Repo: "https://github.com/example/repo", Branch: "main", Depth: 1, GitFolder: "/", Wait: "20s",
	}
	script := buildGitSyncScript(gs, false)
	if !strings.Contains(script, "prepare_signal_handlers") {
		t.Error("sidecar script should contain prepare_signal_handlers")
	}
	if !strings.Contains(script, "wait_for_termination") {
		t.Error("sidecar script should contain wait_for_termination")
	}
}

func TestBuildGitSyncScript_InitIsOneShot(t *testing.T) {
	gs := &nifiv1alpha1.GitSyncSpec{
		Repo: "https://github.com/example/repo", Branch: "main", Depth: 1, GitFolder: "/", Wait: "20s",
	}
	script := buildGitSyncScript(gs, true)
	// Init script should NOT have the background & operator
	if strings.Contains(script, "prepare_signal_handlers") {
		t.Error("init script should not contain prepare_signal_handlers")
	}
	if strings.Contains(script, "wait_for_termination") {
		t.Error("init script should not contain wait_for_termination")
	}
}
