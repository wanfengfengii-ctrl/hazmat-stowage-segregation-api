package stowage

import (
	"fmt"
	"testing"
)

// item builds an Item with the given class, bay and deck and a generated ID.
func item(id string, class string, bay int, deck string) Item {
	return Item{CargoID: id, HazardClass: class, Bay: bay, Deck: deck}
}

// TestClassMatrix adjudicates every unordered combination of the six allowed
// hazard classes at a fixed, widely separated position (bay 10 vs 12, both
// upper deck) and checks the verdict for each pair.
func TestClassMatrix(t *testing.T) {
	classes := []string{"1", "2.1", "3", "4.1", "5.1", "8"}

	// want[c1][c2] = expected rule code, or "" for compatible.
	want := map[string]map[string]string{
		"1":   {"2.1": RuleClass1Isolation, "3": RuleClass1Isolation, "4.1": RuleClass1Isolation, "5.1": RuleClass1Isolation, "8": RuleClass1Isolation},
		"2.1": {"8": RuleCorrosiveFlamGas}, // same deck -> conflict
		"3":   {},
		"4.1": {},
		"5.1": {}, // bay difference is 2 here -> compatible
		"8":   {},
	}

	for i, c1 := range classes {
		for j := i; j < len(classes); j++ {
			c2 := classes[j]
			t.Run(fmt.Sprintf("%s_vs_%s", c1, c2), func(t *testing.T) {
				checked, conflicts := Adjudicate([]Item{
					item("AAA", c1, 10, "U"),
					item("BBB", c2, 12, "U"),
				})
				if checked != 1 {
					t.Fatalf("checked pairs = %d, want 1", checked)
				}
				rule := ""
				if len(conflicts) > 0 {
					rule = conflicts[0].Rule
				}
				expected := ""
				if row, ok := want[c1]; ok {
					expected = row[c2]
				}
				if expected == "" {
					if row, ok := want[c2]; ok {
						expected = row[c1]
					}
				}
				if rule != expected {
					t.Fatalf("pair %s/%s: rule = %q, want %q (conflicts: %+v)", c1, c2, rule, expected, conflicts)
				}
			})
		}
	}
}

// TestOxidizerBaySeparation walks the bay-difference boundary for
// class 5.1 against classes 3 and 4.1: conflict below 2, compatible from 2.
func TestOxidizerBaySeparation(t *testing.T) {
	for _, other := range []string{"3", "4.1"} {
		for bayDiff := 0; bayDiff <= 3; bayDiff++ {
			name := fmt.Sprintf("5.1_vs_%s_diff_%d", other, bayDiff)
			t.Run(name, func(t *testing.T) {
				_, conflicts := Adjudicate([]Item{
					item("OXI", "5.1", 10, "U"),
					item("OTHER", other, 10+bayDiff, "L"),
				})
				conflict := len(conflicts) == 1
				wantConflict := bayDiff < 2
				if conflict != wantConflict {
					t.Fatalf("bay diff %d: conflict = %v, want %v", bayDiff, conflict, wantConflict)
				}
				if conflict && conflicts[0].Rule != RuleOxidizerBaySep {
					t.Fatalf("rule = %q, want %q", conflicts[0].Rule, RuleOxidizerBaySep)
				}
			})
		}
	}
}

// TestCorrosiveFlammableGas covers the four deck/bay combinations for a
// class 8 / class 2.1 pair: only different decks AND bay difference >= 1
// releases the pair.
func TestCorrosiveFlammableGas(t *testing.T) {
	cases := []struct {
		name         string
		deckA, deckB string
		bayA, bayB   int
		wantConflict bool
	}{
		{"same_deck_same_bay", "U", "U", 5, 5, true},
		{"same_deck_different_bay", "U", "U", 5, 9, true},
		{"different_deck_same_bay", "U", "L", 5, 5, true},
		{"different_deck_adjacent_bay", "U", "L", 5, 6, false},
		{"different_deck_distant_bay", "L", "U", 1, 30, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, conflicts := Adjudicate([]Item{
				item("ACID", "8", tc.bayA, tc.deckA),
				item("GAS", "2.1", tc.bayB, tc.deckB),
			})
			conflict := len(conflicts) == 1
			if conflict != tc.wantConflict {
				t.Fatalf("conflict = %v, want %v (conflicts: %+v)", conflict, tc.wantConflict, conflicts)
			}
			if conflict && conflicts[0].Rule != RuleCorrosiveFlamGas {
				t.Fatalf("rule = %q, want %q", conflicts[0].Rule, RuleCorrosiveFlamGas)
			}
		})
	}
}

