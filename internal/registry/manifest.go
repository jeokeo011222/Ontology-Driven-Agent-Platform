// Package registry 实现插件注册中心（第四篇）：
// 插件与版本生命周期、安装状态机、智能体/工具版本解析与清单校验。
package registry

import (
	"bytes"
	"encoding/json"

	"odap/internal/platform/errors"
)

// 插件类型白名单（registry.plugins.plugin_kind CHECK）。
const (
	KindAgent         = "AGENT"
	KindOntology      = "ONTOLOGY"
	KindTool          = "TOOL"
	KindPolicy        = "POLICY"
	KindModelProvider = "MODEL_PROVIDER"
)

// 运行时类型白名单（第四篇：process/container/wasm 三选一）。
const (
	RuntimeProcess   = "process"
	RuntimeContainer = "container"
	RuntimeWasm      = "wasm"
)

// pluginKinds / runtimeTypes 是白名单集合。
var (
	pluginKinds = map[string]bool{
		KindAgent: true, KindOntology: true, KindTool: true,
		KindPolicy: true, KindModelProvider: true,
	}
	runtimeTypes = map[string]bool{
		RuntimeProcess: true, RuntimeContainer: true, RuntimeWasm: true,
	}
)

// Manifest 是插件清单（V1 字段白名单，未知字段拒绝）。
type Manifest struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Runtime struct {
		Type string `json:"type"`
	} `json:"runtime"`
	Entrypoint       string   `json:"entrypoint"`
	Capabilities     []string `json:"capabilities,omitempty"`
	RequiredServices []string `json:"required_services,omitempty"`
}

// ValidateManifest 校验插件清单 JSON。必填缺失或取值越界返回
// CodePluginManifestInvalid；未知字段一律拒绝（防夹带任意配置）。
func ValidateManifest(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return errors.Ef(errors.CodePluginManifestInvalid, "清单 JSON 解析失败: %v", err)
	}
	if dec.More() {
		return errors.E(errors.CodePluginManifestInvalid, "清单 JSON 含有尾部多余内容")
	}
	if m.Key == "" {
		return errors.E(errors.CodePluginManifestInvalid, "清单缺少 key")
	}
	if m.Kind == "" {
		return errors.E(errors.CodePluginManifestInvalid, "清单缺少 kind")
	}
	if !pluginKinds[m.Kind] {
		return errors.Ef(errors.CodePluginManifestInvalid, "非法插件类型 %q", m.Kind)
	}
	if m.Name == "" {
		return errors.E(errors.CodePluginManifestInvalid, "清单缺少 name")
	}
	if m.Version == "" {
		return errors.E(errors.CodePluginManifestInvalid, "清单缺少 version")
	}
	if m.Runtime.Type == "" {
		return errors.E(errors.CodePluginManifestInvalid, "清单缺少 runtime.type")
	}
	if !runtimeTypes[m.Runtime.Type] {
		return errors.Ef(errors.CodePluginManifestInvalid, "非法运行时类型 %q（允许 process/container/wasm）", m.Runtime.Type)
	}
	if m.Entrypoint == "" {
		return errors.E(errors.CodePluginManifestInvalid, "清单缺少 entrypoint")
	}
	return nil
}

// ParseManifest 校验并解析清单。
func ParseManifest(raw []byte) (Manifest, error) {
	if err := ValidateManifest(raw); err != nil {
		return Manifest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, errors.Wrap(err, errors.CodePluginManifestInvalid, "清单 JSON 解析失败")
	}
	return m, nil
}
