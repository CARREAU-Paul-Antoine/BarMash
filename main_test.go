package main

import (
	"math/rand"
	"testing"
	"unsafe"
)

// -----------------------------------------------------------------------------
// Tests Unitaires & Analyse Mémoire
// -----------------------------------------------------------------------------

// TestClientStructSize analyse la taille mémoire de la structure Client
// et son adéquation avec les lignes de cache processeur (64 octets standard L1/L2).
func TestClientStructSize(t *testing.T) {
	size := unsafe.Sizeof(Client{})
	t.Logf("Taille de la structure Client : %d octets (Ligne de cache standard = 64 octets)", size)

	if size > 64 {
		t.Logf("⚠️ NOTE ARCHITECTURE : La structure fait %d octets et dépasse une seule ligne de cache (64B).", size)
		t.Logf("   Un accès à un seul client peut déclencher jusqu'à 2 lectures de lignes de cache (Cache Line Splits).")
	}
}

// -----------------------------------------------------------------------------
// Micro-Benchmarks d'Audit de Performance
// -----------------------------------------------------------------------------

// Benchmark1_ContiguousSlice simule la mise à jour d'un tableau contigu en mémoire ([]Client)
// où le Hardware Prefetcher précharge les données de manière optimale.
func Benchmark1_ContiguousSlice(b *testing.B) {
	clientsData := make([]Client, TotalClients)
	for i := 0; i < TotalClients; i++ {
		clientsData[i] = Client{
			ID:        int64(i),
			X:         float64(i % 2000),
			Y:         float64((i * 3) % 2000),
			Speed:     3.0,
			Alcohol:   0.5,
			Stamina:   100.0,
			Tolerance: 2.5,
			State:     StateSober,
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		for i := 0; i < TotalClients; i++ {
			c := &clientsData[i]
			c.X += 0.5
			c.Y += 0.5
			c.Stamina -= 0.1
			if c.Stamina <= 0 {
				c.Stamina = 100.0
			}
		}
	}
}

// Benchmark2_PointersSequential simule la mise à jour séquentielle d'une tranche de pointeurs ([]*Client)
// chaque élément résidant de manière indépendante sur le Tas (Heap).
func Benchmark2_PointersSequential(b *testing.B) {
	ptrs := make([]*Client, TotalClients)
	for i := 0; i < TotalClients; i++ {
		ptrs[i] = &Client{
			ID:        int64(i),
			X:         float64(i % 2000),
			Y:         float64((i * 3) % 2000),
			Speed:     3.0,
			Alcohol:   0.5,
			Stamina:   100.0,
			Tolerance: 2.5,
			State:     StateSober,
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		for i := 0; i < TotalClients; i++ {
			c := ptrs[i]
			c.X += 0.5
			c.Y += 0.5
			c.Stamina -= 0.1
			if c.Stamina <= 0 {
				c.Stamina = 100.0
			}
		}
	}
}

// Benchmark3_PointersScattered simule le pire cas : accès aléatoire à des pointeurs dispersés en RAM
// provoquant des CPU Stalls continus en attendant les données de la mémoire vive (DRAM).
func Benchmark3_PointersScattered(b *testing.B) {
	ptrs := make([]*Client, TotalClients)
	for i := 0; i < TotalClients; i++ {
		ptrs[i] = &Client{
			ID:        int64(i),
			X:         float64(i % 2000),
			Y:         float64((i * 3) % 2000),
			Speed:     3.0,
			Alcohol:   0.5,
			Stamina:   100.0,
			Tolerance: 2.5,
			State:     StateSober,
		}
	}

	// Permutation aléatoire déterministe des indices
	rng := rand.New(rand.NewSource(42))
	indices := rng.Perm(TotalClients)

	b.ResetTimer()
	b.ReportAllocs()

	for n := 0; n < b.N; n++ {
		for i := 0; i < TotalClients; i++ {
			targetIdx := indices[i]
			c := ptrs[targetIdx]
			c.X += 0.5
			c.Y += 0.5
			c.Stamina -= 0.1
			if c.Stamina <= 0 {
				c.Stamina = 100.0
			}
		}
	}
}
