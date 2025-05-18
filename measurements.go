package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	randomStringChars  = "abcdefghijklmnopqrstuvwxyz0123456789"
	numConcurrentTests = 500               // High concurrency
	lookupCount        = 3                 // Fewer iterations per resolver
	initialCheckDomain = "google.com"
	defaultTimeout     = 2 * time.Second
)

func generateRandomString(length int) string {
	b := make([]byte, length)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := range b {
		b[i] = randomStringChars[r.Intn(len(randomStringChars))]
	}
	return string(b)
}

func checkWorkingResolvers(resolvers []string, domain string) []string {
	var wg sync.WaitGroup
	var mu sync.Mutex
	working := make([]string, 0, len(resolvers))
	sem := make(chan struct{}, numConcurrentTests)

	for _, resolver := range resolvers {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			dialer := &net.Dialer{Timeout: defaultTimeout}
			res := &net.Resolver{
				PreferGo: true,
				Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, network, r+":53")
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
			defer cancel()
			if _, err := res.LookupHost(ctx, domain); err == nil {
				mu.Lock()
				working = append(working, r)
				mu.Unlock()
			}
		}(resolver)
	}
	wg.Wait()
	return working
}

func performDNSBenchmark(resolver, domain string) ([]time.Duration, bool) {
	dialer := &net.Dialer{Timeout: defaultTimeout}
	res := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, resolver+":53")
		},
	}
	ctx := context.Background()
	var durations []time.Duration
	for i := 0; i < lookupCount; i++ {
		start := time.Now()
		_, err := res.LookupHost(ctx, domain)
		if err != nil {
			return nil, false
		}
		durations = append(durations, time.Since(start))
	}
	return durations, true
}

func calculateStats(durations []time.Duration) (best, worst, mean time.Duration) {
	if len(durations) == 0 {
		return
	}
	best, worst = durations[0], durations[0]
	var total time.Duration
	for _, d := range durations {
		if d < best {
			best = d
		}
		if d > worst {
			worst = d
		}
		total += d
	}
	mean = total / time.Duration(len(durations))
	return
}

func writeFileLines(filename string, lines []string) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, line := range lines {
		_, err := w.WriteString(line + "\n")
		if err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeMeasurements(filename string, data map[string]map[string]time.Duration) error {
	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for resolver, stats := range data {
		_, err := fmt.Fprintf(w, "%s : %d ms : %d ms : %d ms\n",
			resolver,
			stats["best"].Milliseconds(),
			stats["worst"].Milliseconds(),
			stats["mean"].Milliseconds())
		if err != nil {
			return err
		}
	}
	return w.Flush()
}

func getFastestResolvers(measurements map[string]map[string]time.Duration) []string {
	type resStat struct {
		resolver string
		best     time.Duration
		mean     time.Duration
	}
	stats := make([]resStat, 0, len(measurements))
	for res, s := range measurements {
		if s["best"] < 100*time.Millisecond {
			stats = append(stats, resStat{resolver: res, best: s["best"], mean: s["mean"]})
		}
	}

	if len(stats) == 0 {
		log.Println("No resolvers under 100ms. Using fallback list.")
		return []string{"1.1.1.1", "8.8.8.8"}
	}

	sort.Slice(stats, func(i, j int) bool {
		return stats[i].mean < stats[j].mean
	})

	topCount := int(float64(len(stats)) * 0.1)
	if topCount < 1 {
		topCount = len(stats)
	}

	result := make([]string, 0, topCount)
	for i := 0; i < topCount; i++ {
		result = append(result, stats[i].resolver)
	}
	return result
}

func main() {
	// Step 1: Load resolvers
	file, err := os.Open("resolvers.txt")
	if err != nil {
		log.Fatalf("Failed to open resolvers.txt: %v", err)
	}
	defer file.Close()
	var resolvers []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			resolvers = append(resolvers, line)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("Error reading resolvers.txt: %v", err)
	}

	log.Printf("Loaded %d resolvers. Validating...", len(resolvers))

	// Step 2: Filter working resolvers
	workingResolvers := checkWorkingResolvers(resolvers, initialCheckDomain)
	log.Printf("Found %d working resolvers. Starting benchmark...", len(workingResolvers))

	if len(workingResolvers) == 0 {
		log.Fatal("No working resolvers found.")
	}

	// Step 3: Random domain to avoid cache
	randomDomain := generateRandomString(12) + ".resolvertest.pingback.me"
	log.Printf("Using domain: %s", randomDomain)

	// Step 4: Benchmark
	measurements := make(map[string]map[string]time.Duration)
	cleaned := make([]string, 0, len(workingResolvers))

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, numConcurrentTests)

	for _, resolver := range workingResolvers {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			durations, ok := performDNSBenchmark(r, randomDomain)
			if !ok {
				return
			}
			best, worst, mean := calculateStats(durations)

			mu.Lock()
			cleaned = append(cleaned, r)
			measurements[r] = map[string]time.Duration{
				"best":  best,
				"worst": worst,
				"mean":  mean,
			}
			mu.Unlock()

			fmt.Printf("%s -> Best: %dms | Worst: %dms | Mean: %dms\n", r, best.Milliseconds(), worst.Milliseconds(), mean.Milliseconds())
		}(resolver)
	}
	wg.Wait()

	// Step 5: Save results
	if err := writeFileLines("resolvers.cleaned", cleaned); err != nil {
		log.Fatalf("Failed to write resolvers.cleaned: %v", err)
	}
	if err := writeMeasurements("measurements.txt", measurements); err != nil {
		log.Fatalf("Failed to write measurements.txt: %v", err)
	}
	if fastest := getFastestResolvers(measurements); len(fastest) > 0 {
		if err := writeFileLines("faster-resolvers.txt", fastest); err != nil {
			log.Fatalf("Failed to write faster-resolvers.txt: %v", err)
		}
		log.Printf("Wrote %d fastest resolvers.", len(fastest))
	}
	log.Println("Done.")
}
