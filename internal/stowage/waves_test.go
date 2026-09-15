package stowage

import (
	"reflect"
	"testing"
)

// TestLoadingWavesPartition pins the exact wave partition of a five-container
// manifest with six conflicts, exercising the selection criteria — most
// distinct occupied adjacent waves first, then total conflict degree
// descending, then cargo ID ascending — and the lowest-free-wave placement.
//
// The conflict graph: DELTA (class 1) conflicts with all four others;
// ALPHA/CHARLIE violate the oxidizer separation; BRAVO/ECHO share the upper
// deck. Degrees: DELTA 4, the rest 2. The trace is:
//
//	DELTA  -> wave 1 (highest degree)
//	ALPHA  -> wave 2 (saturation 1, degree 2, lowest cargo ID)
//	CHARLIE-> wave 3 (saturation 2: waves 1 and 2 occupied)
//	BRAVO  -> wave 2 (saturation 1, degree 2; compatible with ALPHA)
//	ECHO   -> wave 3 (saturation 2; compatible with CHARLIE)
func TestLoadingWavesPartition(t *testing.T) {
	manifest := []Item{
		item("DELTA", "1", 4, "U"),
		item("ALPHA", "5.1", 10, "U"),
		item("CHARLIE", "3", 11, "L"),
		item("BRAVO", "8", 20, "U"),
		item("ECHO", "2.1", 22, "U"),
	}
	want := []Wave{
		{Number: 1, CargoIDs: []string{"DELTA"}},
		{Number: 2, CargoIDs: []string{"ALPHA", "BRAVO"}},
		{Number: 3, CargoIDs: []string{"CHARLIE", "ECHO"}},
	}
	if got := LoadingWaves(manifest); !reflect.DeepEqual(got, want) {
		t.Fatalf("waves = %+v, want %+v", got, want)
	}
}

// TestLoadingWavesOrderIndependent checks that the input order never changes
// the wave partition.
func TestLoadingWavesOrderIndependent(t *testing.T) {
	forward := []Item{
		item("DELTA", "1", 4, "U"),
		item("ALPHA", "5.1", 10, "U"),
		item("CHARLIE", "3", 11, "L"),
		item("BRAVO", "8", 20, "U"),
		item("ECHO", "2.1", 22, "U"),
	}
	reversed := make([]Item, len(forward))
	for i, it := range forward {
		reversed[len(forward)-1-i] = it
	}
	got, want := LoadingWaves(reversed), LoadingWaves(forward)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reversed waves = %+v, want %+v", got, want)
	}
}

// TestLoadingWavesConflictFree checks that a manifest without conflicts
// merges into a single wave holding every container, IDs ascending.
func TestLoadingWavesConflictFree(t *testing.T) {
	manifest := []Item{
		item("C3", "2.1", 20, "U"),
		item("C1", "3", 5, "U"),
		item("C2", "4.1", 9, "L"),
	}
	want := []Wave{{Number: 1, CargoIDs: []string{"C1", "C2", "C3"}}}
	if got := LoadingWaves(manifest); !reflect.DeepEqual(got, want) {
		t.Fatalf("waves = %+v, want %+v", got, want)
	}
}

// TestLoadingWavesInvariants checks the fundamental properties over a denser
// manifest: every container appears exactly once, no wave holds a conflicting
// pair, waves are numbered 1..k and each wave's cargo IDs are ascending.
func TestLoadingWavesInvariants(t *testing.T) {
	manifest := []Item{
		item("EXP1", "1", 1, "U"),
		item("EXP2", "1", 2, "L"),
		item("OXI", "5.1", 10, "U"),
		item("FLAM3", "3", 11, "L"),
		item("FLAM41", "4.1", 12, "U"),
		item("ACID", "8", 20, "U"),
		item("GAS", "2.1", 20, "U"),
		item("NEUT", "3", 25, "L"),
	}
	_, conflicts := Adjudicate(manifest)
	if len(conflicts) == 0 {
		t.Fatal("the manifest must have conflicts for this test to be meaningful")
	}
	waves := LoadingWaves(manifest)

	// Every container appears exactly once across the waves.
	seen := map[string]int{}
	inWave := map[string]int{}
	for _, w := range waves {
		for _, id := range w.CargoIDs {
			seen[id]++
			inWave[id] = w.Number
		}
	}
	if len(seen) != len(manifest) {
		t.Fatalf("waves hold %d distinct containers, want %d", len(seen), len(manifest))
	}
	for _, it := range manifest {
		if seen[it.CargoID] != 1 {
			t.Fatalf("container %s appears %d times, want exactly 1", it.CargoID, seen[it.CargoID])
		}
	}

	// No wave holds a conflicting pair.
	for _, c := range conflicts {
		if inWave[c.Pair[0]] == inWave[c.Pair[1]] {
			t.Fatalf("conflicting pair %v both in wave %d", c.Pair, inWave[c.Pair[0]])
		}
	}

	// Waves are numbered 1..k in order, cargo IDs ascending within each wave.
	for i, w := range waves {
		if w.Number != i+1 {
			t.Fatalf("wave %d has number %d, want %d", i, w.Number, i+1)
		}
		for j := 1; j < len(w.CargoIDs); j++ {
			if w.CargoIDs[j-1] >= w.CargoIDs[j] {
				t.Fatalf("wave %d cargo IDs not ascending: %v", w.Number, w.CargoIDs)
			}
		}
	}
}

// TestLoadingWavesSingleContainer checks the minimal manifest: one container,
// no conflicts, a single wave.
func TestLoadingWavesSingleContainer(t *testing.T) {
	got := LoadingWaves([]Item{item("SOLO", "8", 15, "L")})
	want := []Wave{{Number: 1, CargoIDs: []string{"SOLO"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("waves = %+v, want %+v", got, want)
	}
}
