package nhcutils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
	"k8s.io/apimachinery/pkg/util/yaml"
)

type bundleCSV struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Version string `json:"version"`
		Install struct {
			Spec struct {
				Deployments []struct {
					Spec struct {
						Template struct {
							Spec struct {
								Containers []struct {
									Name  string `json:"name"`
									Image string `json:"image"`
								} `json:"containers"`
							} `json:"spec"`
						} `json:"template"`
					} `json:"spec"`
				} `json:"deployments"`
			} `json:"spec"`
		} `json:"install"`
	} `json:"spec"`
}

type bundleAnnotations struct {
	Annotations map[string]string `json:"annotations"`
}

type imageInfo struct {
	Digest string `json:"digest"`
}

type imageTags struct {
	Tags []string `json:"Tags"`
}

type inspectedBundle struct {
	Pullspec, Package, Version, ManagerImage string
}

// ResolveAndVerifyUpgradeClusterInputs pins and validates the PR bundle used after a cluster upgrade.
func ResolveAndVerifyUpgradeClusterInputs(
	ctx context.Context, inputs nhcparams.UpgradeClusterInputs,
) (nhcparams.UpgradeClusterInputs, error) {
	candidate, err := inspectBundle(ctx, inputs.CandidateNHC.Bundle)
	if err != nil {
		return inputs, fmt.Errorf("inspect candidate bundle: %w", err)
	}

	if err := verifyBundle(candidate, inputs.Package, inputs.CandidateNHC.Version); err != nil {
		return inputs, fmt.Errorf("candidate bundle: %w", err)
	}

	if err := requireSameImage(ctx, candidate.ManagerImage, inputs.CandidateNHC.Image); err != nil {
		return inputs, fmt.Errorf("candidate bundle manager: %w", err)
	}

	inputs.CandidateNHC.Bundle, inputs.CandidateNHC.Image = candidate.Pullspec, candidate.ManagerImage

	return inputs, nil
}

// ResolveAndVerifyFreshInstallInputs resolves and verifies candidate NHC and SNR artifacts.
func ResolveAndVerifyFreshInstallInputs(
	ctx context.Context, inputs nhcparams.FreshInstallInputs,
) (nhcparams.FreshInstallInputs, error) {
	candidateNHC, err := inspectAndVerifyBundle(ctx, "candidate NHC", inputs.CandidateNHC.Bundle,
		inputs.Package, inputs.CandidateNHC.Version, inputs.CandidateNHC.Image)
	if err != nil {
		return inputs, err
	}

	candidateSNRBundle := inputs.CandidateSNR.Bundle
	if candidateSNRBundle == "" {
		candidateSNRBundle, err = findLatestGABundle(ctx, nhcparams.BaselineSNRBundleRepository)
		if err != nil {
			return inputs, fmt.Errorf("discover candidate SNR bundle: %w", err)
		}
	}

	candidateSNR, err := inspectAndVerifyBundle(ctx, "candidate SNR", candidateSNRBundle,
		inputs.SNRPackage, inputs.CandidateSNR.Version, inputs.CandidateSNR.Image)
	if err != nil {
		return inputs, err
	}

	inputs.CandidateNHC = resolvedArtifact(candidateNHC)
	inputs.CandidateSNR = resolvedArtifact(candidateSNR)

	return inputs, nil
}

