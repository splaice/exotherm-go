package archive

import (
	"os"
	"path/filepath"
	"testing"
)

// demoPassphrase / demoFixture: the bundled example archive (see the README).
const (
	demoPassphrase = "FF59E1881B4ED01C5C2F5F01890AFF25E9764D27"
	demoFixture    = "../../testdata/demo/SupportLogs.tar.gz.gpg.aa"
)

func TestExtractFullArchive(t *testing.T) {
	parts, err := CollectParts(demoFixture)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Extract(parts, demoPassphrase, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if res.PartialRecovery {
		t.Error("full archive should not report partial recovery")
	}
	if res.Files == 0 {
		t.Error("expected files extracted")
	}
}

// TestExtractRecoversTruncatedArchive checks that a truncated upload yields the
// complete members extracted before the cut-off (a partial recovery) rather than
// a hard failure. go-crypto errors mid-stream once the encrypted data is
// truncated, and Extract must salvage what decrypted.
func TestExtractRecoversTruncatedArchive(t *testing.T) {
	full, err := os.ReadFile(demoFixture)
	if err != nil {
		t.Fatal(err)
	}
	// Cut off the last 40% to simulate an incomplete upload.
	truncated := filepath.Join(t.TempDir(), "trunc.gpg")
	if err := os.WriteFile(truncated, full[:len(full)*60/100], 0o644); err != nil {
		t.Fatal(err)
	}

	parts, err := CollectParts(truncated)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Extract(parts, demoPassphrase, t.TempDir())
	if err != nil {
		t.Fatalf("expected partial recovery, got error: %v", err)
	}
	if !res.PartialRecovery {
		t.Error("expected PartialRecovery = true for a truncated archive")
	}
	if res.Files == 0 {
		t.Error("expected at least one complete file recovered")
	}
}
