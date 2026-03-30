package flutter

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MobAI-App/iosbox/internal/sdk"
)

type buildContext struct {
	projectPath string
	buildDir    string
	appDir      string
	release     bool

	flutterRoot    string // path to Flutter SDK
	engineDir      string // path to pre-built Flutter.xcframework
	dartAssetsDir  string // path to flutter_assets/
	nativeBinary   string // path to compiled Runner binary
	nativeBuildDir string // SwiftPM build output (contains plugin dylibs)
	ipaPath        string // path to output .ipa
}

func Build(projectPath string, release bool) error {
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	mode := "debug"
	if release {
		mode = "release"
	}

	ctx := &buildContext{
		projectPath: abs,
		buildDir:    filepath.Join(abs, "build", "iosbox"),
		appDir:      filepath.Join(abs, "build", "iosbox", "Runner.app"),
		release:     release,
	}

	fmt.Printf("build mode: %s\n", mode)

	if err := os.MkdirAll(ctx.buildDir, 0o755); err != nil {
		return fmt.Errorf("create build dir: %w", err)
	}

	// Prepend shims to PATH so flutter assemble finds xcrun/lipo/codesign replacements.
	shimsDir := sdk.ShimsDir()
	flutterShimsDir := sdk.FlutterShimsDir()
	path := os.Getenv("PATH")
	if _, err := os.Stat(flutterShimsDir); err == nil {
		path = flutterShimsDir + ":" + path
	}
	if _, err := os.Stat(shimsDir); err == nil {
		path = shimsDir + ":" + path
	}
	os.Setenv("PATH", path)
	fmt.Printf("using shims from %s\n", shimsDir)

	steps := []struct {
		name string
		fn   func(*buildContext) error
	}{
		{"resolve flutter SDK", resolveFlutterSDK},
		{"generate config", generateConfig},
		{"assemble dart assets", assembleDartAssets},
		{"resolve engine framework", resolveEngine},
		{"compile native code", compileNative},
		{"assemble app bundle", assembleApp},
		{"build native assets", buildNativeAssets},
		{"package ipa", packageIPA},
	}

	for _, step := range steps {
		fmt.Printf("→ %s\n", step.name)
		if err := step.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
		fmt.Printf("  ✓ done\n")
	}

	return nil
}

func packageIPA(ctx *buildContext) error {
	ipaPath := filepath.Join(ctx.buildDir, "Runner.ipa")
	f, err := os.Create(ipaPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	defer zw.Close()

	appName := filepath.Base(ctx.appDir)
	err = filepath.Walk(ctx.appDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(ctx.appDir, path)
		zipPath := "Payload/" + appName + "/" + rel
		zipPath = strings.TrimSuffix(zipPath, "/")

		if info.IsDir() {
			_, err := zw.Create(zipPath + "/")
			return err
		}

		// Preserve executable bit via external attributes
		mode := info.Mode()
		header := &zip.FileHeader{
			Name:   zipPath,
			Method: zip.Deflate,
		}
		header.SetMode(mode)

		w, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}

		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(w, src)
		return err
	})
	if err != nil {
		return err
	}

	ctx.ipaPath = ipaPath
	fmt.Printf("  ipa: %s\n", ipaPath)
	return nil
}
