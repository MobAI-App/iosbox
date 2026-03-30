package flutter

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func generateConfig(ctx *buildContext) error {
	flutterDir := filepath.Join(ctx.projectPath, "ios", "Flutter")
	if err := os.MkdirAll(flutterDir, 0o755); err != nil {
		return err
	}

	// Generate Generated.xcconfig (read by flutter assemble via -dDefines)
	xcconfig := fmt.Sprintf(`FLUTTER_ROOT=%s
FLUTTER_APPLICATION_PATH=%s
COCOAPODS_PARALLEL_CODE_SIGN=true
FLUTTER_TARGET=lib/main.dart
FLUTTER_BUILD_DIR=build
FLUTTER_BUILD_NAME=1.0.0
FLUTTER_BUILD_NUMBER=1
DART_OBFUSCATION=false
TRACK_WIDGET_CREATION=true
TREE_SHAKE_ICONS=false
PACKAGE_CONFIG=%s
`, ctx.flutterRoot, ctx.projectPath,
		filepath.Join(ctx.projectPath, ".dart_tool", "package_config.json"))

	xcconfigPath := filepath.Join(flutterDir, "Generated.xcconfig")
	if err := os.WriteFile(xcconfigPath, []byte(xcconfig), 0o644); err != nil {
		return err
	}

	// Always run flutter pub get to ensure packages resolve to container paths
	cmd := exec.Command("flutter", "pub", "get")
	cmd.Dir = ctx.projectPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("flutter pub get failed:\n%s\n%w", string(out), err)
	}

	fmt.Println("  generated config for Linux")
	return nil
}
