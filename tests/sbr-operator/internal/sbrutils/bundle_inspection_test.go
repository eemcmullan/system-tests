package sbrutils

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeScriptFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestBundleInspector(t *testing.T) {
	dir := t.TempDir()
	writeScriptFixture(t, filepath.Join(dir, "csv.yaml"), `metadata:
  name: storage-based-remediation.v5.8.0
  annotations:
    containerImage: candidate-manager
    olm.skipRange: '>=0.1.0 <5.8.0'
spec:
  version: 5.8.0
  install:
    spec:
      deployments:
      - spec:
          template:
            spec:
              containers:
              - name: manager
                image: candidate-manager
`, 0600)
	writeScriptFixture(t, filepath.Join(dir, "annotations.yaml"),
		"annotations:\n  operators.operatorframework.io.bundle.package.v1: storage-based-remediation\n", 0600)
	writeScriptFixture(t, filepath.Join(dir, "oc"), `#!/usr/bin/env bash
set -euo pipefail
if [[ $2 == extract ]]; then
  shift 3
  while [[ $# -gt 0 ]]; do
    if [[ $1 == --path ]]; then
      case $2 in
        /manifests/:*) cp "$FIXTURE_DIR/csv.yaml" "${2#*:}/sbr.clusterserviceversion.yaml" ;;
        /metadata/:*) cp "$FIXTURE_DIR/annotations.yaml" "${2#*:}/annotations.yaml" ;;
      esac
      shift 2
    else shift; fi
  done
elif [[ $2 == info ]]; then
	[[ $3 == --filter-by-os=linux/amd64 ]]
	if [[ $4 == wrong-operator ]]; then
    printf '{"digest":"sha256:%064d"}\n' 2
  else
    printf '{"digest":"sha256:%064d"}\n' 1
  fi
else exit 99; fi
`, 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FIXTURE_DIR", dir)

	bundle, err := inspectBundle(context.Background(), "bundle")
	if err != nil {
		t.Fatal(err)
	}

	if err := verifyBundle(bundle, "storage-based-remediation", "5.8.0"); err != nil {
		t.Fatal(err)
	}

	if err := requireSameImage(context.Background(), bundle.ManagerImage, "built-operator"); err != nil {
		t.Fatal(err)
	}

	if err := requireSameImage(context.Background(), bundle.ManagerImage, "wrong-operator"); err == nil {
		t.Fatal("accepted unrelated operator image")
	}
}

func TestFindLatestGABundleUsesOnlyPlainSemverTags(t *testing.T) {
	dir := t.TempDir()
	writeScriptFixture(t, filepath.Join(dir, "skopeo"), `#!/usr/bin/env bash
set -euo pipefail
[[ $1 == list-tags && $2 == docker://registry.test/bundles ]]
printf '{"Tags":["latest","v0.2.0","v0.3.0-rc.1","v0.2.1-a9feb15","v0.2.1","v0.2.10","v1.0.0-beta.1"]}\n'
`, 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	pullspec, err := findLatestGABundle(context.Background(), "registry.test/bundles")
	if err != nil {
		t.Fatal(err)
	}

	if pullspec != "registry.test/bundles:v0.2.10" {
		t.Fatalf("unexpected latest GA bundle: %s", pullspec)
	}
}

// TestInspectImageIgnoresStderrWarnings guards against a real regression: "oc image info"
// writes a deprecation warning to stderr (e.g. "W0924 ... Defaulting of registry auth
// file..."), which corrupted JSON parsing when stdout and stderr were merged into one
// buffer. inspectImage must parse cleanly even when the CLI writes to both streams.
func TestInspectImageIgnoresStderrWarnings(t *testing.T) {
	dir := t.TempDir()
	writeScriptFixture(t, filepath.Join(dir, "oc"), `#!/usr/bin/env bash
set -euo pipefail
echo 'W0924 12:04:07.722005 helpers.go:151] Defaulting of registry auth file is deprecated.' >&2
printf '{"digest":"sha256:%064d"}\n' 3
`, 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	info, err := inspectImage(context.Background(), "some-image")
	if err != nil {
		t.Fatal(err)
	}

	expected := "sha256:" + strings.Repeat("0", 63) + "3"
	if info.Digest != expected {
		t.Fatalf("digest = %q, expected %q", info.Digest, expected)
	}
}

func TestDownstreamGAVersionRejectsSuffixedTags(t *testing.T) {
	for tag, accepted := range map[string]bool{
		"v0.3.1":         true,
		"v0.3.1-a9feb15": false,
		"v0.3.1-rc.1":    false,
		"v0.3":           false,
		"latest":         false,
	} {
		_, ok := downstreamGAVersion(tag)
		if ok != accepted {
			t.Fatalf("tag %q acceptance = %t, expected %t", tag, ok, accepted)
		}
	}
}

func TestBundleCommandsSetTimeout(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "operator-sdk")
	writeScriptFixture(t, binary, "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$COMMAND_LOG\"\n", 0700)
	t.Setenv("COMMAND_LOG", filepath.Join(dir, "commands"))

	if _, err := InstallBundle(context.Background(), binary, "test-namespace", "baseline-bundle"); err != nil {
		t.Fatal(err)
	}

	if _, err := UpgradeBundle(context.Background(), binary, "test-namespace", "candidate-bundle"); err != nil {
		t.Fatal(err)
	}

	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}

	for _, expected := range []string{
		"run bundle -n test-namespace --timeout=10m baseline-bundle",
		"run bundle-upgrade -n test-namespace --timeout=10m candidate-bundle",
	} {
		if !strings.Contains(string(commands), expected) {
			t.Fatalf("missing %q in %s", expected, commands)
		}
	}
}
