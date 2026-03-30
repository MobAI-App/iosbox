package flutter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func resolveEngine(ctx *buildContext) error {
	// The exact path varies by Flutter version, so we search for it.
	engineBase := filepath.Join(ctx.flutterRoot, "bin", "cache", "artifacts", "engine")

	candidates := []string{
		filepath.Join(engineBase, "ios", "Flutter.xcframework"),
		filepath.Join(engineBase, "ios-debug", "Flutter.xcframework"),
	}
	if ctx.release {
		candidates = []string{
			filepath.Join(engineBase, "ios-release", "Flutter.xcframework"),
			filepath.Join(engineBase, "ios", "Flutter.xcframework"),
		}
	}

	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			ctx.engineDir = c
			fmt.Printf("  engine: %s\n", c)
			return nil
		}
	}

	entries, err := os.ReadDir(engineBase)
	if err != nil {
		return fmt.Errorf("cannot read engine cache at %s: %w", engineBase, err)
	}

	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "ios") {
			continue
		}
		candidate := filepath.Join(engineBase, e.Name(), "Flutter.xcframework")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			ctx.engineDir = candidate
			fmt.Printf("  engine: %s\n", candidate)
			return nil
		}
	}

	return fmt.Errorf("Flutter.xcframework not found in %s — run `flutter precache --ios`", engineBase)
}
