package sdk

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractableFiles defines which files we need from Xcode.app.
// This mirrors xTool's SDKEntry.wanted filter.
var ExtractableFiles = []string{
	// Swift runtime and stdlib
	"Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/lib/swift",
	"Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/lib/swift_static",
	"Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/lib/clang",

	// iOS SDK
	"Contents/Developer/Platforms/iPhoneOS.platform/Developer/SDKs",

	// Simulator SDK (optional, for future use)
	// "Contents/Developer/Platforms/iPhoneSimulator.platform/Developer/SDKs",

	// Platform libraries
	"Contents/Developer/Platforms/iPhoneOS.platform/Developer/usr/lib",

	// Testing frameworks
	"Contents/Developer/Platforms/iPhoneOS.platform/Developer/Library/Frameworks",
	"Contents/Developer/Platforms/iPhoneOS.platform/Developer/Library/PrivateFrameworks",
}

// slimSwiftPlatforms lists the platform subdirectories to keep in swift/swift_static
// when running in slim mode. Everything else (macosx, watchos, iphonesimulator, etc.) is skipped.
// allowedSwiftPlatforms lists the subdirectories to keep when extracting swift/swift_static.
// Only iphoneos and shims are needed for iOS cross-compilation.
// "clang" is explicitly excluded — Apple bundles its own clang headers which
// conflict with open-source Swift's clang builtins (e.g. arm_neon.h).
var allowedSwiftPlatforms = map[string]bool{
	"iphoneos": true,
	"shims":    true,
}

func ExtractFromXcodeApp(xcodeApp string) error {
	fmt.Printf("extracting SDK from %s\n", xcodeApp)

	if _, err := os.Stat(filepath.Join(xcodeApp, "Contents", "Developer")); err != nil {
		return fmt.Errorf("not a valid Xcode.app: %s", xcodeApp)
	}

	bundleDir := SDKPath()
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return err
	}

	for _, relPath := range ExtractableFiles {
		src := filepath.Join(xcodeApp, relPath)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			fmt.Printf("  skip (not found): %s\n", relPath)
			continue
		}

		dst := filepath.Join(bundleDir, relPath)

		// Filter swift/swift_static to only keep iphoneos platform and shims.
		// Skips macosx, watchos, iphonesimulator (~1.3GB) and Apple's bundled
		// clang headers which conflict with open-source Swift's builtins.
		isSwiftLib := strings.HasSuffix(relPath, "/swift") || strings.HasSuffix(relPath, "/swift_static")
		if isSwiftLib {
			fmt.Printf("  copying (filtered): %s\n", relPath)
			if err := copyTreeFiltered(src, dst, allowedSwiftPlatforms); err != nil {
				return fmt.Errorf("copy %s: %w", relPath, err)
			}
		} else {
			fmt.Printf("  copying: %s\n", relPath)
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("copy %s: %w", relPath, err)
			}
		}
	}

	// Find the actual SDK directory name (e.g., iPhoneOS18.0.sdk)
	sdksDir := filepath.Join(bundleDir, "Contents/Developer/Platforms/iPhoneOS.platform/Developer/SDKs")
	sdkName, err := findFirstSDK(sdksDir)
	if err != nil {
		return fmt.Errorf("find SDK: %w", err)
	}

	iphoneosSDKRoot := filepath.Join(
		"Contents/Developer/Platforms/iPhoneOS.platform/Developer/SDKs",
		sdkName,
	)

	swiftResourceDir := "Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/lib/swift"

	// Generate toolset.json
	toolsetDir := filepath.Join(bundleDir, "toolset")
	if err := os.MkdirAll(filepath.Join(toolsetDir, "bin"), 0o755); err != nil {
		return err
	}

	toolset := map[string]any{
		"schemaVersion": "1.0",
		"rootPath":      "toolset/bin",
		"linker": map[string]string{
			"path": "ld64.lld",
		},
		"swiftCompiler": map[string]any{
			"extraCLIOptions": []string{
				"-Xfrontend", "-enable-cross-import-overlays",
				"-use-ld=lld",
			},
		},
	}
	if err := writeJSON(filepath.Join(bundleDir, "toolset.json"), toolset); err != nil {
		return err
	}

	// Patch .swiftinterface files to remove Apple-compiler version gate.
	// iOS SDK interfaces are locked to a specific swiftlang build; open-source
	// Swift has a different version string and refuses to load them.
	// Stripping the version comment makes them compiler-agnostic.
	// Patch .swiftinterface files in both the Swift stdlib and the iOS SDK.
	// The iOS SDK contains its own .swiftinterface files (e.g., Foundation,
	// UIKit) that also have the Apple compiler version gate.
	for _, dir := range []string{
		filepath.Join(bundleDir, swiftResourceDir),
		filepath.Join(bundleDir, iphoneosSDKRoot),
	} {
		if _, err := os.Stat(dir); err == nil {
			if err := patchSwiftInterfaces(dir); err != nil {
				return fmt.Errorf("patch swiftinterfaces in %s: %w", dir, err)
			}
		}
	}

	// Generate swift-sdk.json (schema 4.0 field names)
	swiftSDK := map[string]any{
		"schemaVersion": "4.0",
		"targetTriples": map[string]any{
			"arm64-apple-ios": map[string]any{
				"sdkRootPath":        iphoneosSDKRoot,
				"swiftResourcesPath": swiftResourceDir,
				"includeSearchPaths": []string{
					filepath.Join(iphoneosSDKRoot, "usr", "include"),
				},
				"toolsetPaths": []string{"toolset.json"},
			},
		},
	}
	if err := writeJSON(filepath.Join(bundleDir, "swift-sdk.json"), swiftSDK); err != nil {
		return err
	}

	// Generate info.json for artifact bundle
	info := ArtifactBundleInfo{
		SchemaVersion: "1.0",
		Artifacts: map[string]ArtifactDef{
			"darwin-sdk": {
				Type:    "swiftSDK",
				Version: "0.1.0",
				Variants: []VariantDef{
					{
						Path: ".",
						SupportedTriples: []string{
							"aarch64-unknown-linux-gnu",
							"x86_64-unknown-linux-gnu",
						},
					},
				},
			},
		},
	}
	if err := writeJSON(filepath.Join(bundleDir, "info.json"), info); err != nil {
		return err
	}

	fmt.Printf("SDK extracted to %s\n", bundleDir)
	return nil
}

