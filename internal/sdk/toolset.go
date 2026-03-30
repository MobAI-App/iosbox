package sdk

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// xtool's prebuilt darwin tools repo
	darwinToolsRepo = "https://github.com/xtool-org/darwin-tools-linux-llvm/releases/latest/download"
)

func DownloadToolset() error {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	}
	if arch == "arm64" {
		arch = "aarch64"
	}

	url := fmt.Sprintf("%s/toolset-%s.tar.gz", darwinToolsRepo, arch)
	destDir := filepath.Join(ToolsetPath(), "bin")

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(destDir, "ld64.lld")); err == nil {
		fmt.Println("  toolset already downloaded")
		return nil
	}

	fmt.Printf("  downloading toolset from %s\n", url)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download toolset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download toolset: HTTP %d", resp.StatusCode)
	}

	return extractTarGz(resp.Body, destDir)
}

func extractTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Flatten: extract all files directly into destDir
		name := filepath.Base(hdr.Name)
		if name == "." || strings.HasPrefix(name, ".") {
			continue
		}

		target := filepath.Join(destDir, name)

		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)|0o755)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			os.Remove(target) // remove if exists
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}

	return nil
}

func RegisterSDK() error {
	// Copy shims from the image bake location into the persistent volume.
	// The shims are baked into the Docker image at /usr/local/lib/iosbox/shims/.
	shimsSrc := "/usr/local/lib/iosbox/shims"
	if _, err := os.Stat(shimsSrc); err != nil {
		fmt.Printf("  shims source not found at %s, skipping copy\n", shimsSrc)
		return nil
	}
	if err := copyTree(shimsSrc, ShimsDir()); err != nil {
		return fmt.Errorf("copy shims: %w", err)
	}
	fmt.Printf("  shims copied to %s\n", ShimsDir())
	return nil
}

// RegisterSDKWithSwift registers the SDK with the Swift toolchain for the
// current container session. Must be called at build time (not setup) since
// swift sdk install writes to ~/.swiftpm which is not on the persistent volume.
func RegisterSDKWithSwift() error {
	fmt.Println("  registering SDK with swift toolchain...")
	cmd := exec.Command("swift", "sdk", "install", SDKPath())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: swift sdk install failed: %v\n", err)
	}
	return nil
}
