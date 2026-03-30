# iosbox

**Build iOS apps in Docker. Works wherever Docker runs.**

`iosbox` provides a containerized environment for building iOS apps — no Mac, no Xcode installation, no external CI required. Point it at your `Xcode.xip` (downloaded directly from Apple) and your Flutter project, and get a `.ipa` out.

## How it works

1. **`iosbox setup`** — extract the iOS SDK and Swift toolchain from a downloaded `Xcode.xip` into a Docker volume, download a prebuilt `ld64.lld` linker, and register the Swift SDK for cross-compilation.

2. **`iosbox dev`** — run the full build pipeline in the container:
   - Compile Dart sources and collect assets (`flutter assemble`)
   - Resolve Flutter.xcframework from the cache
   - Generate a SwiftPM `Package.swift` wrapping your Swift sources and iOS plugins
   - Compile with `swift build --swift-sdk arm64-apple-ios`
   - Assemble a `.app` bundle and package it into `Runner.ipa`

The output `.ipa` is unsigned. Sign and install it with [MobAI](https://mobai.run), which handles code signing and OTA delivery out of the box.

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- `Xcode.xip` — downloaded from [developer.apple.com/download/all](https://developer.apple.com/download/all/) (Apple ID login required). Only needed once for the SDK setup step.

Swift, Flutter, and all build tooling are included in the image.

## Docker images

Pre-built images are available on [Docker Hub](https://hub.docker.com/r/mobaiapp/iosbox).

## Quick start

### 1. Download Xcode

Sign in at [developer.apple.com/download](https://developer.apple.com/download/all/?q=xcode%2026.3) with a free Apple ID and download **Xcode 26.3** — either `Apple_silicon` or `Universal` variant works.

> The download links require a browser session — direct `curl` won't work without auth cookies.

### 2. One-time SDK setup

Run this once to extract and register the iOS SDK into a named Docker volume:

```bash
docker run --rm \
  -v /path/to/Xcode_26.3_Apple_silicon.xip:/workspace/Xcode.xip:/workspace/Xcode.xip \
  -v iosbox-sdk:/root/.iosbox \
  mobaiapp/iosbox:latest \
  iosbox setup /workspace/Xcode.xip
```

The `iosbox-sdk` volume persists the extracted SDK — you won't need to repeat this step.

### 3. Build your Flutter app

```bash
# Debug build (default)
docker run --rm \
  -v iosbox-sdk:/root/.iosbox \
  -v /path/to/your-flutter-app:/project \
  mobaiapp/iosbox:latest \
  iosbox dev /project

# Release build (AOT, optimized, tree-shaken)
docker run --rm \
  -v iosbox-sdk:/root/.iosbox \
  -v /path/to/your-flutter-app:/project \
  mobaiapp/iosbox:latest \
  iosbox dev --release /project
```

The `.ipa` is written to `/path/to/your-flutter-app/build/iosbox/Runner.ipa`.

Sign, install, and run it with [ios-builder](https://github.com/MobAI-App/ios-builder) and [MobAI](https://mobai.run).

## Project support

| Project type | Status |
|---|---|
| Flutter (iOS) | Supported |
| SwiftUI / native iOS | Planned |
| React Native | Planned |

## Architecture

```
cmd/iosbox/          CLI entrypoint (setup, dev)
internal/
  flutter/          Flutter build pipeline
    build.go        Orchestrates the 8-step build
    native.go       SwiftPM package generation + swift build invocation
    bundle.go       .app bundle assembly
    engine.go       Flutter.xcframework resolution
    native_assets.go  Dart native assets support
    macho.go        Mach-O binary utilities
  sdk/
    sdk.go          SDK path helpers + Swift SDK JSON types
    extract.go      Xcode.app → .sdk bundle extraction
    xip.go          Xcode.xip → Xcode.app extraction (XAR + pbzx + CPIO)
    toolset.go      ld64.lld download + swift sdk install
    shims.go        Shim directory paths
  project/
    detect.go       Project type detection
shims/
  flutter/          xcrun / lipo / codesign stubs for flutter assemble
  clang             Intercepts clang calls targeting arm64-apple-ios
```

## Swift Package Manager

iosbox uses [Swift Package Manager](https://docs.flutter.dev/packages-and-plugins/swift-package-manager/for-app-developers) for native iOS plugin compilation. SwiftPM support in Flutter is still not enabled by default — the Docker image enables it via `flutter config --enable-swift-package-manager`. If your project uses CocoaPods-only plugins, they may need SwiftPM support added.

## Limitations & Disclaimer

- **Xcode 26.3 or earlier required.** Xcode 26.4+ ships SDK headers as stubs that require Apple's installer to reconstitute. This is not yet supported.
- Only physical devices are supported (`arm64-apple-ios`), no simulator.
- The `.ipa` is unsigned. Use [MobAI](https://mobai.run) for signing, installation, and debugging.
- You must supply your own `Xcode.xip` from Apple and comply with its license. This project is for research and educational use only, is not affiliated with Apple or Google, and is provided as-is without warranty.

## License

MIT
