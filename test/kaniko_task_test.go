// +build e2e

/*
Copyright 2019 The Tekton Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1alpha1"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	resources "github.com/tektoncd/pipeline/pkg/apis/resource/v1alpha1"
	tb "github.com/tektoncd/pipeline/test/builder"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	knativetest "knative.dev/pkg/test"
)

const (
	kanikoTaskName          = "kanikotask"
	kanikoTaskRunName       = "kanikotask-run"
	kanikoGitResourceName   = "go-example-git"
	kanikoImageResourceName = "go-example-image"
	// This is a random revision chosen on 10/11/2019
	revision = "1c9d566ecd13535f93789595740f20932f655905"
)

// TestTaskRun is an integration test that will verify a TaskRun using kaniko
func TestKanikoTaskRun(t *testing.T) {
	if skipRootUserTests {
		t.Skip("Skip test as skipRootUserTests set to true")
	}

	c, namespace := setup(t, withRegistry)
	t.Parallel()

	repo := fmt.Sprintf("registry.%s:5000/kanikotasktest", namespace)

	knativetest.CleanupOnInterrupt(func() { tearDown(t, c, namespace) }, t.Logf)
	defer tearDown(t, c, namespace)

	t.Logf("Creating Git PipelineResource %s", kanikoGitResourceName)
	if _, err := c.PipelineResourceClient.Create(getGitResource()); err != nil {
		t.Fatalf("Failed to create Pipeline Resource `%s`: %s", kanikoGitResourceName, err)
	}

	t.Logf("Creating Image PipelineResource %s", repo)
	if _, err := c.PipelineResourceClient.Create(getImageResource(repo)); err != nil {
		t.Fatalf("Failed to create Pipeline Resource `%s`: %s", kanikoGitResourceName, err)
	}

	t.Logf("Creating Task %s", kanikoTaskName)
	if _, err := c.TaskClient.Create(getTask(repo, namespace)); err != nil {
		t.Fatalf("Failed to create Task `%s`: %s", kanikoTaskName, err)
	}

	t.Logf("Creating TaskRun %s", kanikoTaskRunName)
	if _, err := c.TaskRunClient.Create(getTaskRun(namespace)); err != nil {
		t.Fatalf("Failed to create TaskRun `%s`: %s", kanikoTaskRunName, err)
	}

	// Verify status of TaskRun (wait for it)

	if err := WaitForTaskRunState(c, kanikoTaskRunName, Succeed(kanikoTaskRunName), "TaskRunCompleted"); err != nil {
		t.Errorf("Error waiting for TaskRun %s to finish: %s", kanikoTaskRunName, err)
	}

	tr, err := c.TaskRunClient.Get(kanikoTaskRunName, metav1.GetOptions{})
	if err != nil {
		t.Errorf("Error retrieving taskrun: %s", err)
	}
	digest := ""
	commit := ""
	for _, rr := range tr.Status.ResourcesResult {
		switch rr.Key {
		case "digest":
			digest = rr.Value
		case "commit":
			commit = rr.Value
		}
		// Every resource should have a ref with a name
		if rr.ResourceRef.Name == "" {
			t.Errorf("Resource ref not set for %v in TaskRun: %v", rr, tr)
		}
	}
	if digest == "" {
		t.Errorf("Digest not found in TaskRun.Status: %v", tr.Status)
	}
	if commit == "" {
		t.Errorf("Commit not found in TaskRun.Status: %v", tr.Status)
	}

	if revision != commit {
		t.Fatalf("Expected remote commit to match local revision: %s, %s", commit, revision)
	}

	// match the local digest, which is first capture group against the remote image
	remoteDigest, err := getRemoteDigest(t, c, namespace, repo)
	if err != nil {
		t.Fatalf("Expected to get digest for remote image %s: %v", repo, err)
	}
	if d := cmp.Diff(digest, remoteDigest); d != "" {
		t.Fatalf("Expected local digest %s to match remote digest %s: %s", digest, remoteDigest, d)
	}
}

func getGitResource() *v1alpha1.PipelineResource {
	return tb.PipelineResource(kanikoGitResourceName, tb.PipelineResourceSpec(
		v1alpha1.PipelineResourceTypeGit,
		tb.PipelineResourceSpecParam("Url", "https://github.com/GoogleContainerTools/kaniko"),
		tb.PipelineResourceSpecParam("Revision", revision),
	))
}

func getImageResource(repo string) *v1alpha1.PipelineResource {
	return tb.PipelineResource(kanikoImageResourceName, tb.PipelineResourceSpec(
		v1alpha1.PipelineResourceTypeImage,
		tb.PipelineResourceSpecParam("url", repo),
	))
}

func getTask(repo, namespace string) *v1beta1.Task {
	root := int64(0)
	return &v1beta1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: kanikoTaskName, Namespace: namespace},
		Spec: v1beta1.TaskSpec{
			Resources: &v1beta1.TaskResources{
				Inputs: []v1beta1.TaskResource{{ResourceDeclaration: v1beta1.ResourceDeclaration{
					Name: "gitsource", Type: resources.PipelineResourceTypeGit,
				}}},
				Outputs: []v1beta1.TaskResource{{ResourceDeclaration: v1beta1.ResourceDeclaration{
					Name: "builtImage", Type: resources.PipelineResourceTypeImage,
				}}},
			},
			Steps: []v1beta1.Step{{Container: corev1.Container{
				Name:  "kaniko",
				Image: "gcr.io/kaniko-project/executor:v0.17.1",
				Args: []string{
					"--dockerfile=/workspace/gitsource/integration/dockerfiles/Dockerfile_test_label",
					fmt.Sprintf("--destination=%s", repo),
					"--context=/workspace/gitsource",
					"--oci-layout-path=/workspace/output/builtImage",
					"--insecure",
					"--insecure-pull",
					"--insecure-registry=registry." + namespace + ":5000/",
				},
				SecurityContext: &corev1.SecurityContext{
					RunAsUser: &root,
				},
			}}},
			Sidecars: []v1beta1.Sidecar{{Container: corev1.Container{
				Name:  "registry",
				Image: "registry",
			}}},
		},
	}
}

func getTaskRun(namespace string) *v1beta1.TaskRun {
	return &v1beta1.TaskRun{
		ObjectMeta: metav1.ObjectMeta{Name: kanikoTaskRunName, Namespace: namespace},
		Spec: v1beta1.TaskRunSpec{
			TaskRef: &v1beta1.TaskRef{Name: kanikoTaskName},
			Timeout: &metav1.Duration{Duration: 2 * time.Minute},
			Resources: &v1beta1.TaskRunResources{
				Inputs: []v1beta1.TaskResourceBinding{{PipelineResourceBinding: v1beta1.PipelineResourceBinding{
					Name: "gitsource", ResourceRef: &v1beta1.PipelineResourceRef{Name: kanikoGitResourceName},
				}}},
				Outputs: []v1beta1.TaskResourceBinding{{PipelineResourceBinding: v1beta1.PipelineResourceBinding{
					Name: "builtImage", ResourceRef: &v1beta1.PipelineResourceRef{Name: kanikoImageResourceName},
				}}},
			},
		},
	}
}

// getRemoteDigest starts a pod to query the registry from the namespace itself, using skopeo (and jq).
// The reason we have to do that is because the image is pushed on a local registry that is not exposed
// to the "outside" of the test, this means it can be query by the test itself. It can only be query from
// a pod in the namespace. skopeo is able to do that query and we use jq to extract the digest from its
// output. The image used for this pod is build in the tektoncd/plumbing repository.
func getRemoteDigest(t *testing.T, c *clients, namespace, image string) (string, error) {
	t.Helper()
	podName := "skopeo-jq"
	if _, err := c.KubeClient.Kube.CoreV1().Pods(namespace).Create(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      podName,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    "skopeo",
				Image:   "gcr.io/tekton-releases/dogfooding/skopeo:latest",
				Command: []string{"/bin/sh", "-c"},
				Args:    []string{"skopeo inspect --tls-verify=false docker://" + image + ":latest| jq '.Digest'"},
			}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}); err != nil {
		t.Fatalf("Failed to create the skopeo-jq pod: %v", err)
	}
	if err := WaitForPodState(c, podName, namespace, func(pod *corev1.Pod) (bool, error) {
		return pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed", nil
	}, "PodContainersTerminated"); err != nil {
		t.Fatalf("Error waiting for Pod %q to terminate: %v", podName, err)
	}
	logs, err := getContainerLogsFromPod(c.KubeClient.Kube, podName, "skopeo", namespace)
	if err != nil {
		t.Fatalf("Could not get logs for pod %s: %s", podName, err)
	}
	return strings.TrimSpace(strings.ReplaceAll(logs, "\"", "")), nil
}
