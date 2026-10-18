package main

import "math"

// -----------------------------------------------------------------------------
// O7 : Table de Voronoï Précalculée du Bar le Plus Proche (Compromis Espace-Temps)
//
// Le monde est découpé en 80 x 80 cellules de 25 u. Une cellule est certifiée
// pour le comptoir k si, pour tout autre comptoir j, chaque point de la cellule
// est strictement plus proche de k que de j :
//   |p - cj|² - |p - ck|² = 2 p·(ck - cj) + |cj|² - |ck|²  >  BarLUTMargin
// Cette forme étant linéaire en p, il suffit de la vérifier aux 4 coins.
// La marge de 1 u² impose un écart de distance >= 1/(dj + dk) >= 1,7e-4 u, très
// supérieur aux erreurs d'arrondi (~1e-12) : la comparaison flottante de la
// version naïve désigne forcément le même gagnant. Les cellules traversées par
// une frontière de Voronoï valent barLUTAmbiguous et retombent sur le scan complet.
// -----------------------------------------------------------------------------

const (
	BarLUTCellSize  = 25.0
	BarLUTDim       = int(WorldSize / BarLUTCellSize)
	BarLUTMargin    = 1.0
	barLUTAmbiguous = 255
)

var barLUT [BarLUTDim * BarLUTDim]uint8 // 6,4 Ko : résident en L1

// buildBarLUT certifie chaque cellule (chemin froid, à l'initialisation).
func buildBarLUT() {
	for gy := 0; gy < BarLUTDim; gy++ {
		for gx := 0; gx < BarLUTDim; gx++ {
			x0, y0 := float64(gx)*BarLUTCellSize, float64(gy)*BarLUTCellSize
			corners := [4]Vec2{{x0, y0}, {x0 + BarLUTCellSize, y0}, {x0, y0 + BarLUTCellSize}, {x0 + BarLUTCellSize, y0 + BarLUTCellSize}}
			barLUT[gy*BarLUTDim+gx] = certifyCell(corners)
		}
	}
}

func certifyCell(corners [4]Vec2) uint8 {
	for k := range barCenters {
		ck := barCenters[k]
		dominant := true
		for j := range barCenters {
			if j == k {
				continue
			}
			cj := barCenters[j]
			for _, p := range corners {
				lhs := 2*(p.X*(ck.X-cj.X)+p.Y*(ck.Y-cj.Y)) + (cj.X*cj.X + cj.Y*cj.Y) - (ck.X*ck.X + ck.Y*ck.Y)
				if lhs <= BarLUTMargin {
					dominant = false
					break
				}
			}
			if !dominant {
				break
			}
		}
		if dominant {
			return uint8(k)
		}
	}
	return barLUTAmbiguous
}

// nearestBarScan est la recherche linéaire de référence (8 racines carrées).
func nearestBarScan(x, y float64) (idx int, dist, dirX, dirY float64) {
	dist = math.MaxFloat64
	for bIdx := range barCenters {
		dx := barCenters[bIdx].X - x
		dy := barCenters[bIdx].Y - y
		d := math.Sqrt(dx*dx + dy*dy)
		if d < dist {
			idx, dist, dirX, dirY = bIdx, d, dx, dy
		}
	}
	return idx, dist, dirX, dirY
}

// nearestBar renvoie le comptoir le plus proche, sa distance et le vecteur client -> comptoir,
// bit à bit identiques à nearestBarScan, avec une seule racine carrée hors frontières.
func nearestBar(x, y float64) (idx int, dist, dirX, dirY float64) {
	gx, gy := int(x*(1.0/BarLUTCellSize)), int(y*(1.0/BarLUTCellSize))
	if x < 0 || y < 0 || gx >= BarLUTDim || gy >= BarLUTDim {
		return nearestBarScan(x, y)
	}
	k := barLUT[gy*BarLUTDim+gx]
	if k == barLUTAmbiguous {
		return nearestBarScan(x, y)
	}
	dx := barCenters[k].X - x
	dy := barCenters[k].Y - y
	return int(k), math.Sqrt(dx*dx + dy*dy), dx, dy
}