func findFirstSDK(sdksDir string) (string, error) {
	entries, err := os.ReadDir(sdksDir)
	if err != nil {
		return "", err
	}
	// First pass: find a real (non-symlink) iPhoneOS*.sdk directory.
	// The SDKs dir typically has a symlink "iPhoneOS.sdk" → "iPhoneOS26.2.sdk";
	// we want the versioned one.
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue // skip symlinks
		}
		if e.IsDir() && strings.HasSuffix(e.Name(), ".sdk") && strings.HasPrefix(e.Name(), "iPhoneOS") {
			return e.Name(), nil
		}
	}
	// Second pass: accept symlinks too (better than nothing)
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".sdk") && strings.HasPrefix(name, "iPhoneOS") {
			return name, nil
		}
	}
	// Third pass: any .sdk at all
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sdk") {
			return e.Name(), nil
		}
	}
	return "", fmt.Errorf("no .sdk found in %s", sdksDir)
}

// copyTreeFiltered copies a directory tree but only includes top-level subdirectories
// whose names are in the allowed map. Files at the root level are always copied.
func copyTreeFiltered(src, dst string, allowed map[string]bool) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		if rel != "." {
			topDir := strings.SplitN(rel, string(filepath.Separator), 2)[0]
			if !allowed[topDir] {
				topPath := filepath.Join(src, topDir)
				if ti, e := os.Lstat(topPath); e == nil && ti.IsDir() {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}

		target := filepath.Join(dst, rel)

		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		return copyOneFile(path, target, info.Mode())
	})
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		// Use Lstat so we see symlinks as symlinks (not their targets)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			os.Remove(target)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.Symlink(link, target)
		}

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		return copyOneFile(path, target, info.Mode())
	})
}

func patchSwiftInterfaces(dir string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".swiftinterface") {
			return err
		}
		return stripSwiftCompilerVersion(path)
	})
}

func stripSwiftCompilerVersion(path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	var lines []string
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "// swift-compiler-version:") {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	in.Close()

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func copyOneFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
