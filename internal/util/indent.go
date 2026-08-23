package util

import (
	"regexp"
	"strings"
)

// Vendored from operator-go v0.12.6 pkg/util/code.go — removed upstream
// (#441); upstream #617 recorded the removal as permanent (no framework
// replacement). The indentation of rendered script/XML content is part of the
// Gen 2 parity contract. See docs/gen3-migration-design.md §7.

var reTab = regexp.MustCompile(`(^|\n)\t+`)

// IndentTabToSpaces converts leading tabs in a string to a specified number of
// spaces per tab.
func IndentTabToSpaces(code string, spacesPerTab int) string {
	return reTab.ReplaceAllStringFunc(code, func(match string) string {
		indentation := strings.Repeat(" ", (len(match))*spacesPerTab)
		if strings.HasPrefix(match, "\n") {
			indentation = "\n" + strings.Repeat(" ", (len(match)-len("\n"))*spacesPerTab)
		}
		return indentation
	})
}

// IndentTab4Spaces converts leading tabs to 4 spaces each.
func IndentTab4Spaces(code string) string {
	return IndentTabToSpaces(code, 4)
}
