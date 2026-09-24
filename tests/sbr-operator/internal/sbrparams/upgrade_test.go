package sbrparams

import (
	"strings"
	"testing"
)

func setRequiredCandidateInputs(t *testing.T) {
	t.Helper()

	t.Setenv("SBR_UPGRADE_CANDIDATE_SBR_BUNDLE", "registry.test/sbr-bundle:candidate")
	t.Setenv("SBR_UPGRADE_CANDIDATE_SBR_VERSION", "5.8.0")
	t.Setenv("SBR_UPGRADE_CANDIDATE_SBR_IMAGE", "registry.test/sbr:candidate")
	t.Setenv("SBR_UPGRADE_OPERATOR_SDK", "/test/operator-sdk")
	t.Setenv("SBR_UPGRADE_TEST_REVISION", strings.Repeat("a", 40))
}

func TestLoadUpgradeOperatorInputsUsesFixedIdentity(t *testing.T) {
	setRequiredCandidateInputs(t)
	t.Setenv("SBR_UPGRADE_PACKAGE", "ignored-sbr")
	t.Setenv("SBR_UPGRADE_NAMESPACE", "ignored-namespace")

	inputs, err := LoadUpgradeOperatorInputs()
	if err != nil {
		t.Fatal(err)
	}

	if inputs.Package != UpgradeSBRPackage || inputs.Namespace != UpgradeNamespace {
		t.Fatalf("upgrade identity is not fixed: %+v", inputs)
	}

	if inputs.BaselineSBR.Bundle != "" {
		t.Fatalf("optional baseline artifact unexpectedly defaulted before resolution: %+v", inputs)
	}

	if inputs.CandidateSBR.Bundle != "registry.test/sbr-bundle:candidate" ||
		inputs.CandidateSBR.Version != "5.8.0" || inputs.CandidateSBR.Image != "registry.test/sbr:candidate" {
		t.Fatalf("unexpected candidate inputs: %+v", inputs)
	}
}

func TestLoadUpgradeOperatorInputsRequiresCandidateSBRAndSDK(t *testing.T) {
	for _, variable := range []string{
		"SBR_UPGRADE_CANDIDATE_SBR_BUNDLE",
		"SBR_UPGRADE_CANDIDATE_SBR_VERSION",
		"SBR_UPGRADE_CANDIDATE_SBR_IMAGE",
		"SBR_UPGRADE_OPERATOR_SDK",
	} {
		t.Run(variable, func(t *testing.T) {
			setRequiredCandidateInputs(t)
			t.Setenv(variable, "")

			if _, err := LoadUpgradeOperatorInputs(); err == nil || !strings.Contains(err.Error(), variable) {
				t.Fatalf("missing %s was not reported: %v", variable, err)
			}
		})
	}
}

func TestLoadUpgradeOperatorInputsRequiresFullCommitHashRevision(t *testing.T) {
	setRequiredCandidateInputs(t)
	t.Setenv("SBR_UPGRADE_TEST_REVISION", "not-a-commit")

	if _, err := LoadUpgradeOperatorInputs(); err == nil || !strings.Contains(err.Error(), "SBR_UPGRADE_TEST_REVISION") {
		t.Fatalf("invalid test revision was not reported: %v", err)
	}
}

func TestLoadUpgradeOperatorInputsRejectsNonBooleanSkipCleanup(t *testing.T) {
	setRequiredCandidateInputs(t)
	t.Setenv("SBR_UPGRADE_SKIP_CLEANUP", "not-a-bool")

	if _, err := LoadUpgradeOperatorInputs(); err == nil || !strings.Contains(err.Error(), "SBR_UPGRADE_SKIP_CLEANUP") {
		t.Fatalf("invalid skip-cleanup was not reported: %v", err)
	}
}
