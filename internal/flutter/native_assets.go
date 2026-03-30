package flutter

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type nativeAssetPackage struct {
	name    string // package name (e.g. "objective_c")
	srcDir  string // path to source directory
	assetID string // asset identifier (e.g. "package:objective_c/objective_c.dylib")
	dylibName string // output dylib name
}

// knownNativeAssets lists packages that use Dart native assets with ObjC code.
// These can't be built by flutter assemble on Linux, so we compile them ourselves.
var knownNativeAssets = []struct {
	packageName string
	assetID     string
	dylibName   string
}{
	{"objective_c", "package:objective_c/objective_c.dylib", "objective_c.dylib"},
}

func buildNativeAssets(ctx *buildContext) error {
	nativeAssetsDir := filepath.Join(ctx.projectPath, "build", "native_assets", "ios")
	frameworksDir := filepath.Join(ctx.appDir, "Frameworks")

	var bundledFromAssemble []nativeAssetPackage
	for _, known := range knownNativeAssets {
		fwName := strings.TrimSuffix(known.dylibName, ".dylib")
		fwDir := filepath.Join(nativeAssetsDir, fwName+".framework")
		fwBinary := filepath.Join(fwDir, fwName)
		if _, err := os.Stat(fwBinary); err == nil {
			destFw := filepath.Join(frameworksDir, fwName+".framework")
			if err := copyDir(fwDir, destFw); err != nil {
				return fmt.Errorf("copy %s.framework: %w", fwName, err)
			}
			if err := ensureCodeSignatureSpace(filepath.Join(destFw, fwName)); err != nil {
				fmt.Printf("  warning: %s code signature: %v\n", fwName, err)
			}
			bundledFromAssemble = append(bundledFromAssemble, nativeAssetPackage{
				name:      known.packageName,
				assetID:   known.assetID,
				dylibName: fwName + ".framework/" + fwName,
			})
			fmt.Printf("  bundled native asset: %s.framework (from flutter assemble)\n", fwName)
		}
	}

	if len(bundledFromAssemble) > 0 {
		manifestPath := findNativeAssetsManifest(ctx.appDir)
		return updateNativeAssetsManifest(manifestPath, bundledFromAssemble)
	}

	pubCache := findPubCache()
	if pubCache == "" {
		return nil
	}

	var assets []nativeAssetPackage
	for _, known := range knownNativeAssets {
		pkgDir := findPackageDir(pubCache, known.packageName)
		if pkgDir == "" {
			continue
		}
		srcDir := filepath.Join(pkgDir, "src")
		if _, err := os.Stat(srcDir); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(pkgDir, "hook", "build.dart")); err != nil {
			continue
		}
		assets = append(assets, nativeAssetPackage{
			name:      known.packageName,
			srcDir:    srcDir,
			assetID:   known.assetID,
			dylibName: known.dylibName,
		})
	}

	if len(assets) == 0 {
		return nil
	}

	sdkRoot := resolveIOSSDKRoot()
	for _, asset := range assets {
		fmt.Printf("  building native asset: %s\n", asset.name)
		dylibPath := filepath.Join(frameworksDir, asset.dylibName)
		if err := compileNativeAsset(asset, sdkRoot, dylibPath); err != nil {
			return fmt.Errorf("build %s: %w", asset.name, err)
		}
		if err := ensureCodeSignatureSpace(dylibPath); err != nil {
			fmt.Printf("  warning: %s code signature: %v\n", asset.dylibName, err)
		}
	}

	manifestPath := findNativeAssetsManifest(ctx.appDir)
	return updateNativeAssetsManifest(manifestPath, assets)
}

