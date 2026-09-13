package windows

import "testing"

func TestIsRoutinePendingRename(t *testing.T) {
	tests := []struct {
		name  string
		entry string
		want  bool
	}{
		{name: "empty (delete pair's second half)", entry: "", want: true},
		{
			name:  "gaming services proxy dll, confirmed live",
			entry: `\??\C:\Windows\System32\gamingservicesproxy_13.dll.0`,
			want:  true,
		},
		{
			name:  "edge updater temp exe, confirmed live",
			entry: `\??\C:\Program Files (x86)\Microsoft\Edge\Temp\20476_794646704\old_msedge.exe`,
			want:  true,
		},
		{
			name:  "edge updater temp dir, confirmed live",
			entry: `\??\C:\Program Files (x86)\Microsoft\Edge\Temp\20476_794646704`,
			want:  true,
		},
		{
			// Regression: this exact bare-folder entry (no trailing
			// backslash/filename) is what a `...\temp\` pattern (with a
			// trailing backslash) missed -- confirmed live, it kept
			// "Reboot required" stuck true even with the rest of this
			// ignore-list already in place.
			name:  "edge updater temp folder itself (no trailing separator), confirmed live",
			entry: `\??\C:\Program Files (x86)\Microsoft\Edge\Temp`,
			want:  true,
		},
		{
			name:  "case-insensitive match",
			entry: `\??\C:\PROGRAM FILES (X86)\MICROSOFT\EDGE\TEMP\foo.tmp`,
			want:  true,
		},
		{
			name:  "windows installer rollback file -- a real pending change",
			entry: `\??\C:\Config.Msi\561b8f4a.rbf`,
			want:  false,
		},
		{
			name:  "onedrive updater -- a real pending change",
			entry: `\??\C:\Program Files\Microsoft OneDrive\Update\OneDriveSetup.exe`,
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRoutinePendingRename(tt.entry); got != tt.want {
				t.Errorf("isRoutinePendingRename(%q) = %v, want %v", tt.entry, got, tt.want)
			}
		})
	}
}

func TestAnyRealPendingRename(t *testing.T) {
	// Confirmed live on a real host (the exact 4-entry value read back
	// via `reg query` after upgrading to the fix that was supposed to
	// silence this, and still showed reboot-required stuck true): only
	// routine noise queued -- must not report a real pending change.
	onlyNoise := []string{
		`\??\C:\Windows\System32\gamingservicesproxy_13.dll.0`,
		`\??\C:\Program Files (x86)\Microsoft\Edge\Temp\20476_794646704\old_msedge.exe`,
		`\??\C:\Program Files (x86)\Microsoft\Edge\Temp\20476_794646704`,
		`\??\C:\Program Files (x86)\Microsoft\Edge\Temp`,
	}
	if anyRealPendingRename(onlyNoise) {
		t.Error("expected only-routine-noise entries to report no real pending rename")
	}

	// A real pending change mixed in among routine noise must still be
	// caught -- confirmed intent: when in doubt, err toward reporting
	// reboot-required.
	withReal := append(append([]string(nil), onlyNoise...),
		`\??\C:\Config.Msi\561b8f4a.rbf`, ``)
	if !anyRealPendingRename(withReal) {
		t.Error("expected a real pending rename mixed in with noise to still be reported")
	}

	if anyRealPendingRename(nil) {
		t.Error("expected an empty list to report no pending rename")
	}
}
