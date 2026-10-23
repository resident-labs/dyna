package hooks

import (
	"context"
	"sort"
	"strings"

	"dyna-agent/internal/repoid"
	"dyna-agent/internal/shellquote"
)

// Recipe is one built-in hook that the owner enables with hooks add --recipe.
// Build only makes configuration; the owner decides to install it.
type Recipe struct {
	Label string
	Build func(ctx context.Context, repo, exe, name string) (Hook, error)
}

// doctorLabel is the hook label and help text for the doctor recipe.
const doctorLabel = "Doctor load watchdog"

// Recipes is the single list of built-in recipes. The CLI and its help text
// read it, so a new recipe needs no second switch.
var Recipes = map[string]Recipe{
	"doctor":      {Label: doctorLabel, Build: DoctorRecipe},
	"self-update": {Label: "Update Dyna", Build: SelfUpdateRecipe},
}

// DoctorCondition starts an examination only while the machine is under load.
const DoctorCondition = "load>2/core for 5m or memory>85% or pressure.memory>20%"

// DoctorRecipe examines the machine every minute, and only reports while the
// condition holds. It needs no source tree: dyna doctor ships in the binary.
func DoctorRecipe(ctx context.Context, repo, exe, name string) (Hook, error) {
	if _, err := repoid.Resolve(ctx, repo); err != nil {
		return Hook{}, err
	}
	h := Hook{
		Name:         name,
		Label:        doctorLabel,
		Event:        Schedule,
		Command:      shellquote.Quote(exe) + " doctor examine machine --if " + shellquote.Quote(DoctorCondition),
		Cwd:          "main",
		Timeout:      "1m",
		OnFail:       "alert",
		EveryMinutes: 1,
	}
	return h, Validate(h)
}

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
