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

const randomStringChars = "abcdefghijklmnopqrstuvwxyz0123456789"
const numConcurrentTests = 10 // Number of concurrent tests
const initialCheckDomain = "google.com"
const defaultTimeout = 3 * time.Second // Global timeout

// generateRandomString generates a random string of the specified length.
func generateRandomString(length int) string {
	rand.Seed(time.Now().UnixNano())
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = randomStringChars[rand.Intn(len(randomStringChars))]
	}
	return string(result)
}

// performDNSLookup performs a DNS lookup for a given resolver and returns the response times in milliseconds.
func performDNSLookup(resolver, domain string, resultsChan chan<- map[string][]time.Duration) {
	var durations []time.Duration
	r := &net.Resolver{
		// Set the server to the target resolver.
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout:   defaultTimeout,
				KeepAlive: -1,
			}
			return d.DialContext(ctx, network, resolver+":53")
		},
		PreferGo: true,
	}

	ctx := context.Background()

	for i := 0; i < 10; i++ {
		start := time.Now()
		_, err := r.LookupHost(ctx, domain)
		if err != nil {
			// Log the error and send nil to resultsChan
			log.Printf("Error during DNS lookup for %s: %v\n", resolver, err)
			resultsChan <- nil
			return
		}
		duration := time.Since(start)
		durations = append(durations, duration)
		time.Sleep(100 * time.Millisecond)
	}
	resultsChan <- map[string][]time.Duration{resolver: durations}
}

// calculateStats calculates the best, worst, and mean durations.
func calculateStats(durations []time.Duration) (time.Duration, time.Duration, time.Duration) {
	if len(durations) == 0 {
		return 0, 0, 0
	}

	best := durations[0]
	worst := durations[0]
	total := time.Duration(0)

	for _, duration := range durations {
		if duration < best {
			best = duration
		}
		if duration > worst {
			worst = duration
		}
		total += duration
	}
	mean := total / time.Duration(len(durations))
	return best, worst, mean
}

// writeMeasurementsReport writes the measurements to a file.
func writeMeasurementsReport(measurements map[string]map[string]time.Duration, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	for resolver, stats := range measurements {
		_, err := writer.WriteString(fmt.Sprintf("%s : %d ms : %d ms : %d ms\n",
			resolver,
			stats["best"].Milliseconds(),
			stats["worst"].Milliseconds(),
			stats["mean"].Milliseconds(),
		))
		if err != nil {
			return err
		}
	}
	return nil
}

// getFastestResolvers returns the top 10% fastest resolvers.
func getFastestResolvers(measurements map[string]map[string]time.Duration) []string {
	type resolverData struct {
		resolver string
		mean     time.Duration
	}
	var resolversData []resolverData
	for resolver, stats := range measurements {
		resolversData = append(resolversData, resolverData{resolver: resolver, mean: stats["mean"]})
	}

	// Sort by mean duration
	sort.Slice(resolversData, func(i, j int) bool {
		return resolversData[i].mean < resolversData[j].mean
	})

	// Calculate 10% threshold
	topCount := int(float64(len(resolversData)) * 0.1)
	if topCount == 0 {
		return []string{} // Return empty if no resolvers or less than 10
	}
	fastestResolvers := make([]string, 0, topCount) // Pre-allocate for efficiency.
	for i := 0; i < topCount; i++ {
		fastestResolvers = append(fastestResolvers, resolversData[i].resolver)
	}
	return fastestResolvers
}

// writeFastestResolvers writes the fastest resolvers to a file.
func writeFastestResolvers(resolvers []string, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	for _, resolver := range resolvers {
		_, err := writer.WriteString(resolver + "\n")
		if err != nil {
			return err
		}
	}
	return nil
}

// writeCleanedResolvers writes the resolvers that did not timeout to a file.
func writeCleanedResolvers(resolvers []string, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	for _, resolver := range resolvers {
		_, err := writer.WriteString(resolver + "\n")
		if err != nil {
			return err
		}
	}
	return nil
}

// checkResolver checks if a resolver works with a given domain.
func checkResolver(resolver, domain string) bool {
	r := &net.Resolver{
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout:   defaultTimeout, // Use the global timeout
				KeepAlive: -1,
			}
			return d.DialContext(ctx, network, resolver+":53")
		},
		PreferGo: true,
	}
	ctx := context.Background()
	_, err := r.LookupHost(ctx, domain)
	return err == nil
}

