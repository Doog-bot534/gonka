// Load test for ManagedArtifactStore write and read throughput.
//
// Usage (from decentralized-api directory):
//   go run ./managed_store_load.go -mode=write -duration=5s -workers=32
//   go run ./managed_store_load.go -mode=read  -duration=5s -workers=32 -preload=200000
//
// By default it uses a temporary directory which is cleaned up on exit.

// go run ./managed_store_load.go \
//   -mode=write -duration=10s -workers=32 \
//   -cpuprofile=cpu.out
//
// go tool pprof cpu.out
// # inside pprof:
// #   top
// #   top -cum
// #   list artifacts.AddWithNode
// #   list artifacts.flushLocked
// #   list AddWithNode

package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	"decentralized-api/poc/artifacts"
)

type modeType string

const (
	modeWrite modeType = "write"
	modeRead  modeType = "read"
)

func main() {
	var (
		flagMode         = flag.String("mode", "write", "benchmark mode: write or read")
		flagDuration     = flag.Duration("duration", 5*time.Second, "benchmark duration")
		flagWorkers      = flag.Int("workers", runtime.NumCPU()*2, "number of concurrent workers")
		flagVectorSize   = flag.Int("vector-size", 128, "artifact vector size in bytes")
		flagStages       = flag.Int("stages", 1, "number of PoC stages (ManagedArtifactStore stores) to pre-create")
		flagRetain       = flag.Int("retain", 10, "ManagedArtifactStore retain count")
		flagFlushEvery   = flag.Duration("flush-interval", 0, "periodic flush interval (0 to disable)")
		flagBaseDir      = flag.String("dir", "", "base directory for ManagedArtifactStore (empty = temp dir)")
		flagKeepDir      = flag.Bool("keep-dir", false, "do not remove base directory after benchmark")
		flagPreloadCount = flag.Int("preload", 100000, "number of artifacts to preload for read benchmark")
		flagCPUProfile   = flag.String("cpuprofile", "", "write CPU profile to this file")
		flagMemProfile   = flag.String("memprofile", "", "write heap profile to this file")
	)
	flag.Parse()

	if *flagWorkers <= 0 {
		log.Fatalf("workers must be > 0")
	}
	if *flagStages <= 0 {
		log.Fatalf("stages must be > 0")
	}

	mode := modeType(*flagMode)
	if mode != modeWrite && mode != modeRead {
		log.Fatalf("unsupported mode %q (expected write or read)", *flagMode)
	}

	// Optional CPU profiling.
	if *flagCPUProfile != "" {
		f, err := os.Create(*flagCPUProfile)
		if err != nil {
			log.Fatalf("could not create CPU profile: %v", err)
		}
		// Ensure we stop profiling before closing the file so the profile is fully written.
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatalf("could not start CPU profile: %v", err)
		}
		defer func() {
			pprof.StopCPUProfile()
			if err := f.Close(); err != nil {
				log.Printf("error closing CPU profile file: %v", err)
			}
		}()
	}

	// Base directory setup.
	baseDir := *flagBaseDir
	var cleanupBaseDir func()
	if baseDir == "" {
		tmp, err := os.MkdirTemp("", "artifact-bench-")
		if err != nil {
			log.Fatalf("create temp dir: %v", err)
		}
		baseDir = tmp
		if !*flagKeepDir {
			cleanupBaseDir = func() { _ = os.RemoveAll(baseDir) }
		}
	}
	if cleanupBaseDir != nil {
		defer cleanupBaseDir()
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		log.Fatalf("mkdir base dir: %v", err)
	}

	// Use a ManagedArtifactStore so we include its overhead (cleanup, retain, etc.).
	m := artifacts.NewManagedArtifactStore(baseDir, *flagRetain)
	defer func() {
		if err := m.Close(); err != nil {
			log.Printf("ManagedArtifactStore close error: %v", err)
		}

		// Optional heap profile written at the end of the run.
		if *flagMemProfile != "" {
			f, err := os.Create(*flagMemProfile)
			if err != nil {
				log.Printf("could not create memory profile: %v", err)
				return
			}
			defer func() {
				if err := f.Close(); err != nil {
					log.Printf("error closing memory profile file: %v", err)
				}
			}()

			// Force GC to get up-to-date heap data.
			runtime.GC()
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Printf("could not write memory profile: %v", err)
			}
		}
	}()

	if *flagFlushEvery > 0 {
		m.StartPeriodicFlush(*flagFlushEvery)
	}

	fmt.Printf("Base dir: %s\n", baseDir)
	fmt.Printf("Mode: %s, Workers: %d, Duration: %s, Stages: %d, VectorSize: %d\n",
		mode, *flagWorkers, *flagDuration, *flagStages, *flagVectorSize)

	// Pre-create the requested number of stages/stores.
	stores := make([]*artifacts.ArtifactStore, *flagStages)
	for i := 0; i < *flagStages; i++ {
		height := int64((i + 1) * 1000) // arbitrary but distinct stage heights
		s, err := m.GetOrCreateStore(height)
		if err != nil {
			log.Fatalf("GetOrCreateStore(%d): %v", height, err)
		}
		stores[i] = s
	}

	vector := make([]byte, *flagVectorSize)

	switch mode {
	case modeWrite:
		totalOps, elapsed := runWriteBenchmark(stores, vector, *flagWorkers, *flagDuration)
		report("write", totalOps, elapsed)
	case modeRead:
		totalOps, elapsed := runReadBenchmark(stores[0], vector, *flagWorkers, *flagDuration, *flagPreloadCount)
		report("read", totalOps, elapsed)
	default:
		log.Fatalf("unsupported mode %q", mode)
	}
}

