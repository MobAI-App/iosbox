package flutter

import (
	"fmt"
	"os/exec"
	"strings"
)

func resolveFlutterSDK(ctx *buildContext) error {
	flutterBin, err := exec.LookPath("flutter")
	if err != nil {
		return fmt.Errorf("flutter not found in PATH: %w", err)
	}

	out, err := exec.Command("realpath", flutterBin).Output()
	if err != nil {
		out = []byte(flutterBin)
	}

	resolved := strings.TrimSpace(string(out))
	sdkRoot := resolved
	for i := 0; i < 2; i++ {
		idx := strings.LastIndex(sdkRoot, "/")
		if idx < 0 {
			return fmt.Errorf("unexpected flutter binary path: %s", resolved)
		}
		sdkRoot = sdkRoot[:idx]
	}

	ctx.flutterRoot = sdkRoot
	fmt.Printf("  flutter SDK: %s\n", sdkRoot)
	return nil
}
