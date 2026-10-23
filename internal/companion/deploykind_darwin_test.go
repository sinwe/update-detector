//go:build darwin

package companion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeUnitPresentDarwin(t *testing.T) {
	dir := t.TempDir()
	orig := launchdPlistDir
	launchdPlistDir = dir
	t.Cleanup(func() { launchdPlistDir = orig })

	if nativeUnitPresent("update-detector") {
		t.Fatal("expected absent with no plist file present")
	}
	if err := os.WriteFile(filepath.Join(dir, "com.sinwe.update-detector.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !nativeUnitPresent("update-detector") {
		t.Fatal("expected present once com.sinwe.update-detector.plist exists")
	}
	// A systemd unit file must NOT count on darwin -- wrong platform's
	// marker, same reason the linux file ignores plists.
	if err := os.WriteFile(filepath.Join(dir, "update-detector.service"), []byte("[Unit]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if nativeUnitPresent("update-detector-companion") {
		t.Fatal("expected absent: only its own plist counts, not a .service file")
	}
}
