package sbrparams

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

const (
	// UpgradeSBRPackage is the fixed OLM package name for the SBR operator.
	UpgradeSBRPackage = "storage-based-remediation"

	// UpgradeNamespace is the established namespace shared by medik8s operators.
	UpgradeNamespace = medik8sparams.OperatorNs

	// BaselineSBRBundleRepository contains released downstream SBR bundles.
	BaselineSBRBundleRepository = "registry.redhat.io/workload-availability/storage-based-remediation-operator-bundle"

	// SBRUpgradeConfigTestName is the fixed name of the test-owned StorageBasedRemediationConfig
	// used to prove that configuration and reconciliation survive the standalone upgrade.
	SBRUpgradeConfigTestName = "sbr-operator-upgrade"
)

// OperatorArtifact identifies one bundle and the operator version and image it contains.
type OperatorArtifact struct {
	Bundle  string `json:"bundle"`
	Version string `json:"version"`
	Image   string `json:"image"`
}

// UpgradeOperatorInputs are the artifacts used by tier:upgrade-operator.
type UpgradeOperatorInputs struct {
	BaselineSBR  OperatorArtifact `json:"baselineSBR"`
	CandidateSBR OperatorArtifact `json:"candidateSBR"`
	TestRevision string           `json:"testRevision"`
	Package      string           `json:"package"`
	Namespace    string           `json:"namespace"`
	OperatorSDK  string           `json:"operatorSDK"`
	SkipCleanup  bool             `json:"skipCleanup"`
}

// LoadUpgradeOperatorInputs reads the candidate inputs and optional baseline override
// from the environment.
func LoadUpgradeOperatorInputs() (UpgradeOperatorInputs, error) {
	skipCleanup, err := loadUpgradeSkipCleanup()
	if err != nil {
		return UpgradeOperatorInputs{}, err
	}

	testRevision, err := loadUpgradeTestRevision()
	if err != nil {
		return UpgradeOperatorInputs{}, err
	}

	candidateSBR, operatorSDK, err := loadCandidateSBR()
	if err != nil {
		return UpgradeOperatorInputs{}, err
	}

	return UpgradeOperatorInputs{
		BaselineSBR: OperatorArtifact{
			Bundle:  os.Getenv("SBR_UPGRADE_BASELINE_SBR_BUNDLE"),
			Version: os.Getenv("SBR_UPGRADE_BASELINE_SBR_VERSION"),
			Image:   os.Getenv("SBR_UPGRADE_BASELINE_SBR_IMAGE"),
		},
		CandidateSBR: candidateSBR,
		TestRevision: testRevision,
		Package:      UpgradeSBRPackage,
		Namespace:    UpgradeNamespace,
		OperatorSDK:  operatorSDK,
		SkipCleanup:  skipCleanup,
	}, nil
}

func loadCandidateSBR() (OperatorArtifact, string, error) {
	// These four caller-supplied values may be populated by Makefile automation in the future.
	candidate := candidateSBRFromEnvironment()
	operatorSDK := os.Getenv("SBR_UPGRADE_OPERATOR_SDK")

	for key, value := range map[string]string{
		"SBR_UPGRADE_CANDIDATE_SBR_BUNDLE":  candidate.Bundle,
		"SBR_UPGRADE_CANDIDATE_SBR_VERSION": candidate.Version,
		"SBR_UPGRADE_CANDIDATE_SBR_IMAGE":   candidate.Image,
		"SBR_UPGRADE_OPERATOR_SDK":          operatorSDK,
	} {
		if value == "" {
			return OperatorArtifact{}, "", fmt.Errorf("%s must be set for the SBR operator upgrade scenario", key)
		}
	}

	return candidate, operatorSDK, nil
}

func candidateSBRFromEnvironment() OperatorArtifact {
	return OperatorArtifact{
		Bundle:  os.Getenv("SBR_UPGRADE_CANDIDATE_SBR_BUNDLE"),
		Version: os.Getenv("SBR_UPGRADE_CANDIDATE_SBR_VERSION"),
		Image:   os.Getenv("SBR_UPGRADE_CANDIDATE_SBR_IMAGE"),
	}
}

func loadUpgradeSkipCleanup() (bool, error) {
	skipCleanup, err := strconv.ParseBool(upgradeEnvOrDefault("SBR_UPGRADE_SKIP_CLEANUP", "false"))
	if err != nil {
		return false, fmt.Errorf("SBR_UPGRADE_SKIP_CLEANUP must be a boolean: %w", err)
	}

	return skipCleanup, nil
}

func loadUpgradeTestRevision() (string, error) {
	revision := os.Getenv("SBR_UPGRADE_TEST_REVISION")
	if revision == "" {
		output, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("derive SBR_UPGRADE_TEST_REVISION: %w", err)
		}

		revision = strings.TrimSpace(string(output))
	}

	if !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(revision) {
		return "", fmt.Errorf("SBR_UPGRADE_TEST_REVISION must be a full Git commit hash")
	}

	return revision, nil
}

func upgradeEnvOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}

	return fallback
}