// ResolveAndVerifyUpgradeOperatorInputs performs standalone-upgrade bundle identity checks.
func ResolveAndVerifyUpgradeOperatorInputs(
	ctx context.Context, inputs nhcparams.UpgradeOperatorInputs,
) (nhcparams.UpgradeOperatorInputs, error) {
	baselineNHCBundle := inputs.BaselineNHC.Bundle
	if baselineNHCBundle == "" {
		var err error

		baselineNHCBundle, err = findLatestGABundle(ctx, nhcparams.BaselineNHCBundleRepository)
		if err != nil {
			return inputs, fmt.Errorf("discover baseline NHC bundle: %w", err)
		}
	}

	baselineNHC, err := inspectAndVerifyBundle(ctx, "baseline NHC", baselineNHCBundle,
		inputs.Package, inputs.BaselineNHC.Version, inputs.BaselineNHC.Image)
	if err != nil {
		return inputs, err
	}

	candidateNHC, err := inspectAndVerifyBundle(ctx, "candidate NHC", inputs.CandidateNHC.Bundle,
		inputs.Package, inputs.CandidateNHC.Version, inputs.CandidateNHC.Image)
	if err != nil {
		return inputs, err
	}

	if baselineNHC.Version == candidateNHC.Version {
		return inputs, fmt.Errorf("candidate NHC version must differ from the baseline")
	}

	if err := requireDifferentImage(ctx, baselineNHC.ManagerImage, candidateNHC.ManagerImage); err != nil {
		return inputs, fmt.Errorf("candidate NHC manager: %w", err)
	}

	baselineSNRBundle := inputs.BaselineSNR.Bundle
	if baselineSNRBundle == "" {
		baselineSNRBundle, err = findLatestGABundle(ctx, nhcparams.BaselineSNRBundleRepository)
		if err != nil {
			return inputs, fmt.Errorf("discover baseline SNR bundle: %w", err)
		}
	}

	baselineSNR, err := inspectAndVerifyBundle(ctx, "baseline SNR", baselineSNRBundle,
		inputs.SNRPackage, inputs.BaselineSNR.Version, inputs.BaselineSNR.Image)
	if err != nil {
		return inputs, err
	}

	candidateSNR := baselineSNR
	if inputs.CandidateSNR.Bundle != "" {
		candidateSNR, err = inspectAndVerifyBundle(ctx, "candidate SNR", inputs.CandidateSNR.Bundle,
			inputs.SNRPackage, inputs.CandidateSNR.Version, inputs.CandidateSNR.Image)
		if err != nil {
			return inputs, err
		}
	} else {
		if inputs.CandidateSNR.Version != "" && inputs.CandidateSNR.Version != baselineSNR.Version {
			return inputs, fmt.Errorf("candidate SNR version %q differs from baseline %q; SNR upgrades are not implemented",
				inputs.CandidateSNR.Version, baselineSNR.Version)
		}

		if inputs.CandidateSNR.Image != "" {
			if err := requireSameImage(ctx, baselineSNR.ManagerImage, inputs.CandidateSNR.Image); err != nil {
				return inputs, fmt.Errorf("candidate SNR differs from baseline; SNR upgrades are not implemented: %w", err)
			}
		}
	}

	if candidateSNR.Version != baselineSNR.Version {
		return inputs, fmt.Errorf("candidate SNR version %q differs from baseline %q; SNR upgrades are not implemented",
			candidateSNR.Version, baselineSNR.Version)
	}

	if err := requireSameImage(ctx, baselineSNR.Pullspec, candidateSNR.Pullspec); err != nil {
		return inputs, fmt.Errorf("candidate SNR bundle differs from baseline; SNR upgrades are not implemented: %w", err)
	}

	if err := requireSameImage(ctx, baselineSNR.ManagerImage, candidateSNR.ManagerImage); err != nil {
		return inputs, fmt.Errorf("candidate SNR manager differs from baseline; SNR upgrades are not implemented: %w", err)
	}

	inputs.BaselineNHC = resolvedArtifact(baselineNHC)
	inputs.CandidateNHC = resolvedArtifact(candidateNHC)
	inputs.BaselineSNR = resolvedArtifact(baselineSNR)
	inputs.CandidateSNR = resolvedArtifact(candidateSNR)

	return inputs, nil
}

func resolvedArtifact(bundle inspectedBundle) nhcparams.OperatorArtifact {
	return nhcparams.OperatorArtifact{
		Bundle:  bundle.Pullspec,
		Version: bundle.Version,
		Image:   bundle.ManagerImage,
	}
}

