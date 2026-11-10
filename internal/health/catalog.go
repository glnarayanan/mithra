package health

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

// The catalog names common measurements and lab tests, so typed entries and
// imported reports use one name for the same test. It carries no judgement:
// reference ranges shown to people always come from the record itself.
//
//go:embed catalog.json
var catalogJSON []byte

type CatalogTest struct {
	Code        string   `json:"code"`
	Name        string   `json:"canonical_name"`
	Category    string   `json:"category"`
	DefaultUnit string   `json:"default_unit"`
	Synonyms    []string `json:"synonyms"`
}

var (
	catalogTests []CatalogTest
	catalogIndex map[string]int
)

func init() {
	if err := json.Unmarshal(catalogJSON, &catalogTests); err != nil {
		panic("health catalog: " + err.Error())
	}
	catalogIndex = map[string]int{}
	for index, test := range catalogTests {
		catalogIndex[catalogKey(test.Name)] = index
		catalogIndex[catalogKey(test.Code)] = index
		for _, synonym := range test.Synonyms {
			if _, taken := catalogIndex[catalogKey(synonym)]; !taken {
				catalogIndex[catalogKey(synonym)] = index
			}
		}
	}
}

func catalogKey(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.NewReplacer(",", " ", "-", " ", "_", " ").Replace(name))), " ")
}

// Catalog returns every known test, ordered by category then name.
func Catalog() []CatalogTest {
	result := append([]CatalogTest(nil), catalogTests...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return categoryRank(result[i].Category) < categoryRank(result[j].Category)
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// LookupTest finds a test by its name, code or a common synonym.
func LookupTest(name string) (CatalogTest, bool) {
	index, ok := catalogIndex[catalogKey(name)]
	if !ok {
		return CatalogTest{}, false
	}
	return catalogTests[index], true
}

// CanonicalAnalyte returns the catalog name for a known test, or the trimmed
// input unchanged.
func CanonicalAnalyte(name string) string {
	if test, ok := LookupTest(name); ok {
		return test.Name
	}
	return strings.TrimSpace(name)
}

// CategoryFor returns the catalog category for an analyte, or "Other".
func CategoryFor(analyte string) string {
	if test, ok := LookupTest(analyte); ok {
		return test.Category
	}
	return "Other"
}

func categoryRank(category string) int {
	order := []string{"Body", "Diabetes", "Lipid", "Thyroid", "Kidney", "Liver", "CBC", "Iron", "Vitamins", "Electrolytes", "Hormones", "Inflammation"}
	for index, name := range order {
		if name == category {
			return index
		}
	}
	return len(order)
}

// CategoryRank orders categories on the Health page.
func CategoryRank(category string) int { return categoryRank(category) }
