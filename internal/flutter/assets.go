package flutter

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MobAI-App/iosbox/internal/sdk"
)

// The flags mirror what xcode_backend.dart passes to flutter assemble.
func assembleDartAssets(ctx *buildContext) error {
	assetsDir := filepath.Join(ctx.buildDir, "flutter_assets")

	dartDefines := readDartDefines(ctx.projectPath)
	sdkRoot := resolveIOSSDKRoot()

	buildMode := "debug"
	configuration := "Debug"
	assembleTarget := "debug_ios_bundle_flutter_assets"
	trackWidgetCreation := "true"
	treeShakeIcons := "false"
	if ctx.release {
		buildMode = "release"
		configuration = "Release"
		assembleTarget = "release_ios_bundle_flutter_assets"
		trackWidgetCreation = "false"
		treeShakeIcons = "true"
	}

	cmd := exec.Command("flutter", "assemble",
		"--no-version-check",
		"--output", assetsDir,
		"--depfile", filepath.Join(ctx.buildDir, "flutter_assets.d"),
		"-dTargetPlatform=ios",
		"-dTargetFile=lib/main.dart",
		"-dBuildMode="+buildMode,
		"-dIosArchs=arm64",
		"-dSdkRoot="+sdkRoot,
		"-dConfiguration="+configuration,
		"-dTreeShakeIcons="+treeShakeIcons,
		"-dTrackWidgetCreation="+trackWidgetCreation,
		"-dDartObfuscation=false",
		"-dAction=build",
		"-dSplitDebugInfo=",
		"-dFrontendServerStarterPath=",
		"-dSrcRoot="+filepath.Join(ctx.projectPath, "ios"),
		"--DartDefines="+dartDefines,
		"--ExtraGenSnapshotOptions=",
		"--ExtraFrontEndOptions=",
		assembleTarget,
	)
	cmd.Dir = ctx.projectPath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("flutter assemble failed: %w", err)
	}

	ctx.dartAssetsDir = assetsDir
	fmt.Printf("  assets: %s\n", assetsDir)
	return nil
}

func resolveIOSSDKRoot() string {
	sdkRoot, err := sdk.IPhoneOSSDKRoot()
	if err != nil {
		log.Fatalf("failed to resolve iPhoneOS SDK root: %v", err)
	}
	return sdkRoot
}

func readDartDefines(projectPath string) string {
	xcconfigPath := filepath.Join(projectPath, "ios", "Flutter", "Generated.xcconfig")
	data, err := os.ReadFile(xcconfigPath)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "DART_DEFINES="); ok {
			return after
		}
	}
	return ""
}
