package hooks

import (
	"context"
	"sort"
	"strings"
)

// Recipe is one built-in hook that the owner enables with hooks add --recipe.
// Build only makes configuration; the owner decides to install it.
type Recipe struct {
	Label string
	Build func(ctx context.Context, repo, exe, name string) (Hook, error)
}

// Recipes is the single list of built-in recipes. The CLI and its help text
// read it, so a new recipe needs no second switch.
var Recipes = map[string]Recipe{
	"self-update": {Label: "Update Dyna", Build: SelfUpdateRecipe},
}

// DoctorCondition is the default condition of the doctor hook: it examines
// the machine only while it is under load.
const DoctorCondition = "load>2/core for 5m or memory>85% or pressure.memory>20%"

// RecipeNames lists the recipe names in a stable order for help text.
func RecipeNames() []string {
	names := make([]string, 0, len(Recipes))
	for name := range Recipes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RecipeHelp names every recipe and its label for flag help.
func RecipeHelp() string {
	parts := make([]string, 0, len(Recipes))
	for _, name := range RecipeNames() {
		parts = append(parts, name+" ("+Recipes[name].Label+")")
	}
	return strings.Join(parts, ", ")
}
