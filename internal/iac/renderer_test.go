package iac

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRendererIsDeterministicAndSelfContained(t *testing.T) {
	t.Parallel()
	firstDirectory := t.TempDir()
	secondDirectory := t.TempDir()
	first := validDeploymentSpec()
	first.NonSensitiveEnvironment["LOG_LEVEL"] = "info"
	second := validDeploymentSpec()
	second.NonSensitiveEnvironment = map[string]string{
		"LOG_LEVEL":              "info",
		"SPRING_PROFILES_ACTIVE": "production",
	}

	renderer := Renderer{}
	if err := renderer.Render(firstDirectory, first); err != nil {
		t.Fatalf("render first configuration: %v", err)
	}
	if err := renderer.Render(secondDirectory, second); err != nil {
		t.Fatalf("render second configuration: %v", err)
	}
	firstRoot, err := os.ReadFile(filepath.Join(firstDirectory, "main.tf.json"))
	if err != nil {
		t.Fatalf("read first root: %v", err)
	}
	secondRoot, err := os.ReadFile(filepath.Join(secondDirectory, "main.tf.json"))
	if err != nil {
		t.Fatalf("read second root: %v", err)
	}
	if !bytes.Equal(firstRoot, secondRoot) {
		t.Fatalf("configuration is not deterministic:\n%s\n---\n%s", firstRoot, secondRoot)
	}
	for _, name := range []string{"main.tf", "variables.tf", "versions.tf", "outputs.tf"} {
		if _, err := os.Stat(filepath.Join(firstDirectory, "modules", "cloud-run", name)); err != nil {
			t.Fatalf("embedded module %s was not rendered: %v", name, err)
		}
	}
}
