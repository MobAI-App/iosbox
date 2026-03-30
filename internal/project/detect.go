package project

import (
	"fmt"
	"os"
	"path/filepath"
)

type Type string

const (
	Flutter    Type = "flutter"
	SwiftUI    Type = "swiftui"
	ReactNative Type = "react-native"
)

func Detect(path string) (Type, error) {
	if _, err := os.Stat(filepath.Join(path, "pubspec.yaml")); err == nil {
		return Flutter, nil
	}

	if _, err := os.Stat(filepath.Join(path, "Package.swift")); err == nil {
		return SwiftUI, nil
	}

	if _, err := os.Stat(filepath.Join(path, "node_modules", "react-native")); err == nil {
		return ReactNative, nil
	}

	return "", fmt.Errorf("could not detect project type in %s", path)
}
