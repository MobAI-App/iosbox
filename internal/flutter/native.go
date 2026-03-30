package flutter

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MobAI-App/iosbox/internal/sdk"
)

type pluginInfo struct {
	Name       string // e.g. "path_provider_foundation"
	PkgDir     string // path to the SwiftPM package directory (contains Package.swift)
	ClassName  string // e.g. "PathProviderPlugin" (from GeneratedPluginRegistrant.m)
	ModuleName string // SwiftPM module name for import
}

func compileNative(ctx *buildContext) error {
	builderDir := filepath.Join(ctx.buildDir, "native-builder")
	if err := generateBuilderPackage(ctx, builderDir); err != nil {
		return fmt.Errorf("generate builder package: %w", err)
	}

	// Use a path on the container filesystem (not the bind-mounted project dir)
	// to avoid EINTR errors when Swift PM copies resource bundles across filesystem boundaries.
	buildPath := "/tmp/iosbox-native-build"

	swiftConfiguration := "debug"
	if ctx.release {
		swiftConfiguration = "release"
	}

	// Strategy 1: swift-sdk (Linux/WSL with registered SDK)
	if sdk.IsInstalled() {
		fmt.Println("  using registered swift-sdk")
		args := []string{
			"build",
			"--swift-sdk", "arm64-apple-ios",
			"--configuration", swiftConfiguration,
			"--build-path", buildPath,
		}
		// Make Flutter.framework headers visible to Obj-C plugin sources
		engineSlice := findEngineSlice(ctx.engineDir)
		if engineSlice != "" {
			args = append(args,
				"-Xswiftc", "-F", "-Xswiftc", engineSlice,
				"-Xcc", "-F", "-Xcc", engineSlice,
				"-Xlinker", "-F", "-Xlinker", engineSlice,
				"-Xlinker", "-framework", "-Xlinker", "Flutter",
			)
		}
		// Set rpath so dyld finds Flutter.framework in Frameworks/ at runtime.
		// Reserve space for LC_CODE_SIGNATURE so signing tools can inject a signature.
		args = append(args,
			"-Xlinker", "-rpath", "-Xlinker", "@executable_path/Frameworks",
			"-Xlinker", "-adhoc_codesign",
			"-Xlinker", "-headerpad_max_install_names",
		)
		// Link compiler-rt builtins for @available() checks (___isPlatformVersionAtLeast)
		if rtLib := sdk.ClangRTLibPath(); rtLib != "" {
			args = append(args, "-Xlinker", rtLib)
		}
		cmd := exec.Command("swift", args...)
		cmd.Dir = builderDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err == nil {
			return findAndSetBinary(ctx, buildPath)
		}
		return fmt.Errorf("swift build --swift-sdk arm64-apple-ios failed")
	}

	return fmt.Errorf("iOS SDK not installed — run `iosbox setup`")
}

func findAndSetBinary(ctx *buildContext, buildPath string) error {
	ctx.nativeBinary = findBinary(buildPath, "Runner")
	if ctx.nativeBinary == "" {
		return fmt.Errorf("compiled Runner binary not found in %s", buildPath)
	}
	ctx.nativeBuildDir = buildPath
	fmt.Printf("  binary: %s\n", ctx.nativeBinary)
	return nil
}

func generateBuilderPackage(ctx *buildContext, builderDir string) error {
	srcDir := filepath.Join(builderDir, "Sources", "Runner")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return err
	}

	iosDir := filepath.Join(ctx.projectPath, "ios")
	runnerDir := filepath.Join(iosDir, "Runner")

	entries, err := os.ReadDir(runnerDir)
	if err != nil {
		return fmt.Errorf("read runner dir: %w", err)
	}

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".swift") {
			if err := copyFile(filepath.Join(runnerDir, e.Name()), filepath.Join(srcDir, e.Name())); err != nil {
				return err
			}
		}
	}

	// Xcode builds use Main.storyboard (compiled to Main.storyboardc) to instantiate
	// the FlutterViewController. We can't compile storyboards on Linux, so we inject
	// the window setup code directly into AppDelegate.
	if err := patchAppDelegate(srcDir); err != nil {
		return fmt.Errorf("patch app delegate: %w", err)
	}

	engineFrameworkDir := findEngineSlice(ctx.engineDir)

	plugins := resolvePlugins(ctx.projectPath)

	if err := generateSwiftPluginRegistrantFromPlugins(srcDir, plugins); err != nil {
		return err
	}

	packageSwift := generatePackageSwiftWithPlugins(engineFrameworkDir, plugins)

	pkgPath := filepath.Join(builderDir, "Package.swift")
	if err := os.WriteFile(pkgPath, []byte(packageSwift), 0o644); err != nil {
		return err
	}

	fmt.Printf("  builder: %s\n", pkgPath)
	if len(plugins) > 0 {
		names := make([]string, len(plugins))
		for i, p := range plugins {
			names[i] = p.Name
		}
		fmt.Printf("  plugins: %s\n", strings.Join(names, ", "))
	}
	fmt.Printf("  engine=%s\n", engineFrameworkDir)
	return nil
}