func report(label string, totalOps int64, elapsed time.Duration) {
	rate := float64(totalOps) / elapsed.Seconds()
	fmt.Printf("%s: total=%d ops, time=%s, rate=%.0f ops/sec\n", label, totalOps, elapsed, rate)
	fmt.Printf("%s: target 5M: %.2f%% achieved\n", label, (rate/5_000_000)*100)
}

// runWriteBenchmark continuously appends artifacts via AddWithNode.
func runWriteBenchmark(stores []*artifacts.ArtifactStore, vector []byte, workers int, duration time.Duration) (int64, time.Duration) {
	var wg sync.WaitGroup
	done := make(chan struct{})

	perWorker := make([]int64, workers)

	const maxInt32 = int64(1<<31 - 1)
	nonceSpan := maxInt32 / int64(workers) // non-overlapping nonce ranges per worker

	start := time.Now()
	for w := 0; w < workers; w++ {
		w := w
		store := stores[w%len(stores)]
		baseNonce := int32(nonceSpan * int64(w))

		wg.Add(1)
		go func() {
			defer wg.Done()
			var localOps int64
			nonce := baseNonce
			nodeID := fmt.Sprintf("node-%d", w)

			for {
				select {
				case <-done:
					perWorker[w] = localOps
					return
				default:
					if int64(nonce-baseNonce) >= nonceSpan {
						// Very unlikely for typical benchmark durations; stop if we ever reach this.
						perWorker[w] = localOps
						return
					}
					if err := store.AddWithNode(nonce, vector, nodeID); err != nil {
						log.Printf("AddWithNode error (worker %d): %v", w, err)
						perWorker[w] = localOps
						return
					}
					nonce++
					localOps++
				}
			}
		}()
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()

	var total int64
	for _, c := range perWorker {
		total += c
	}
	return total, time.Since(start)
}

// runReadBenchmark preloads the store with artifacts, then repeatedly calls GetArtifact.
func runReadBenchmark(store *artifacts.ArtifactStore, vector []byte, workers int, duration time.Duration, preload int) (int64, time.Duration) {
	if preload <= 0 {
		log.Fatalf("preload must be > 0 for read mode")
	}

	fmt.Printf("Preloading %d artifacts for read benchmark...\n", preload)

	// Preload artifacts sequentially to ensure a dense index range.
	for i := 0; i < preload; i++ {
		nonce := int32(i + 1)
		if err := store.AddWithNode(nonce, vector, "preload"); err != nil {
			log.Fatalf("preload AddWithNode(%d): %v", nonce, err)
		}
	}

	if err := store.Flush(); err != nil {
		log.Fatalf("preload Flush: %v", err)
	}

	count := int(store.Count())
	if count < preload {
		log.Printf("warning: store.Count()=%d < preload=%d", count, preload)
	}
	if count == 0 {
		log.Fatalf("no artifacts after preload")
	}

	fmt.Printf("Preload complete. Stored artifacts: %d\n", count)

	var wg sync.WaitGroup
	done := make(chan struct{})
	perWorker := make([]int64, workers)
	var readMu sync.Mutex // serialize GetArtifact to avoid concurrent Seek/Read on shared file descriptor

	start := time.Now()
	for w := 0; w < workers; w++ {
		w := w
		src := rand.New(rand.NewSource(time.Now().UnixNano() + int64(w)))

		wg.Add(1)
		go func() {
			defer wg.Done()
			var localOps int64

			for {
				select {
				case <-done:
					perWorker[w] = localOps
					return
				default:
					idx := src.Intn(count)

					// NOTE: ArtifactStore.GetArtifact uses a shared *os.File with Seek+Read
					// under an RLock, which is not safe for concurrent callers because
					// the file offset is shared. We serialize calls here so the load
					// test measures correct read behavior instead of racing on the fd.
					readMu.Lock()
					_, _, err := store.GetArtifact(uint32(idx))
					readMu.Unlock()
					if err != nil {
						log.Printf("GetArtifact error (worker %d, idx %d): %v", w, idx, err)
						perWorker[w] = localOps
						return
					}
					localOps++
				}
			}
		}()
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()

	var total int64
	for _, c := range perWorker {
		total += c
	}
	return total, time.Since(start)
}


