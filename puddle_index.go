package main

import "math"

// -----------------------------------------------------------------------------
// O2 : Index Spatial CSR des Flaques (Compressed Sparse Row)
//
// Le monde est découpé en 32 x 32 cellules de 62,5 u. Une flaque à moins de
// VomitPerturbRadius (50 u) d'un client est forcément dans le voisinage 3 x 3
// de sa cellule ; la marge de 12,5 u absorbe tout arrondi flottant.
//
// L'index est reconstruit en début de tick par tri par comptage (O(P + cellules)) :
//   puddleCellStart[k] .. puddleCellStart[k+1] délimite les indices de la cellule k
//   dans puddleCellItems. Les cellules d'une même ligne étant contiguës, un
//   voisinage 3 x 3 se lit en 3 plages continues au lieu de 9 listes.
//
// Les flaques créées pendant le tick (indices >= puddleIndexed) ne sont pas
// indexées : elles sont parcourues linéairement pour rester visibles des clients
// suivants, exactement comme dans la version naïve.
// -----------------------------------------------------------------------------

const (
	PuddleCellSize  = 62.5
	PuddleGridDim   = int(WorldSize / PuddleCellSize)
	PuddleCellCount = PuddleGridDim * PuddleGridDim
)

var (
	puddleCellStart [PuddleCellCount + 1]int32
	puddleCellFill  [PuddleCellCount]int32
	puddleCellItems = make([]int32, 0, MaxPuddlesCapacity)
	puddleIndexed   int
)

// puddleCell convertit une coordonnée en index de cellule, borné à la grille.
func puddleCell(v float64) int {
	c := int(v * (1.0 / PuddleCellSize))
	if c < 0 {
		return 0
	}
	if c >= PuddleGridDim {
		return PuddleGridDim - 1
	}
	return c
}

// buildPuddleIndex reconstruit l'index CSR sans allocation (tri par comptage).
func buildPuddleIndex() {
	if len(vomitPuddles) == 0 && puddleIndexed == 0 {
		return // Index déjà vide : évite d'effacer 4 Ko à chaque tick sans flaque
	}
	clear(puddleCellStart[:])
	for i := range vomitPuddles {
		p := &vomitPuddles[i]
		puddleCellStart[puddleCell(p.Y)*PuddleGridDim+puddleCell(p.X)+1]++
	}
	for k := 1; k <= PuddleCellCount; k++ {
		puddleCellStart[k] += puddleCellStart[k-1]
	}

	n := len(vomitPuddles)
	if cap(puddleCellItems) < n {
		puddleCellItems = make([]int32, n, 2*n)
	}
	puddleCellItems = puddleCellItems[:n]
	copy(puddleCellFill[:], puddleCellStart[:PuddleCellCount])
	for i := range vomitPuddles {
		p := &vomitPuddles[i]
		k := puddleCell(p.Y)*PuddleGridDim + puddleCell(p.X)
		puddleCellItems[puddleCellFill[k]] = int32(i)
		puddleCellFill[k]++
	}
	puddleIndexed = n
}

// probePuddle applique le test de distance de la version naïve (même expression flottante).
func probePuddle(x, y float64, vp *VomitPuddle) (trapped, smelling bool) {
	dx := x - vp.X
	dy := y - vp.Y
	dist := math.Sqrt(dx*dx + dy*dy)
	if dist <= vp.Radius {
		return true, false
	}
	return false, dist <= VomitPerturbRadius
}

// probePuddles renvoie si le client est piégé dans une flaque ou en sent une à proximité.
// Le résultat est un OU logique sur les flaques, donc indépendant de l'ordre de parcours.
func probePuddles(x, y float64) (trapped, smelling bool) {
	trapped, smelling = probeIndexedPuddles(x, y)
	if trapped {
		return true, smelling
	}
	t, s := probeTailPuddles(x, y)
	return t, smelling || s
}

// probeIndexedPuddles teste les flaques présentes en début de tick (lecture seule : phase 1).
func probeIndexedPuddles(x, y float64) (trapped, smelling bool) {
	cx, cy := puddleCell(x), puddleCell(y)
	x0, x1 := max(cx-1, 0), min(cx+1, PuddleGridDim-1)
	for gy := max(cy-1, 0); gy <= min(cy+1, PuddleGridDim-1); gy++ {
		row := gy * PuddleGridDim
		for k := puddleCellStart[row+x0]; k < puddleCellStart[row+x1+1]; k++ {
			t, s := probePuddle(x, y, &vomitPuddles[puddleCellItems[k]])
			if t {
				return true, smelling
			}
			smelling = smelling || s
		}
	}
	return false, smelling
}

// probeTailPuddles teste les flaques créées pendant le tick courant (phase 2, séquentielle).
func probeTailPuddles(x, y float64) (trapped, smelling bool) {
	for i := puddleIndexed; i < len(vomitPuddles); i++ {
		t, s := probePuddle(x, y, &vomitPuddles[i])
		if t {
			return true, smelling
		}
		smelling = smelling || s
	}
	return false, smelling
}