func patchAppDelegate(srcDir string) error {
	// Create and run the FlutterEngine FIRST, then create the view controller,
	// then register plugins. This ensures the engine is ready before plugins try to use it.
	const appDelegate = `import Flutter
import UIKit

@main
@objc class AppDelegate: FlutterAppDelegate {
    let flutterEngine = FlutterEngine(name: "main")

    override func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]?
    ) -> Bool {
        flutterEngine.run()

        let flutterVC = FlutterViewController(engine: flutterEngine, nibName: nil, bundle: nil)
        self.window = UIWindow(frame: UIScreen.main.bounds)
        self.window?.rootViewController = flutterVC
        self.window?.makeKeyAndVisible()

        GeneratedPluginRegistrant.register(with: self)
        return super.application(application, didFinishLaunchingWithOptions: launchOptions)
    }
}
`
	return os.WriteFile(filepath.Join(srcDir, "AppDelegate.swift"), []byte(appDelegate), 0o644)
}

func resolvePlugins(projectPath string) []pluginInfo {
	depsFile := filepath.Join(projectPath, ".flutter-plugins-dependencies")
	data, err := os.ReadFile(depsFile)
	if err != nil {
		return nil
	}

	var deps struct {
		Plugins struct {
			IOS []struct {
				Name        string `json:"name"`
				Path        string `json:"path"`
				NativeBuild bool   `json:"native_build"`
			} `json:"ios"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &deps); err != nil {
		return nil
	}

	classMap := parsePluginClasses(projectPath)

	var plugins []pluginInfo
	for _, p := range deps.Plugins.IOS {
		if !p.NativeBuild {
			continue
		}

		var pkgDir string
		for _, sub := range []string{
			filepath.Join(p.Path, "darwin", p.Name),
			filepath.Join(p.Path, "ios", p.Name),
		} {
			if _, err := os.Stat(filepath.Join(sub, "Package.swift")); err == nil {
				pkgDir = sub
				break
			}
		}
		if pkgDir == "" {
			fmt.Printf("  warning: plugin %s has no SwiftPM package, skipping\n", p.Name)
			continue
		}

		// Module name = plugin name with underscores (same as SwiftPM target name)
		moduleName := p.Name

		className := classMap[p.Name]
		if className == "" {
			className = classMap[moduleName]
		}

		plugins = append(plugins, pluginInfo{
			Name:       p.Name,
			PkgDir:     pkgDir,
			ClassName:  className,
			ModuleName: moduleName,
		})
	}

	return plugins
}

// The @import and register lines are in separate blocks but in the same order,
// so we pair them positionally.
func parsePluginClasses(projectPath string) map[string]string {
	objcPath := filepath.Join(projectPath, "ios", "Runner", "GeneratedPluginRegistrant.m")
	data, _ := os.ReadFile(objcPath)

	var modules []string
	var classes []string

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "@import ") {
			mod := strings.TrimSuffix(strings.TrimPrefix(line, "@import "), ";")
			modules = append(modules, mod)
		}
		if strings.Contains(line, "registerWithRegistrar") && strings.HasPrefix(line, "[") {
			cls := strings.TrimPrefix(line, "[")
			if idx := strings.Index(cls, " "); idx > 0 {
				classes = append(classes, cls[:idx])
			}
		}
	}

	result := make(map[string]string)
	for i := 0; i < len(modules) && i < len(classes); i++ {
		result[modules[i]] = classes[i]
	}
	return result
}

func generateSwiftPluginRegistrantFromPlugins(srcDir string, plugins []pluginInfo) error {
	var imports, regCode strings.Builder

	for _, p := range plugins {
		if p.ClassName == "" {
			continue
		}
		fmt.Fprintf(&imports, "import %s\n", p.ModuleName)
		fmt.Fprintf(&regCode, "        %s.register(with: registry.registrar(forPlugin: \"%s\")!)\n", p.ClassName, p.ClassName)
	}

	swift := fmt.Sprintf(`import Flutter
%s
class GeneratedPluginRegistrant: NSObject {
    static func register(with registry: FlutterPluginRegistry) {
%s    }
}
`, imports.String(), regCode.String())

	return os.WriteFile(filepath.Join(srcDir, "GeneratedPluginRegistrant.swift"), []byte(swift), 0o644)
}

func findEngineSlice(xcframeworkDir string) string {
	if xcframeworkDir == "" {
		return ""
	}
	entries, _ := os.ReadDir(xcframeworkDir)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "ios-arm64") && !strings.Contains(e.Name(), "simulator") {
			candidate := filepath.Join(xcframeworkDir, e.Name())
			if _, err := os.Stat(filepath.Join(candidate, "Flutter.framework")); err == nil {
				return candidate
			}
		}
	}
	return ""
}

func generatePackageSwiftWithPlugins(engineFrameworkDir string, plugins []pluginInfo) string {
	var deps, targetDeps strings.Builder

	for _, p := range plugins {
		fmt.Fprintf(&deps, "        .package(path: %q),\n", p.PkgDir)
		pkgName := filepath.Base(p.PkgDir)
		productName := readSwiftPMProductName(p.PkgDir, p.Name)
		fmt.Fprintf(&targetDeps, "                .product(name: %q, package: %q),\n", productName, pkgName)
	}

	swiftSettings := ""
	linkerFlags := ""
	if engineFrameworkDir != "" {
		swiftSettings = fmt.Sprintf(`
            swiftSettings: [
                .unsafeFlags(["-F", %q]),
            ],`, engineFrameworkDir)
		linkerFlags = fmt.Sprintf(`
            linkerSettings: [
                .unsafeFlags(["-F", %q, "-framework", "Flutter"]),
            ]`, engineFrameworkDir)
	}

	return fmt.Sprintf(`// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "Runner",
    platforms: [.iOS(.v13)],
    dependencies: [
%s    ],
    targets: [
        .executableTarget(
            name: "Runner",
            dependencies: [
%s            ],
            path: "Sources/Runner",%s%s
        ),
    ]
)
`, deps.String(), targetDeps.String(), swiftSettings, linkerFlags)
}

func readSwiftPMProductName(pkgDir, pluginName string) string {
	data, err := os.ReadFile(filepath.Join(pkgDir, "Package.swift"))
	if err != nil {
		return strings.ReplaceAll(pluginName, "_", "-")
	}
	content := string(data)
	marker := `.library(name: "`
	if idx := strings.Index(content, marker); idx >= 0 {
		rest := content[idx+len(marker):]
		if end := strings.Index(rest, `"`); end >= 0 {
			return rest[:end]
		}
	}
	return strings.ReplaceAll(pluginName, "_", "-")
}

func findLD64() string {
	// Prefer the downloaded toolset (supports iOS) over the system ld64.lld
	// (apt-installed LLVM ld64.lld typically does NOT support iOS platform).
	candidates := []string{
		filepath.Join(sdk.ToolsetPath(), "bin", "ld64.lld"),
		"/usr/local/bin/ld64.lld",
	}
	// System llvm paths (fallback — may not support iOS)
	if matches, _ := filepath.Glob("/usr/lib/llvm-*/bin/ld64.lld"); len(matches) > 0 {
		candidates = append(candidates, matches...)
	}
	candidates = append(candidates, "/usr/bin/ld64.lld")
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func findBinary(buildPath, name string) string {
	candidates := []string{
		filepath.Join(buildPath, "debug", name),
		filepath.Join(buildPath, "release", name),
		filepath.Join(buildPath, "arm64-apple-ios", "debug", name),
		filepath.Join(buildPath, "arm64-apple-ios", "release", name),
		filepath.Join(buildPath, "arm64-apple-iphoneos", "debug", name),
		filepath.Join(buildPath, "arm64-apple-iphoneos", "release", name),
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	var found string
	filepath.Walk(buildPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() == name && info.Mode()&0o111 != 0 {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
