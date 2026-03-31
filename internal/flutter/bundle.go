package flutter

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// assembleApp creates the .app bundle structure from compiled artifacts.
//
// Expected structure:
//
//	Runner.app/
//	├── Runner                  (executable)
//	├── Info.plist
//	├── Frameworks/
//	│   ├── Flutter.framework/
//	│   └── App.framework/
//	│       └── flutter_assets/
//	│           ├── kernel_blob.bin
//	│           └── ...
//	└── (optional) flutter_assets/ symlink or copy
func assembleApp(ctx *buildContext) error {
	appDir := ctx.appDir

	os.RemoveAll(appDir)

	if err := os.MkdirAll(filepath.Join(appDir, "Frameworks"), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	if ctx.nativeBinary != "" {
		if err := copyFile(ctx.nativeBinary, filepath.Join(appDir, "Runner")); err != nil {
			return fmt.Errorf("copy runner binary: %w", err)
		}
	}

	if ctx.dartAssetsDir != "" {
		appFrameworkSrc := filepath.Join(ctx.dartAssetsDir, "App.framework")
		if _, err := os.Stat(appFrameworkSrc); err == nil {
			// Always copy the full App.framework — for release it contains the AOT
			// snapshot binary; for debug it contains flutter_assets + kernel_blob.bin.
			if err := copyDir(appFrameworkSrc, filepath.Join(appDir, "Frameworks", "App.framework")); err != nil {
				return fmt.Errorf("copy App.framework: %w", err)
			}
		}

		if err := copyFlutterEngine(ctx.engineDir, filepath.Join(appDir, "Frameworks")); err != nil {
			return fmt.Errorf("copy flutter engine: %w", err)
		}
	}

	if ctx.nativeBuildDir != "" {
		frameworksDir := filepath.Join(appDir, "Frameworks")
		bundled := make(map[string]bool)
		filepath.Walk(ctx.nativeBuildDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.HasSuffix(info.Name(), ".dylib") && !strings.Contains(path, ".build/") && !bundled[info.Name()] {
				if !isMachODylib(path) {
					return nil
				}
				dst := filepath.Join(frameworksDir, info.Name())
				if copyErr := copyFile(path, dst); copyErr == nil {
					// Ensure dylib has LC_CODE_SIGNATURE for signing tools
					if err := ensureCodeSignatureSpace(dst); err != nil {
						fmt.Printf("  warning: %s: %v\n", info.Name(), err)
					}
					bundled[info.Name()] = true
					fmt.Printf("  bundled: %s\n", info.Name())
				}
			}
			return nil
		})
	}

	// Copy bundle resources from ios/Runner/ (e.g. GoogleService-Info.plist, assets).
	// Xcode does this via "Copy Bundle Resources"; we replicate it here.
	runnerDir := filepath.Join(ctx.projectPath, "ios", "Runner")
	if entries, err := os.ReadDir(runnerDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			// Skip source code, Info.plist (generated separately), and build dirs
			if strings.HasSuffix(name, ".swift") || strings.HasSuffix(name, ".h") ||
				strings.HasSuffix(name, ".m") || name == "Info.plist" ||
				name == "Base.lproj" || name == "Assets.xcassets" {
				continue
			}
			src := filepath.Join(runnerDir, name)
			dst := filepath.Join(appDir, name)
			info, _ := e.Info()
			if info != nil && info.IsDir() {
				if err := copyDir(src, dst); err != nil {
					fmt.Printf("  warning: copy %s: %v\n", name, err)
				}
			} else {
				if err := copyFile(src, dst); err != nil {
					fmt.Printf("  warning: copy %s: %v\n", name, err)
				}
			}
		}
	}

	infoPlist := generateInfoPlist(ctx)
	if err := os.WriteFile(filepath.Join(appDir, "Info.plist"), []byte(infoPlist), 0o644); err != nil {
		return fmt.Errorf("write Info.plist: %w", err)
	}

	return nil
}

func copyFlutterEngine(xcframeworkPath, destDir string) error {
	entries, _ := os.ReadDir(xcframeworkPath)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "ios-arm64") && !strings.Contains(e.Name(), "simulator") {
			candidate := filepath.Join(xcframeworkPath, e.Name(), "Flutter.framework")
			if _, err := os.Stat(candidate); err == nil {
				return copyDir(candidate, filepath.Join(destDir, "Flutter.framework"))
			}
		}
	}
	return fmt.Errorf("no ios-arm64 slice found in %s", xcframeworkPath)
}

