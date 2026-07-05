package ubuntu

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCheckRebootRequired(t *testing.T) {
	t.Run("file absent", func(t *testing.T) {
		required, pkgs, err := checkRebootRequired(filepath.Join(t.TempDir(), "reboot-required"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if required || pkgs != nil {
			t.Fatalf("got required=%v pkgs=%v, want false/nil", required, pkgs)
		}
	})

	t.Run("file present without pkgs sidecar", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "reboot-required")
		if err := os.WriteFile(marker, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		required, pkgs, err := checkRebootRequired(marker)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !required || pkgs != nil {
			t.Fatalf("got required=%v pkgs=%v, want true/nil", required, pkgs)
		}
	})

	t.Run("file present with pkgs sidecar", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "reboot-required")
		if err := os.WriteFile(marker, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker+".pkgs", []byte("linux-image-generic\n\nlibc6\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		required, pkgs, err := checkRebootRequired(marker)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"linux-image-generic", "libc6"}
		if !required || !reflect.DeepEqual(pkgs, want) {
			t.Fatalf("got required=%v pkgs=%#v, want true/%#v", required, pkgs, want)
		}
	})
}
