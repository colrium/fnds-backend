package skills

var catalogNames = [...]string{
	"Mason",
	"Carpenter",
	"Electrician",
	"Plumber",
	"Painter",
	"Welder",
	"Tile Fixer",
	"Gardener",
	"Cleaner",
	"General Labourer",
	"Aluminum Fabrication & Installation",
}

// CatalogNames returns the trades supported by artisan profiles and vacancies.
func CatalogNames() []string {
	names := make([]string, len(catalogNames))
	copy(names, catalogNames[:])
	return names
}
