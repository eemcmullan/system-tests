//nolint:lll // Compact test fixtures make object identity and failure cases visible together.
package sbrutils

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestRunCommandCapturesOutput(t *testing.T) {
	for _, exit := range []string{"0", "7"} {
		t.Run(exit, func(t *testing.T) {
			output, err := RunCommand(context.Background(), "sh", "-c", "printf stdout; printf stderr >&2; exit "+exit)
			if !strings.Contains(output, "stdout") || !strings.Contains(output, "stderr") {
				t.Fatalf("lost output: %q", output)
			}

			if (err == nil) != (exit == "0") {
				t.Fatalf("unexpected command error: %v", err)
			}
		})
	}
}

func TestRunCommandStdoutExcludesStderr(t *testing.T) {
	output, err := RunCommandStdout(context.Background(), "sh", "-c", "printf stdout; printf stderr >&2")
	if err != nil {
		t.Fatal(err)
	}

	if output != "stdout" {
		t.Fatalf("stdout leaked stderr content: %q", output)
	}

	_, err = RunCommandStdout(context.Background(), "sh", "-c", "printf stderr >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "stderr") {
		t.Fatalf("expected stderr folded into error, got: %v", err)
	}
}

func testClient(objects ...client.Object) client.WithWatch {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func testObject(kind, namespace, name string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion("operators.coreos.com/v1alpha1")
	object.SetKind(kind)
	object.SetName(name)
	object.SetNamespace(namespace)

	return object
}

func TestCleanPreflight(t *testing.T) {
	for _, kind := range []string{"ClusterServiceVersion", "Subscription", "InstallPlan", "Deployment", "DaemonSet"} {
		name := "storage-based-remediation.v0.3.0"

		t.Run(kind, func(t *testing.T) {
			objectName := "arbitrary-name"
			if kind == "Deployment" || kind == "DaemonSet" {
				objectName = name
			}

			object := testObject(kind, "elsewhere", objectName)
			if kind == "Deployment" || kind == "DaemonSet" {
				object.SetAPIVersion("apps/v1")
			}

			object.Object["spec"] = map[string]interface{}{"package": name}
			if err := CheckClean(context.Background(), testClient(object), "new-namespace"); err == nil {
				t.Fatal("accepted pre-existing installation")
			}
		})
	}

	ciDaemonSet := testObject("DaemonSet", "openshift-e2e-loki", "loki-promtail")
	ciDaemonSet.SetAPIVersion("apps/v1")

	ciDaemonSet.Object["spec"] = map[string]interface{}{"job": "storage-based-remediation-upgrade"}
	if err := CheckClean(context.Background(), testClient(ciDaemonSet), "new-namespace"); err != nil {
		t.Fatalf("unrelated CI DaemonSet must be accepted: %v", err)
	}

	api := interceptor.NewClient(testClient(), interceptor.Funcs{List: func(ctx context.Context, underlying client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if strings.Contains(list.GetObjectKind().GroupVersionKind().Group, "medik8s.io") {
			return &meta.NoKindMatchError{GroupKind: schema.GroupKind{Kind: "not installed"}}
		}

		return underlying.List(ctx, list, opts...)
	}})
	if err := CheckClean(context.Background(), api, "new-namespace"); err != nil {
		t.Fatalf("empty cluster must be accepted: %v", err)
	}

	for _, failure := range []error{errors.New("network unavailable"), apierrors.NewForbidden(schema.GroupResource{Resource: "storagebasedremediationconfigs"}, "", errors.New("denied"))} {
		api := interceptor.NewClient(testClient(), interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return failure }})
		if err := CheckClean(context.Background(), api, "new-namespace"); err == nil {
			t.Fatal("hid non-absence error")
		}
	}
}

func TestRejectedNamespaceNeverRunsCleanup(t *testing.T) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "existing", UID: "existing-uid"}}

	api := testClient(namespace)
	if err := CheckClean(context.Background(), api, namespace.Name); err == nil {
		t.Fatal("accepted existing namespace")
	}

	run := &OwnedRun{API: api, Namespace: namespace.Name, Token: "new-run", Packages: []string{"sbr"},
		CleanupPackage: func(context.Context, string, string, string) (string, error) {
			t.Fatal("cleanup ran after rejection")

			return "", nil
		}}
	if err := run.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := api.Get(context.Background(), client.ObjectKeyFromObject(namespace), namespace); err != nil {
		t.Fatal("existing namespace removed", err)
	}
}

