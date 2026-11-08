//go:build !windows

package debian

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFakeDebsecan(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "debsecan")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return bin
}

func TestDetectParsesCVEsAndCounts(t *testing.T) {
	writeFakeDebsecan(t, `cat <<'EOF'
CVE-2024-0001 something
  - openssl (critical urgency)

CVE-2024-0002 something
  - curl (high urgency)

CVE-2024-0003 something
  - bash (medium urgency)

CVE-2024-0004 something
  - vim (low urgency)

CVE-2024-0005 something
  - zlib (high urgency)
EOF`)
	det, err := New(Config{Command: "debsecan"})
	if err != nil {
		t.Fatal(err)
	}
	info, err := det.Detect(context.Background(), nil)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if info.Total != 5 {
		t.Fatalf("Total = %d, want 5", info.Total)
	}
	if info.Critical != 1 || info.High != 2 || info.Medium != 1 || info.Low != 1 {
		t.Fatalf("counts = %+v", info)
	}
	if len(info.Items) != 5 {
		t.Fatalf("items = %d", len(info.Items))
	}
}

func TestDetectEmptyOutput(t *testing.T) {
	writeFakeDebsecan(t, `# nothing`)
	det, _ := New(Config{Command: "debsecan"})
	info, err := det.Detect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Total != 0 {
		t.Fatalf("Total = %d, want 0", info.Total)
	}
}

func TestDetectMissingBinary(t *testing.T) {
	det, _ := New(Config{Command: "definitely-not-a-binary"})
	_, err := det.Detect(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestDetectDedupesCVEs(t *testing.T) {
	writeFakeDebsecan(t, `cat <<'EOF'
CVE-2024-0001 something
  - openssl (high urgency)

CVE-2024-0001 something
  - openssl (high urgency)
EOF`)
	det, _ := New(Config{Command: "debsecan"})
	info, err := det.Detect(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if info.Total != 1 {
		t.Fatalf("Total = %d, want 1 (dedup)", info.Total)
	}
}