// TestClass1Isolation checks class 1 against every class, including itself.
func TestClass1Isolation(t *testing.T) {
	for _, other := range []string{"1", "2.1", "3", "4.1", "5.1", "8"} {
		t.Run("1_vs_"+other, func(t *testing.T) {
			_, conflicts := Adjudicate([]Item{
				item("EXP", "1", 1, "U"),
				item("OTHER", other, 30, "L"),
			})
			conflict := len(conflicts) == 1
			// Class 1 conflicts with any OTHER class; two class-1 containers
			// share the same class and are compatible under these rules.
			wantConflict := other != "1"
			if conflict != wantConflict {
				t.Fatalf("conflict = %v, want %v", conflict, wantConflict)
			}
		})
	}
}

// TestDeterministicOrdering verifies that the input order never changes the
// adjudication result and that conflicts are sorted lexicographically with
// ascending cargo IDs inside each pair.
func TestDeterministicOrdering(t *testing.T) {
	manifest := []Item{
		item("DELTA", "1", 4, "U"),    // conflicts with every other class
		item("ALPHA", "5.1", 10, "U"), // conflicts with CHARLIE (diff 1)
		item("CHARLIE", "3", 11, "L"),
		item("BRAVO", "8", 20, "U"), // conflicts with ECHO (same deck)
		item("ECHO", "2.1", 22, "U"),
	}
	_, want := Adjudicate(manifest)

	// Reversed input must yield the exact same conflicts.
	reversed := make([]Item, len(manifest))
	for i, it := range manifest {
		reversed[len(manifest)-1-i] = it
	}
	_, got := Adjudicate(reversed)

	if len(got) != len(want) {
		t.Fatalf("conflict count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("conflict %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Pairs ascending inside, sorted lexicographically across the list.
	for i, c := range got {
		if c.Pair[0] >= c.Pair[1] {
			t.Fatalf("conflict %d pair not ascending: %v", i, c.Pair)
		}
		if i > 0 {
			prev := got[i-1].Pair
			if prev[0] > c.Pair[0] || (prev[0] == c.Pair[0] && prev[1] >= c.Pair[1]) {
				t.Fatalf("conflicts not sorted: %v then %v", prev, c.Pair)
			}
		}
	}
}

// TestCheckedPairsCount verifies the number of unordered pairs n*(n-1)/2.
func TestCheckedPairsCount(t *testing.T) {
	for n := 0; n <= 5; n++ {
		items := make([]Item, n)
		for i := range items {
			items[i] = item(fmt.Sprintf("C%d", i), "3", i+1, "U")
		}
		checked, _ := Adjudicate(items)
		if want := n * (n - 1) / 2; checked != want {
			t.Fatalf("n=%d: checked = %d, want %d", n, checked, want)
		}
	}
}

// TestDiffConflicts checks that a conflict is identified by pair and rule —
// a changed reason alone counts as neither resolved nor introduced — and
// that both result lists are ordered by pair.
func TestDiffConflicts(t *testing.T) {
	before := []Conflict{
		{Pair: [2]string{"A", "B"}, Rule: RuleClass1Isolation, Reason: "original wording"},
		{Pair: [2]string{"A", "C"}, Rule: RuleOxidizerBaySep, Reason: "r2"},
		{Pair: [2]string{"B", "D"}, Rule: RuleCorrosiveFlamGas, Reason: "r3"},
	}
	after := []Conflict{
		{Pair: [2]string{"A", "B"}, Rule: RuleClass1Isolation, Reason: "moved, but same pair and rule"},
		{Pair: [2]string{"B", "C"}, Rule: RuleOxidizerBaySep, Reason: "r4"},
		{Pair: [2]string{"C", "D"}, Rule: RuleCorrosiveFlamGas, Reason: "r5"},
	}
	resolved, introduced := DiffConflicts(before, after)

	if len(resolved) != 2 || resolved[0].Pair != [2]string{"A", "C"} || resolved[1].Pair != [2]string{"B", "D"} {
		t.Fatalf("resolved = %+v, want pairs [A C] and [B D]", resolved)
	}
	if len(introduced) != 2 || introduced[0].Pair != [2]string{"B", "C"} || introduced[1].Pair != [2]string{"C", "D"} {
		t.Fatalf("introduced = %+v, want pairs [B C] and [C D]", introduced)
	}

	// Empty adjudications diff to empty (non-nil) lists.
	resolved, introduced = DiffConflicts(nil, nil)
	if len(resolved) != 0 || len(introduced) != 0 {
		t.Fatalf("empty diff = %+v / %+v, want both empty", resolved, introduced)
	}
	if resolved == nil || introduced == nil {
		t.Fatal("empty diff must stay non-nil so it marshals as []")
	}
}
