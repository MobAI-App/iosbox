package sdk

import (
	"path/filepath"
)

func ShimsDir() string {
	return filepath.Join(IosBoxHome(), "shims")
}

func FlutterShimsDir() string {
	return filepath.Join(ShimsDir(), "flutter")
}