func compileNativeAsset(asset nativeAssetPackage, sdkRoot, outputPath string) error {
	var cFiles, mFiles []string
	entries, err := os.ReadDir(asset.srcDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(asset.srcDir, e.Name())
		if strings.HasSuffix(e.Name(), ".c") {
			cFiles = append(cFiles, path)
		} else if strings.HasSuffix(e.Name(), ".m") {
			mFiles = append(mFiles, path)
		}
	}

	if len(cFiles) == 0 && len(mFiles) == 0 {
		return fmt.Errorf("no source files found in %s", asset.srcDir)
	}

	objDir, err := os.MkdirTemp("", "native-asset-obj-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(objDir)

	commonFlags := []string{
		"--target=arm64-apple-ios13.0",
		"-isysroot", sdkRoot,
		"-fpic",
		"-I", asset.srcDir,
	}

	var objFiles []string
	for _, src := range cFiles {
		obj := filepath.Join(objDir, filepath.Base(src)+".o")
		args := append([]string{"-c", src, "-o", obj}, commonFlags...)
		cmd := exec.Command("clang", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("compile %s: %w", filepath.Base(src), err)
		}
		objFiles = append(objFiles, obj)
	}

	for _, src := range mFiles {
		obj := filepath.Join(objDir, filepath.Base(src)+".o")
		args := append([]string{"-c", src, "-o", obj, "-x", "objective-c", "-fobjc-arc"}, commonFlags...)
		cmd := exec.Command("clang", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("compile %s: %w", filepath.Base(src), err)
		}
		objFiles = append(objFiles, obj)
	}

	lld := findLD64()
	linkFlags := []string{
		"-shared",
		"--target=arm64-apple-ios13.0",
		"-isysroot", sdkRoot,
		"-undefined", "dynamic_lookup",
		"-Wl,-install_name,@rpath/" + asset.dylibName,
		"-o", outputPath,
	}
	if lld != "" {
		linkFlags = append(linkFlags, "-fuse-ld="+lld,
			"-Wl,-platform_version,ios,13.0.0,16.0.0",
			"-Wl,-adhoc_codesign",
			"-Wl,-headerpad_max_install_names",
		)
	}
	linkFlags = append(linkFlags, objFiles...)

	os.MkdirAll(filepath.Dir(outputPath), 0o755)
	cmd := exec.Command("clang", linkFlags...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("link %s: %w", asset.dylibName, err)
	}

	return nil
}

// findNativeAssetsManifest locates NativeAssetsManifest.json in the app bundle.
// It may be at flutter_assets/ (root) or Frameworks/App.framework/flutter_assets/.
func findNativeAssetsManifest(appDir string) string {
	candidates := []string{
		filepath.Join(appDir, "flutter_assets", "NativeAssetsManifest.json"),
		filepath.Join(appDir, "Frameworks", "App.framework", "flutter_assets", "NativeAssetsManifest.json"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	// Default to the App.framework location
	return candidates[1]
}

func updateNativeAssetsManifest(manifestPath string, assets []nativeAssetPackage) error {
	type manifest struct {
		FormatVersion []int                           `json:"format-version"`
		NativeAssets  map[string]map[string][]string  `json:"native-assets"`
	}

	m := manifest{
		FormatVersion: []int{1, 0, 0},
		NativeAssets:  make(map[string]map[string][]string),
	}

	if data, err := os.ReadFile(manifestPath); err == nil {
		json.Unmarshal(data, &m)
	}
	if m.NativeAssets == nil {
		m.NativeAssets = make(map[string]map[string][]string)
	}
	if m.NativeAssets["ios_arm64"] == nil {
		m.NativeAssets["ios_arm64"] = make(map[string][]string)
	}

	for _, asset := range assets {
		m.NativeAssets["ios_arm64"][asset.assetID] = []string{"absolute", asset.dylibName}
	}

	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(manifestPath, data, 0o644)
}

func findPubCache() string {
	if dir := os.Getenv("PUB_CACHE"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".pub-cache", "hosted", "pub.dev"),
		filepath.Join(home, ".pub-cache", "hosted", "pub.dartlang.org"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func findPackageDir(pubCache, packageName string) string {
	entries, err := os.ReadDir(pubCache)
	if err != nil {
		return ""
	}
	var best string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), packageName+"-") {
			best = filepath.Join(pubCache, e.Name())
		}
	}
	return best
}