func inspectAndVerifyBundle(
	ctx context.Context, name, pullspec, expectedPackage, expectedVersion, expectedImage string,
) (inspectedBundle, error) {
	bundle, err := inspectBundle(ctx, pullspec)
	if err != nil {
		return bundle, fmt.Errorf("inspect %s bundle: %w", name, err)
	}

	if err := verifyBundle(bundle, expectedPackage, expectedVersion); err != nil {
		return bundle, fmt.Errorf("%s bundle: %w", name, err)
	}

	if expectedImage != "" {
		if err := requireSameImage(ctx, bundle.ManagerImage, expectedImage); err != nil {
			return bundle, fmt.Errorf("%s bundle manager: %w", name, err)
		}
	}

	return bundle, nil
}

func findLatestGABundle(ctx context.Context, repository string) (string, error) {
	output, err := RunCommand(ctx, "skopeo", "list-tags", "docker://"+repository)
	if err != nil {
		return "", fmt.Errorf("skopeo list-tags %s: %w\n%s", repository, err, output)
	}

	var listed imageTags
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		return "", fmt.Errorf("parse tags for %s: %w", repository, err)
	}

	latestTag := ""
	latestVersion := [3]int{-1, -1, -1}

	for _, tag := range listed.Tags {
		version, ok := downstreamGAVersion(tag)
		if !ok {
			continue
		}

		if compareVersion(version, latestVersion) > 0 ||
			(compareVersion(version, latestVersion) == 0 && tag > latestTag) {
			latestTag, latestVersion = tag, version
		}
	}

	if latestTag == "" {
		return "", fmt.Errorf("no downstream GA tags found in %s", repository)
	}

	return repository + ":" + latestTag, nil
}

