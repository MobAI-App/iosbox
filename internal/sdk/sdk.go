package sdk

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func IosBoxHome() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".iosbox")
}

func SDKPath() string {
	return filepath.Join(IosBoxHome(), "sdk", "darwin.artifactbundle")
}

func ToolsetPath() string {
	return filepath.Join(SDKPath(), "toolset")
}

func IsInstalled() bool {
	_, err := os.Stat(filepath.Join(SDKPath(), "swift-sdk.json"))
	return err == nil
}

func IPhoneOSSDKRoot() (string, error) {
	jsonPath := filepath.Join(SDKPath(), "swift-sdk.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return "", fmt.Errorf("SDK not installed — run `iosbox setup`")
	}

	var raw struct {
		TargetTriples map[string]struct {
			SDKRootPath string `json:"sdkRootPath"`
		} `json:"targetTriples"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("corrupt swift-sdk.json: %w", err)
	}
	triple, ok := raw.TargetTriples["arm64-apple-ios"]
	if !ok || triple.SDKRootPath == "" {
		return "", fmt.Errorf("arm64-apple-ios triple not found in swift-sdk.json")
	}
	abs := filepath.Join(SDKPath(), triple.SDKRootPath)
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("SDK path from swift-sdk.json does not exist: %s", abs)
	}
	return abs, nil
}

func findSDKRoot(base, prefix string) (string, error) {
	var found string
	filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}
		name := info.Name()
		if len(name) > len(prefix) && name[:len(prefix)] == prefix && filepath.Ext(name) == ".sdk" {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", fmt.Errorf("iPhoneOS SDK not found in %s", base)
	}
	return found, nil
}

func IPhoneOSSDKVersion() string {
	sdkRoot, err := IPhoneOSSDKRoot()
	if err != nil {
		return ""
	}
	name := filepath.Base(sdkRoot) // e.g. "iPhoneOS26.2.sdk"
	name = strings.TrimPrefix(name, "iPhoneOS")
	name = strings.TrimSuffix(name, ".sdk")
	return name
}

// Passing this as -resource-dir to swiftc prevents the Linux Swift stdlib's
// module maps (e.g. Dispatch) from conflicting with the iOS SDK's modules.
func SwiftResourcesDir() string {
	return filepath.Join(SDKPath(), "Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/lib/swift")
}

// Provides compiler-rt builtins like ___isPlatformVersionAtLeast
// needed by ObjC code using @available() checks.
func ClangRTLibPath() string {
	pattern := filepath.Join(SDKPath(), "Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/lib/clang/*/lib/darwin/libclang_rt.ios.a")
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func HostTriple() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}
	return arch + "-unknown-linux-gnu"
}

type SwiftSDKJSON struct {
	SchemaVersion string                  `json:"schemaVersion"`
	TargetTriples map[string]TripleConfig `json:"targetTriples"`
}

type TripleConfig struct {
	SDKRootDir   string   `json:"sdkRootDir"`
	ToolsetPaths []string `json:"toolsetPaths"`
}

// ArtifactBundleInfo represents info.json in the artifact bundle.
type ArtifactBundleInfo struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Artifacts     map[string]ArtifactDef `json:"artifacts"`
}

type ArtifactDef struct {
	Type     string       `json:"type"`
	Version  string       `json:"version"`
	Variants []VariantDef `json:"variants"`
}

type VariantDef struct {
	Path             string   `json:"path"`
	SupportedTriples []string `json:"supportedTriples,omitempty"`
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
