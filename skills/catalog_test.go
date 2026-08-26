package skills

import (
	"slices"
	"testing"
)

func TestCatalogNamesIncludesAluminumFabricationAndInstallation(t *testing.T) {
	const name = "Aluminum Fabrication & Installation"
	if !slices.Contains(CatalogNames(), name) {
		t.Fatalf("CatalogNames() does not include %q", name)
	}
}

func TestCatalogNamesIncludesDriver(t *testing.T) {
	const name = "Driver"
	if !slices.Contains(CatalogNames(), name) {
		t.Fatalf("CatalogNames() does not include %q", name)
	}
}

func TestCatalogNamesReturnsCopy(t *testing.T) {
	names := CatalogNames()
	names[0] = "changed"

	if CatalogNames()[0] == "changed" {
		t.Fatal("CatalogNames() returned mutable catalog storage")
	}
}