func main() {
	// Check if resolvers.txt exists
	if _, err := os.Stat("resolvers.txt"); os.IsNotExist(err) {
		log.Fatal("resolvers.txt does not exist. Please run the previous step to generate it.")
		return // Exit
	}

	// Read resolvers from resolvers.txt
	file, err := os.Open("resolvers.txt")
	if err != nil {
		log.Fatalf("Error opening resolvers.txt: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	resolvers := make([]string, 0)
	for scanner.Scan() {
		resolver := strings.TrimSpace(scanner.Text())
		if resolver != "" {
			resolvers = append(resolvers, resolver)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("Error reading resolvers.txt: %v", err)
	}

	// Filter out non-working resolvers
	workingResolvers := make([]string, 0)
	checkedResolvers := make(map[string]bool) // Keep track of checked resolvers
	for _, resolver := range resolvers {
		if _, checked := checkedResolvers[resolver]; !checked { // only check if not checked
			if checkResolver(resolver, initialCheckDomain) {
				workingResolvers = append(workingResolvers, resolver)
				fmt.Printf("Resolver %s passed initial check.\n", resolver)
			} else {
				log.Printf("Resolver %s failed initial check and will be excluded.\n", resolver)
			}
			checkedResolvers[resolver] = true // Mark resolver as checked
		} else {
			workingResolvers = append(workingResolvers, resolver)
			fmt.Printf("Resolver %s was already checked and passed.\n", resolver)
		}
	}

	if len(workingResolvers) == 0 {
		log.Fatal("No resolvers passed the initial check. Exiting.")
		return
	}

	measurements := make(map[string]map[string]time.Duration)
	domain := generateRandomString(12) + ".resolvertest.pingback.me"

	cleanedResolvers := make([]string, 0) // To store resolvers that don't timeout

	// Use a buffered channel to limit the number of concurrent goroutines.
	resultsChan := make(chan map[string][]time.Duration, numConcurrentTests)
	var wg sync.WaitGroup

	// Launch goroutines to perform DNS lookups concurrently.
	for _, resolver := range workingResolvers {
		wg.Add(1)
		go func(r string) {
			defer wg.Done()
			performDNSLookup(r, domain, resultsChan)
		}(resolver)
	}

	// Collect the results from the goroutines.
	go func() {
		wg.Wait()
		close(resultsChan) // Close the channel after all goroutines have finished.
	}()

	// Process the results from the channel.
	for result := range resultsChan {
		if result == nil {
			log.Println("Skipping a resolver due to timeout or other error.")
			continue
		}
		for resolver, durations := range result {
			cleanedResolvers = append(cleanedResolvers, resolver) // Add to cleaned list
			best, worst, mean := calculateStats(durations)
			measurements[resolver] = map[string]time.Duration{
				"best":  best,
				"worst": worst,
				"mean":  mean,
			}
			fmt.Printf("Measurements for %s: Best: %d ms, Worst: %d ms, Mean: %d ms\n", resolver, best.Milliseconds(), worst.Milliseconds(), mean.Milliseconds())
		}
	}

	// Write measurements report
	if err := writeMeasurementsReport(measurements, "measurements.txt"); err != nil {
		log.Fatalf("Error writing measurements.txt: %v", err)
	}
	log.Println("Measurements report written to measurements.txt")

	// Get and write fastest resolvers
	fastestResolvers := getFastestResolvers(measurements)
	if len(fastestResolvers) > 0 {
		if err := writeFastestResolvers(fastestResolvers, "faster-resolvers.txt"); err != nil {
			log.Fatalf("Error writing faster-resolvers.txt: %v", err)
		}
		log.Println("Fastest resolvers written to faster-resolvers.txt")
	} else {
		log.Println("No fastest resolvers found (less than 10 resolvers)")
	}

	// Write cleaned resolvers
	if err := writeCleanedResolvers(cleanedResolvers, "resolvers.cleaned"); err != nil {
		log.Fatalf("Error writing resolvers.cleaned: %v", err)
	}
	log.Println("Cleaned resolvers written to resolvers.cleaned")
}
