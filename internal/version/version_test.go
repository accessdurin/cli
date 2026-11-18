package version

import (
	"os"
	"testing"
)

func TestLicenseMatchesTheRepository(t *testing.T) {
	root, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	if string(root) != License {
		t.Fatal("internal/version/LICENSE differs from the repository LICENSE; copy it again")
	}
}
