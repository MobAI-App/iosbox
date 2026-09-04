package flutter

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeploymentTarget(t *testing.T) {
	cases := []struct {
		name  string
		pbx   string
		want  string
		noPbx bool
	}{
		{name: "missing project", noPbx: true, want: "13.0"},
		{name: "no setting", pbx: "PRODUCT_NAME = Runner;", want: "13.0"},
		{name: "single", pbx: "IPHONEOS_DEPLOYMENT_TARGET = 13.0;", want: "13.0"},
		{
			// flutter create sets 13.0 at project level; raising the minimum in
			// Xcode adds a higher value on the Runner target further down the file.
			name: "highest wins regardless of order",
			pbx: `IPHONEOS_DEPLOYMENT_TARGET = 13.0;
IPHONEOS_DEPLOYMENT_TARGET = 14.0;
IPHONEOS_DEPLOYMENT_TARGET = 13.0;`,
			want: "14.0",
		},
		{name: "numeric not lexical", pbx: "IPHONEOS_DEPLOYMENT_TARGET = 9.0;\nIPHONEOS_DEPLOYMENT_TARGET = 15.5;", want: "15.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iosDir := t.TempDir()
			if !tc.noPbx {
				proj := filepath.Join(iosDir, "Runner.xcodeproj")
				if err := os.MkdirAll(proj, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(proj, "project.pbxproj"), []byte(tc.pbx), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := deploymentTarget(iosDir); got != tc.want {
				t.Errorf("deploymentTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}
