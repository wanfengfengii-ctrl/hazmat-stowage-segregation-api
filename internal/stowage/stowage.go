// Package stowage decides whether a set of dangerous-goods containers may be
// loaded into the same vessel area during terminal pre-stowage planning.
//
// The adjudication rules, in evaluation order, are:
//
//  1. Hazard class 1 conflicts with any OTHER hazard class.
//  2. Hazard class 5.1 against class 3 or 4.1 conflicts only when the
//     absolute bay difference is below 2.
//  3. Hazard class 8 against class 2.1 is compatible only when the decks
//     differ AND the absolute bay difference is at least 1; otherwise it
//     conflicts.
//
// Every other combination is compatible. When several rules match the same
// pair, the first rule in the order above supplies the reported reason.
package stowage

import (
	"fmt"
	"sort"
)

// Rule codes, listed in evaluation order.
const (
	RuleClass1Isolation  = "CLASS_1_ISOLATION"
	RuleOxidizerBaySep   = "OXIDIZER_BAY_SEPARATION"
	RuleCorrosiveFlamGas = "CORROSIVE_FLAMMABLE_GAS_SEPARATION"
)

// Item is one declared dangerous-goods container in a pre-stowage manifest.
type Item struct {
	CargoID     string
	HazardClass string // one of "1", "2.1", "3", "4.1", "5.1", "8"
	Bay         int    // 1..30
	Deck        string // "U" (upper) or "L" (lower)
}

// Conflict describes one rejected unordered pair of containers.
type Conflict struct {
	Pair   [2]string `json:"pair"`   // cargo IDs, ascending
	Rule   string    `json:"rule"`   // code of the first matching rule
	Reason string    `json:"reason"` // human-readable explanation
}

// Adjudicate checks every unordered pair of items and returns the number of
// checked pairs together with the detected conflicts. The result is
// deterministic and independent of the input order: cargo IDs are ascending
// inside each pair and pairs are ordered lexicographically by their IDs.
func Adjudicate(items []Item) (checkedPairs int, conflicts []Conflict) {
	conflicts = []Conflict{}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			checkedPairs++
			if c, ok := pairConflict(items[i], items[j]); ok {
				conflicts = append(conflicts, c)
			}
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].Pair[0] != conflicts[j].Pair[0] {
			return conflicts[i].Pair[0] < conflicts[j].Pair[0]
		}
		return conflicts[i].Pair[1] < conflicts[j].Pair[1]
	})
	return checkedPairs, conflicts
}

// pairConflict applies the segregation rules to one unordered pair, in rule
// order, and reports the first matching rule.
func pairConflict(a, b Item) (Conflict, bool) {
	pair := sortedPair(a.CargoID, b.CargoID)

	// Rule 1: class 1 conflicts with any other hazard class. Two class-1
	// containers share the same class, so the pair is not covered here.
	if (a.HazardClass == "1") != (b.HazardClass == "1") {
		return Conflict{
			Pair:   pair,
			Rule:   RuleClass1Isolation,
			Reason: "hazard class 1 conflicts with any other hazard class",
		}, true
	}

	// Rule 2: class 5.1 against class 3 or 4.1 conflicts only when the
	// absolute bay difference is below 2.
	if other, ok := oxidizerPair(a.HazardClass, b.HazardClass); ok {
		if d := abs(a.Bay - b.Bay); d < 2 {
			return Conflict{
				Pair: pair,
				Rule: RuleOxidizerBaySep,
				Reason: fmt.Sprintf(
					"hazard class 5.1 and class %s require bay separation >= 2 (%s bay %d, %s bay %d)",
					other, pair[0], bayOf(pair[0], a, b), pair[1], bayOf(pair[1], a, b)),
			}, true
		}
		return Conflict{}, false
	}

	// Rule 3: class 8 against class 2.1 is compatible only when the decks
	// differ and the absolute bay difference is at least 1.
	if sameClasses(a.HazardClass, b.HazardClass, "8", "2.1") {
		if a.Deck != b.Deck && abs(a.Bay-b.Bay) >= 1 {
			return Conflict{}, false
		}
		return Conflict{
			Pair: pair,
			Rule: RuleCorrosiveFlamGas,
			Reason: fmt.Sprintf(
				"hazard class 8 and class 2.1 require different decks and bay separation >= 1 (%s deck %s bay %d, %s deck %s bay %d)",
				pair[0], deckOf(pair[0], a, b), bayOf(pair[0], a, b),
				pair[1], deckOf(pair[1], a, b), bayOf(pair[1], a, b)),
		}, true
	}

	return Conflict{}, false
}

// sortedPair returns both cargo IDs in ascending order.
func sortedPair(x, y string) [2]string {
	if y < x {
		return [2]string{y, x}
	}
	return [2]string{x, y}
}

// sameClasses reports whether x and y are exactly the two given classes, in
// any order.
func sameClasses(x, y, want1, want2 string) bool {
	return (x == want1 && y == want2) || (x == want2 && y == want1)
}

// oxidizerPair reports whether the two classes are {5.1, 3} or {5.1, 4.1}
// and, if so, returns the non-5.1 class.
func oxidizerPair(x, y string) (string, bool) {
	for _, other := range []string{"3", "4.1"} {
		if sameClasses(x, y, "5.1", other) {
			return other, true
		}
	}
	return "", false
}

// bayOf returns the bay of the item whose cargo ID is id.
func bayOf(id string, a, b Item) int {
	if a.CargoID == id {
		return a.Bay
	}
	return b.Bay
}

// deckOf returns the deck of the item whose cargo ID is id.
func deckOf(id string, a, b Item) string {
	if a.CargoID == id {
		return a.Deck
	}
	return b.Deck
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