func generateInfoPlist(ctx *buildContext) string {
	projectPlist := filepath.Join(ctx.projectPath, "ios", "Runner", "Info.plist")
	if data, err := os.ReadFile(projectPlist); err == nil {
		return resolveInfoPlistVars(string(data), ctx.projectPath, ctx.release)
	}

	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleExecutable</key>
    <string>Runner</string>
    <key>CFBundleIdentifier</key>
    <string>com.example.app</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>Runner</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleVersion</key>
    <string>1.0</string>
    <key>MinimumOSVersion</key>
    <string>13.0</string>
</dict>
</plist>
`
}

// Flutter's Info.plist uses xcconfig $(VAR) references that must be resolved before signing.
func resolveInfoPlistVars(content, projectPath string, release bool) string {
	bundleID := resolveBundleID(projectPath)
	if bundleID == "" {
		bundleID = "com.example.app"
	}

	appName := bundleID
	if idx := strings.LastIndex(bundleID, "."); idx >= 0 {
		appName = bundleID[idx+1:]
	}
	// Sanitize: underscores not allowed in Apple appIdName
	appName = strings.ReplaceAll(appName, "_", "")

	buildName := "1.0.0"
	buildNumber := "1"
	xcconfigPath := filepath.Join(projectPath, "ios", "Flutter", "Generated.xcconfig")
	if xdata, err := os.ReadFile(xcconfigPath); err == nil {
		for _, line := range strings.Split(string(xdata), "\n") {
			if v, ok := strings.CutPrefix(line, "FLUTTER_BUILD_NAME="); ok {
				buildName = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "FLUTTER_BUILD_NUMBER="); ok {
				buildNumber = strings.TrimSpace(v)
			}
		}
	}

	vars := map[string]string{
		"PRODUCT_BUNDLE_IDENTIFIER": bundleID,
		"PRODUCT_NAME":              appName,
		"EXECUTABLE_NAME":           "Runner",
		"DEVELOPMENT_LANGUAGE":      "en",
		"FLUTTER_BUILD_NAME":        buildName,
		"FLUTTER_BUILD_NUMBER":      buildNumber,
	}

	re := regexp.MustCompile(`\$\(([^)]+)\)`)
	content = re.ReplaceAllStringFunc(content, func(match string) string {
		key := match[2 : len(match)-1]
		if val, ok := vars[key]; ok {
			return val
		}
		return match // leave unknown vars as-is
	})

	// Remove UIMainStoryboardFile — Flutter sets up the window via FlutterAppDelegate,
	// no storyboard needed, and we don't bundle compiled .storyboardc files.
	mainStoryRe := regexp.MustCompile(`\s*<key>UIMainStoryboardFile</key>\s*<string>[^<]*</string>`)
	content = mainStoryRe.ReplaceAllString(content, "")

	// Sanitize CFBundleName: Apple rejects underscores in appIdName.
	// Replace literal underscore-containing values in the plist string.
	bundleNameRe := regexp.MustCompile(`(<key>CFBundleName</key>\s*<string>)([^<]+)(</string>)`)
	content = bundleNameRe.ReplaceAllStringFunc(content, func(match string) string {
		parts := bundleNameRe.FindStringSubmatch(match)
		name := strings.ReplaceAll(parts[2], "_", "")
		return parts[1] + name + parts[3]
	})

	// Inject NSBonjourServices for Flutter VM service discovery (debug only).
	// Flutter 3.x on iOS uses mDNS (_dartVmService._tcp) to announce the debug VM service.
	// Without this key, the VM service can't register via mDNS and flutter attach fails.
	// Xcode injects this automatically for debug builds; release builds don't need it.
	if !release && !strings.Contains(content, "NSBonjourServices") {
		inject := `	<key>NSBonjourServices</key>
	<array>
		<string>_dartVmService._tcp</string>
	</array>
	<key>NSLocalNetworkUsageDescription</key>
	<string>Allow Flutter tools on your computer to connect and debug your application. This prompt will not appear on release builds.</string>
`
		content = strings.Replace(content, "</dict>", inject+"</dict>", 1)
	}

	return content
}

func resolveBundleID(projectPath string) string {
	pbxproj := filepath.Join(projectPath, "ios", "Runner.xcodeproj", "project.pbxproj")
	data, err := os.ReadFile(pbxproj)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`PRODUCT_BUNDLE_IDENTIFIER\s*=\s*([^;]+);`)
	matches := re.FindAllSubmatch(data, -1)
	for _, m := range matches {
		id := strings.TrimSpace(string(m[1]))
		if id != "" && !strings.Contains(id, "$(") {
			return id
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	info, err := in.Stat()
	if err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode())
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}

		return copyFile(path, target)
	})
}
