package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/MobAI-App/iosbox/internal/flutter"
	"github.com/MobAI-App/iosbox/internal/project"
	"github.com/MobAI-App/iosbox/internal/sdk"
	"github.com/urfave/cli/v3"
)

func main() {
	cmd := &cli.Command{
		Name:  "iosbox",
		Usage: "Build iOS apps on Linux",
		Commands: []*cli.Command{
			setupCommand(),
			buildCommand(),
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}

func setupCommand() *cli.Command {
	return &cli.Command{
		Name:      "setup",
		Usage:     "Extract iOS SDK from Xcode.app or Xcode.xip and register toolchain",
		ArgsUsage: "[path-to-Xcode.app|Xcode.xip]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "download-toolset",
				Usage: "Download ld64.lld toolset without extraction",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Bool("download-toolset") {
				fmt.Println("→ download toolset (ld64.lld)")
				if err := sdk.DownloadToolset(); err != nil {
					return fmt.Errorf("toolset download failed: %w", err)
				}
				fmt.Println("done!")
				return nil
			}

			xcodeApp := cmd.Args().First()
			if xcodeApp == "" {
				return fmt.Errorf("usage: iosbox setup <path-to-Xcode.app|Xcode.xip>")
			}

			if strings.HasSuffix(strings.ToLower(xcodeApp), ".xip") {
				fmt.Println("→ extract Xcode.xip")
				extractDir := xcodeApp + ".extracted"
				appPath, err := sdk.ExtractXIP(xcodeApp, extractDir)
				if err != nil {
					return fmt.Errorf("xip extraction failed: %w", err)
				}
				xcodeApp = appPath
				fmt.Printf("  extracted to: %s\n", xcodeApp)
			}

			fmt.Println("→ extract SDK from Xcode.app")
			if err := sdk.ExtractFromXcodeApp(xcodeApp); err != nil {
				return fmt.Errorf("extract failed: %w", err)
			}

			fmt.Println("→ download toolset (ld64.lld)")
			if err := sdk.DownloadToolset(); err != nil {
				return fmt.Errorf("toolset download failed: %w", err)
			}

			fmt.Println("→ register SDK")
			if err := sdk.RegisterSDK(); err != nil {
				return fmt.Errorf("registration failed: %w", err)
			}

			fmt.Println("\nsetup complete!")
			fmt.Printf("SDK extracted to: %s\n", sdk.SDKPath())
			fmt.Printf("Toolset downloaded to: %s\n", sdk.ToolsetPath())
			return nil
		},
	}
}

func buildCommand() *cli.Command {
	return &cli.Command{
		Name:      "build",
		Usage:     "Build a Flutter/SwiftUI project for iOS",
		ArgsUsage: "[project-path]",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			projectPath := cmd.Args().First()
			if projectPath == "" {
				projectPath = "."
			}

			if err := sdk.RegisterSDKWithSwift(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}

			ptype, err := project.Detect(projectPath)
			if err != nil {
				return fmt.Errorf("error: %w", err)
			}

			fmt.Printf("detected project type: %s\n", ptype)

			switch ptype {
			case project.Flutter:
				if err := flutter.Build(projectPath, false); err != nil {
					return fmt.Errorf("build failed: %w", err)
				}
			default:
				return fmt.Errorf("project type %s not yet supported", ptype)
			}
			return nil
		},
	}
}
