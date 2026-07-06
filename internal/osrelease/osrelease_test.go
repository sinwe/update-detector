package osrelease

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	raw := "NAME=\"Ubuntu\"\nVERSION_ID=\"22.04\"\nVERSION_CODENAME=jammy\n# comment\n\nID=ubuntu\n"
	got := Parse(raw)
	if got["NAME"] != "Ubuntu" || got["VERSION_ID"] != "22.04" || got["VERSION_CODENAME"] != "jammy" || got["ID"] != "ubuntu" {
		t.Fatalf("unexpected parse result: %#v", got)
	}
}

func TestReadID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte("ID=debian\nVERSION_ID=\"13\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := ReadID(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "debian" {
		t.Fatalf("got %q, want debian", id)
	}
}

func TestReadIDMissingFile(t *testing.T) {
	_, err := ReadID(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
