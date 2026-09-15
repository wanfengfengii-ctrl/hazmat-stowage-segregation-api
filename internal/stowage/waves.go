package stowage

import "sort"

// Wave is one loading wave: a group of containers that may be loaded into
// the vessel together because no two of them conflict. Number is the 1-based
// wave number and CargoIDs holds the containers of the wave, ascending.
type Wave struct {
	Number   int      `json:"wave"`
	CargoIDs []string `json:"cargo_ids"`
}

// LoadingWaves partitions the manifest into loading waves so that no wave
// holds two conflicting containers. The undirected conflict graph is built
// from the first-match-rule adjudication of Adjudicate. The containers are
// then colored one at a time: at every step the unassigned container with
// the most distinct wave numbers already used by its conflicting neighbors
// is chosen — ties broken by total conflict degree (descending) and then
// cargo ID (ascending) — and placed in the lowest-numbered wave none of its
// conflicting neighbors occupy.
//
// Every container appears in exactly one wave, waves are numbered from 1 and
// reported in order, and the cargo IDs of each wave are ascending, so the
// result is deterministic and independent of the input order. A conflict-free
// manifest yields a single wave.
func LoadingWaves(items []Item) []Wave {
	_, conflicts := Adjudicate(items)

	// Build the undirected conflict graph: cargo ID -> conflicting cargo IDs.
	neighbors := make(map[string]map[string]bool, len(items))
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if _, ok := neighbors[it.CargoID]; !ok {
			neighbors[it.CargoID] = map[string]bool{}
			ids = append(ids, it.CargoID)
		}
	}
	for _, c := range conflicts {
		neighbors[c.Pair[0]][c.Pair[1]] = true
		neighbors[c.Pair[1]][c.Pair[0]] = true
	}
	degree := make(map[string]int, len(ids))
	for id, ns := range neighbors {
		degree[id] = len(ns)
	}

	assigned := make(map[string]int, len(ids)) // cargo ID -> wave number
	unassigned := make(map[string]bool, len(ids))
	for _, id := range ids {
		unassigned[id] = true
	}
	for len(unassigned) > 0 {
		id := mostConstrained(neighbors, degree, assigned, unassigned)
		assigned[id] = firstFreeWave(neighbors[id], assigned)
		delete(unassigned, id)
	}

	byWave := make(map[int][]string)
	maxWave := 0
	for id, w := range assigned {
		byWave[w] = append(byWave[w], id)
		if w > maxWave {
			maxWave = w
		}
	}
	waves := make([]Wave, 0, maxWave)
	for w := 1; w <= maxWave; w++ {
		cargoIDs := byWave[w]
		sort.Strings(cargoIDs)
		waves = append(waves, Wave{Number: w, CargoIDs: cargoIDs})
	}
	return waves
}

// mostConstrained returns the unassigned container with the highest
// saturation — the number of distinct wave numbers already used by its
// conflicting neighbors. Ties are broken by total conflict degree
// (descending) and then by cargo ID (ascending), so the choice is
// deterministic regardless of map iteration order.
func mostConstrained(neighbors map[string]map[string]bool, degree map[string]int, assigned map[string]int, unassigned map[string]bool) string {
	best := ""
	bestSat, bestDeg := -1, -1
	for id := range unassigned {
		sat := 0
		seen := make(map[int]bool, len(neighbors[id]))
		for nid := range neighbors[id] {
			if w, ok := assigned[nid]; ok && !seen[w] {
				seen[w] = true
				sat++
			}
		}
		deg := degree[id]
		if sat > bestSat ||
			(sat == bestSat && deg > bestDeg) ||
			(sat == bestSat && deg == bestDeg && id < best) {
			best, bestSat, bestDeg = id, sat, deg
		}
	}
	return best
}

// firstFreeWave returns the lowest wave number, starting at 1, that none of
// the container's conflicting neighbors occupy.
func firstFreeWave(nbs map[string]bool, assigned map[string]int) int {
	used := make(map[int]bool, len(nbs))
	for nid := range nbs {
		if w, ok := assigned[nid]; ok {
			used[w] = true
		}
	}
	w := 1
	for used[w] {
		w++
	}
	return w
}
