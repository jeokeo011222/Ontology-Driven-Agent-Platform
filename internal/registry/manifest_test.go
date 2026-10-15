package registry

import (
	"testing"

	"odap/internal/platform/errors"
)

const validManifest = `{
  "key": "expense-agent",
  "kind": "AGENT",
  "name": "Expense Agent",
  "version": "1.0.0",
  "runtime": {"type": "process"},
  "entrypoint": "bin/agent",
  "capabilities": ["read:expense"],
  "required_services": ["ontology"]
}`

func TestValidateManifestAcceptsValid(t *testing.T) {
	if err := ValidateManifest([]byte(validManifest)); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Key != "expense-agent" || m.Kind != KindAgent || m.Runtime.Type != RuntimeProcess {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if len(m.Capabilities) != 1 || len(m.RequiredServices) != 1 {
		t.Fatalf("capabilities/services 解析失败: %+v", m)
	}
}

func TestValidateManifestRuntimeWhitelist(t *testing.T) {
	for _, runtime := range []string{RuntimeProcess, RuntimeContainer, RuntimeWasm} {
		doc := `{"key":"k","kind":"TOOL","name":"n","version":"1","runtime":{"type":"` + runtime + `"},"entrypoint":"e"}`
		if err := ValidateManifest([]byte(doc)); err != nil {
			t.Fatalf("runtime %s rejected: %v", runtime, err)
		}
	}
	for _, runtime := range []string{"lambda", "docker", "", "PROCESS"} {
		doc := `{"key":"k","kind":"TOOL","name":"n","version":"1","runtime":{"type":"` + runtime + `"},"entrypoint":"e"}`
		err := ValidateManifest([]byte(doc))
		if err == nil {
			t.Fatalf("runtime %q must be rejected", runtime)
		}
		if code := errors.As(err).Code; code != errors.CodePluginManifestInvalid {
			t.Fatalf("runtime %q err code = %s, want PLUGIN_MANIFEST_INVALID", runtime, code)
		}
	}
}

func TestValidateManifestRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"invalid json":          `{"key":`,
		"trailing content":      validManifest + validManifest,
		"missing key":           `{"kind":"AGENT","name":"n","version":"1","runtime":{"type":"process"},"entrypoint":"e"}`,
		"missing kind":          `{"key":"k","name":"n","version":"1","runtime":{"type":"process"},"entrypoint":"e"}`,
		"bad kind":              `{"key":"k","kind":"ALIEN","name":"n","version":"1","runtime":{"type":"process"},"entrypoint":"e"}`,
		"missing name":          `{"key":"k","kind":"AGENT","version":"1","runtime":{"type":"process"},"entrypoint":"e"}`,
		"missing version":       `{"key":"k","kind":"AGENT","name":"n","runtime":{"type":"process"},"entrypoint":"e"}`,
		"missing runtime":       `{"key":"k","kind":"AGENT","name":"n","version":"1","entrypoint":"e"}`,
		"missing entrypoint":    `{"key":"k","kind":"AGENT","name":"n","version":"1","runtime":{"type":"process"}}`,
		"unknown field":         `{"key":"k","kind":"AGENT","name":"n","version":"1","runtime":{"type":"process"},"entrypoint":"e","surprise":true}`,
		"unknown runtime field": `{"key":"k","kind":"AGENT","name":"n","version":"1","runtime":{"type":"process","image":"x"},"entrypoint":"e"}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateManifest([]byte(doc))
			if err == nil {
				t.Fatalf("%s: expected rejection", name)
			}
			if code := errors.As(err).Code; code != errors.CodePluginManifestInvalid {
				t.Fatalf("%s: err code = %s, want PLUGIN_MANIFEST_INVALID", name, code)
			}
		})
	}
}

func TestValidateManifestOptionalFieldsOmitted(t *testing.T) {
	doc := `{"key":"k","kind":"POLICY","name":"n","version":"1","runtime":{"type":"wasm"},"entrypoint":"e"}`
	if err := ValidateManifest([]byte(doc)); err != nil {
		t.Fatalf("omitted optional fields should be valid: %v", err)
	}
}
