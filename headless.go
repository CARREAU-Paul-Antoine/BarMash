package main

import (
	"fmt"
	"os"
	"runtime/pprof"
	"strconv"
)

// Mode sans interface pour les mesures de bout en bout (Hyperfine) et les profils réels.
// Le fichier ne dépend que de initSimulation / updateSimulation / getSimulationStats :
// il compile à l'identique sur la version naïve, ce qui permet de comparer les deux binaires.
//
//	BARMASH_TICKS=5000 BARMASH_CPUPROFILE=cpu.pprof ./barmash
func init() {
	n, err := strconv.Atoi(os.Getenv("BARMASH_TICKS"))
	if err != nil || n <= 0 {
		return
	}

	var cpuFile *os.File
	if path := os.Getenv("BARMASH_CPUPROFILE"); path != "" {
		if cpuFile, err = os.Create(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		pprof.StartCPUProfile(cpuFile)
	}

	initSimulation(MasterSeed)
	for i := 0; i < n; i++ {
		updateSimulation()
	}

	if cpuFile != nil {
		pprof.StopCPUProfile()
		cpuFile.Close()
	}
	if path := os.Getenv("BARMASH_MEMPROFILE"); path != "" {
		if f, err := os.Create(path); err == nil {
			pprof.Lookup("allocs").WriteTo(f, 0)
			f.Close()
		}
	}

	s := getSimulationStats(0)
	fmt.Printf("ticks=%d sobres=%d buveurs=%d vomisseurs=%d flaques=%d bieres=%d gc=%d alloc_total_mb=%.2f\n",
		s.Tick, s.SoberCount, s.DrinkingCount, s.VomitingCount, s.ActivePuddles, s.BeersServed, s.NumGC, s.TotalAllocMB)
	os.Exit(0)
}
