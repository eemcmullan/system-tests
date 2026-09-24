package sbrutils

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"
)

// RunOperatorSDK runs one bounded operator-sdk command and preserves its combined output.
func RunOperatorSDK(ctx context.Context, binary string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	command := exec.CommandContext(commandCtx, binary, args...)

	var output bytes.Buffer

	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return output.String(), fmt.Errorf("%s %v: %w", binary, args, err)
	}

	return output.String(), nil
}

// InstallBundle installs a bundle into the scenario's namespace.
func InstallBundle(ctx context.Context, binary, namespace, bundle string) (string, error) {
	return RunOperatorSDK(ctx, binary, "run", "bundle", "-n", namespace, "--timeout=10m", bundle)
}

// UpgradeBundle upgrades the existing bundle installation in place.
func UpgradeBundle(ctx context.Context, binary, namespace, bundle string) (string, error) {
	return RunOperatorSDK(ctx, binary, "run", "bundle-upgrade", "-n", namespace, "--timeout=10m", bundle)
}

// CleanupBundle intentionally leaves CRDs and the namespace OperatorGroup
// alone, since both can be shared by other operators.
func CleanupBundle(ctx context.Context, binary, namespace, packageName string) (string, error) {
	return RunOperatorSDK(ctx, binary, "cleanup", packageName, "-n", namespace,
		"--delete-all=false", "--delete-crds=false", "--delete-operator-groups=false")
}

// GetSBRControllerImage returns the manager image from a running controller.
func GetSBRControllerImage(apiClient *clients.Settings) (string, error) {
	return helpers.GetControllerImage(apiClient, sbrparams.UpgradeNamespace,
		sbrparams.OperatorControllerPodLabelSelector, sbrparams.ManagerContainerName)
}

// CollectFailureEvidence is best-effort so the original assertion remains the
// reported failure when the local environment has no oc binary.
func CollectFailureEvidence(ctx context.Context, namespace string) string {
	var evidence bytes.Buffer

	for _, args := range [][]string{
		{"get", "subscriptions,clusterserviceversions,installplans,catalogsources,pods", "-n", namespace, "-o", "yaml"},
		{"get", "events", "-n", namespace, "--sort-by=.lastTimestamp"},
		{"get", "storagebasedremediationconfigs,storagebasedremediations,storagebasedremediationtemplates",
			"-n", namespace, "-o", "yaml"},
		{"logs", "deployment/" + sbrparams.OperatorDeploymentName, "-n", namespace, "--all-containers=true", "--tail=500"},
	} {
		output, err := RunCommand(ctx, "oc", args...)
		fmt.Fprintf(&evidence, "oc %v (error=%v):\n%s\n", args, err, output)
	}

	return evidence.String()
}

// RunCommand runs a bounded diagnostic command and returns its combined output.
func RunCommand(ctx context.Context, binary string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	command := exec.CommandContext(commandCtx, binary, args...)

	var output bytes.Buffer

	command.Stdout, command.Stderr = &output, &output
	err := command.Run()

	return output.String(), err
}

// RunCommandStdout runs a bounded command and returns only its stdout, so that CLI
// deprecation/throttling warnings written to stderr (e.g. "oc image info"'s
// "W... Defaulting of registry auth file..." line) can never corrupt output the
// caller intends to parse as JSON/YAML. On failure, stderr is folded into the
// returned error for diagnostics.
func RunCommandStdout(ctx context.Context, binary string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	command := exec.CommandContext(commandCtx, binary, args...)

	var stdout, stderr bytes.Buffer

	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w\n%s", err, stderr.String())
	}

	return stdout.String(), nil
}