func TestPreflightRejectsOrphanClusterObjects(t *testing.T) {
	role := testObject("ClusterRole", "", "orphan")
	role.SetAPIVersion("rbac.authorization.k8s.io/v1")
	role.SetLabels(map[string]string{"olm.owner.namespace": "owned"})

	if err := CheckClean(context.Background(), testClient(role), "owned"); err == nil {
		t.Fatal("accepted orphan OLM role")
	}
}

func TestCleanupPreservesReplacedNamespace(t *testing.T) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "replacement", Labels: map[string]string{RunLabel: "run"}}}
	api := testClient(namespace)

	run := &OwnedRun{API: api, Namespace: namespace.Name, NamespaceUID: "original", Token: "run", Packages: []string{"sbr"},
		CleanupPackage: func(context.Context, string, string, string) (string, error) {
			t.Fatal("cleaned replaced namespace")

			return "", nil
		}}
	if err := run.Cleanup(context.Background()); err == nil {
		t.Fatal("did not report identity conflict")
	}

	if err := api.Get(context.Background(), client.ObjectKeyFromObject(namespace), namespace); err != nil {
		t.Fatal("removed replacement", err)
	}
}

func TestPartialInstallCleanupContinuesAfterFailure(t *testing.T) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "namespace-uid", Labels: map[string]string{RunLabel: "run"}}}
	api := testClient(namespace)

	var attempted []string

	run := &OwnedRun{API: api, Namespace: namespace.Name, NamespaceUID: namespace.UID, Token: "run", Packages: []string{"sbr"},
		CleanupPackage: func(_ context.Context, _, _, pkg string) (string, error) {
			attempted = append(attempted, pkg)

			return "failure evidence", errors.New("partial install")
		}}

	err := run.Cleanup(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failure evidence") || len(attempted) != 1 {
		t.Fatalf("lost failures or skipped cleanup: %v %v", attempted, err)
	}

	if err := api.Get(context.Background(), client.ObjectKeyFromObject(namespace), namespace); !apierrors.IsNotFound(err) {
		t.Fatal("owned namespace not removed", err)
	}
}

func TestDeleteUsesUIDPrecondition(t *testing.T) {
	called := false
	api := interceptor.NewClient(testClient(), interceptor.Funcs{Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, opts ...client.DeleteOption) error {
		options := &client.DeleteOptions{}
		for _, option := range opts {
			option.ApplyToDelete(options)
		}

		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "owned-uid" {
			t.Fatal("missing UID precondition")
		}

		called = true

		return nil
	}})

	object := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "owned", UID: "owned-uid"}}
	if err := DeleteIdentity(context.Background(), api, object); err != nil || !called {
		t.Fatal(err)
	}

	object.UID = ""
	if err := DeleteIdentity(context.Background(), api, object); err == nil {
		t.Fatal("accepted missing UID")
	}
}

func TestSafeSpecNeverMatchesARealNode(t *testing.T) {
	spec := SafeSpec("run-token", "efs-sc")

	nodeSelector, isObject := spec["nodeSelector"].(map[string]interface{})
	if !isObject {
		t.Fatal("safe specification nodeSelector is not an object")
	}

	if len(nodeSelector) != 1 {
		t.Fatalf("expected exactly one nodeSelector entry, got %v", nodeSelector)
	}

	value, hasRunLabel := nodeSelector[RunLabel]
	if !hasRunLabel || value != "run-token" {
		t.Fatalf("expected nodeSelector[%q]=run-token, got %v", RunLabel, nodeSelector)
	}

	if spec["sharedStorageClass"] != "efs-sc" {
		t.Fatalf("expected sharedStorageClass=efs-sc, got %v", spec["sharedStorageClass"])
	}

	// A different run's token must produce a different, equally exclusive selector, so two
	// concurrent runs (or a run and a stray real node) can never collide.
	other := SafeSpec("other-token", "efs-sc")

	otherSelector, isObject := other["nodeSelector"].(map[string]interface{})
	if !isObject || otherSelector[RunLabel] == nodeSelector[RunLabel] {
		t.Fatal("SafeSpec did not scope the nodeSelector to the given token")
	}
}