var downstreamGATag = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([a-f0-9]{7,}))?$`)

func downstreamGAVersion(tag string) ([3]int, bool) {
	match := downstreamGATag.FindStringSubmatch(tag)
	if match == nil {
		return [3]int{}, false
	}

	var version [3]int
	for index := range version {
		value, err := strconv.Atoi(match[index+1])
		if err != nil {
			return [3]int{}, false
		}

		version[index] = value
	}

	return version, true
}

func compareVersion(left, right [3]int) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}

		if left[index] > right[index] {
			return 1
		}
	}

	return 0
}

//nolint:funlen,wsl_v5 // Extraction, parsing, and identity validation form one bounded operation.
func inspectBundle(ctx context.Context, pullspec string) (inspectedBundle, error) {
	info, err := inspectImage(ctx, pullspec)
	if err != nil {
		return inspectedBundle{}, err
	}

	repository := strings.Split(pullspec, "@")[0]
	lastSlash := strings.LastIndex(repository, "/")
	if colon := strings.LastIndex(repository, ":"); colon > lastSlash {
		repository = repository[:colon]
	}
	resolved := repository + "@" + info.Digest

	dir, err := os.MkdirTemp("", "nhc-bundle-inspection-")
	if err != nil {
		return inspectedBundle{}, err
	}
	defer os.RemoveAll(dir)

	manifests, metadata := filepath.Join(dir, "manifests"), filepath.Join(dir, "metadata")
	if err := os.MkdirAll(manifests, 0o700); err != nil {
		return inspectedBundle{}, err
	}
	if err := os.MkdirAll(metadata, 0o700); err != nil {
		return inspectedBundle{}, err
	}

	if output, err := RunCommand(ctx, "oc", "image", "extract", resolved,
		"--path", "/manifests/:"+manifests, "--path", "/metadata/:"+metadata, "--confirm"); err != nil {
		return inspectedBundle{}, fmt.Errorf("extract %s: %w\n%s", resolved, err, output)
	}

	csvPaths, err := filepath.Glob(filepath.Join(manifests, "*clusterserviceversion.yaml"))
	if err != nil || len(csvPaths) != 1 {
		return inspectedBundle{}, fmt.Errorf("expected exactly one CSV, found %d", len(csvPaths))
	}

	csvBytes, err := os.ReadFile(csvPaths[0])
	if err != nil {
		return inspectedBundle{}, err
	}
	var csv bundleCSV
	if err := unmarshalYAML(csvBytes, &csv); err != nil {
		return inspectedBundle{}, fmt.Errorf("parse CSV: %w", err)
	}

	annotationBytes, err := os.ReadFile(filepath.Join(metadata, "annotations.yaml"))
	if err != nil {
		return inspectedBundle{}, err
	}
	var annotations bundleAnnotations
	if err := unmarshalYAML(annotationBytes, &annotations); err != nil {
		return inspectedBundle{}, fmt.Errorf("parse bundle annotations: %w", err)
	}

	manager := ""
	for _, deployment := range csv.Spec.Install.Spec.Deployments {
		for _, container := range deployment.Spec.Template.Spec.Containers {
			if container.Name == "manager" {
				if manager != "" {
					return inspectedBundle{}, fmt.Errorf("more than one manager container")
				}
				manager = container.Image
			}
		}
	}
	if manager == "" || csv.Metadata.Annotations["containerImage"] != manager {
		return inspectedBundle{}, fmt.Errorf("CSV manager image and containerImage annotation do not match")
	}

	return inspectedBundle{
		Pullspec:     resolved,
		Package:      annotations.Annotations["operators.operatorframework.io.bundle.package.v1"],
		Version:      csv.Spec.Version,
		ManagerImage: manager,
	}, nil
}

//nolint:wsl_v5 // Parsing checks intentionally follow their inputs.
func inspectImage(ctx context.Context, pullspec string) (imageInfo, error) {
	output, err := RunCommandStdout(ctx, "oc", "image", "info", pullspec, "-o", "json")
	if err != nil {
		return imageInfo{}, fmt.Errorf("oc image info %s: %w\n%s", pullspec, err, output)
	}
	var info imageInfo
	if err := json.Unmarshal([]byte(output), &info); err != nil {
		return imageInfo{}, fmt.Errorf("parse image info: %w", err)
	}
	if !strings.HasPrefix(info.Digest, "sha256:") {
		return imageInfo{}, fmt.Errorf("image has invalid digest %q", info.Digest)
	}

	return info, nil
}

//nolint:wsl_v5 // Package and version checks intentionally remain adjacent.
func verifyBundle(bundle inspectedBundle, expectedPackage, expectedVersion string) error {
	if bundle.Package != expectedPackage {
		return fmt.Errorf("package %q, expected %q", bundle.Package, expectedPackage)
	}
	if expectedVersion != "" && bundle.Version != expectedVersion {
		return fmt.Errorf("version %q, expected %q", bundle.Version, expectedVersion)
	}

	return nil
}

//nolint:wsl_v5 // Digest reads and comparison form one linear check.
func requireSameImage(ctx context.Context, actual, expected string) error {
	actualInfo, err := inspectImage(ctx, actual)
	if err != nil {
		return err
	}
	expectedInfo, err := inspectImage(ctx, expected)
	if err != nil {
		return err
	}
	if actualInfo.Digest != expectedInfo.Digest {
		return fmt.Errorf("digest %s does not match expected digest %s", actualInfo.Digest, expectedInfo.Digest)
	}

	return nil
}

func requireDifferentImage(ctx context.Context, baseline, candidate string) error {
	baselineInfo, err := inspectImage(ctx, baseline)
	if err != nil {
		return err
	}

	candidateInfo, err := inspectImage(ctx, candidate)
	if err != nil {
		return err
	}

	if baselineInfo.Digest == candidateInfo.Digest {
		return fmt.Errorf("candidate digest %s matches the baseline", candidateInfo.Digest)
	}

	return nil
}

func unmarshalYAML(data []byte, value any) error {
	jsonData, err := yaml.ToJSON(data)
	if err != nil {
		return err
	}

	return json.Unmarshal(jsonData, value)
}
