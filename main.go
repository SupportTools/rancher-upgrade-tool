package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ansrivas/fiberprometheus/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/hashicorp/go-version"
	"github.com/prometheus/client_golang/prometheus"
)

// Platform defines the compatibility of Kubernetes versions with a Rancher version
type Platform struct {
	Platform   string `json:"platform"`
	MinVersion string `json:"min_version"`
	MaxVersion string `json:"max_version"`
	Notes      string `json:"notes,omitempty"`
}

// RancherManagerVersion contains supported platforms for each Rancher version
type RancherManagerVersion struct {
	SupportedPlatforms []Platform `json:"supported_platforms"`
}

// UpgradePaths stores all Rancher versions and their compatibility data
type UpgradePaths struct {
	RancherManager map[string]RancherManagerVersion `json:"rancher_manager"`
}

// UpgradeStep represents a single upgrade step
type UpgradeStep struct {
	Type     string `json:"type"`     // Rancher or Kubernetes
	Platform string `json:"platform"` // RKE1, RKE2, etc.
	From     string `json:"from"`     // Previous version
	To       string `json:"to"`       // New version
}

// Custom metrics
var (
	totalRequestsLast60Seconds prometheus.Gauge
	versionsSubmitted          *prometheus.CounterVec
	requestDuration            prometheus.Histogram
	activeRequests             prometheus.Gauge

	// For tracking request timestamps
	requestTimestamps []time.Time
	mu                sync.Mutex
)

// Initialize custom metrics
func initMetrics() {
	totalRequestsLast60Seconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "requests_in_last_60_seconds",
		Help: "Number of requests in the last 60 seconds",
	})

	versionsSubmitted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "versions_submitted_total",
			Help: "Total number of versions submitted",
		},
		[]string{"platform", "rancher_version", "k8s_version"},
	)

	requestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "request_duration_seconds",
		Help:    "Histogram of response latency (seconds) of requests.",
		Buckets: prometheus.DefBuckets,
	})

	activeRequests = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "active_requests",
		Help: "Current number of active requests.",
	})

	// Register custom metrics with Prometheus
	prometheus.MustRegister(
		totalRequestsLast60Seconds,
		versionsSubmitted,
		requestDuration,
		activeRequests,
	)
}

// LoadUpgradePaths loads the upgrade paths from the JSON file
func LoadUpgradePaths() (UpgradePaths, error) {
	file, err := os.Open("./data/upgrade-paths.json")
	if err != nil {
		return UpgradePaths{}, fmt.Errorf("failed to load upgrade paths: %v", err)
	}
	defer file.Close()

	bytes, err := io.ReadAll(file)
	if err != nil {
		return UpgradePaths{}, fmt.Errorf("failed to read upgrade paths file: %v", err)
	}

	var paths UpgradePaths
	err = json.Unmarshal(bytes, &paths)
	if err != nil {
		return UpgradePaths{}, fmt.Errorf("failed to parse upgrade paths JSON: %v", err)
	}
	return paths, nil
}

// PlanUpgrade generates the Rancher + Kubernetes upgrade plan
func PlanUpgrade(currentRancher, currentK8s, platform string, versions []string, paths UpgradePaths) ([]UpgradeStep, error) {
	var upgradeSteps []UpgradeStep
	keyVersions := GetKeyVersions(versions)

	// Normalize platform name to lowercase for consistent comparison
	platformLower := strings.ToLower(platform)

	currentRancherVersion, err := version.NewVersion(currentRancher)
	if err != nil {
		return nil, fmt.Errorf("invalid current Rancher version: %v", err)
	}

	for _, v := range keyVersions {
		nextVersion, err := version.NewVersion(v)
		if err != nil {
			return nil, fmt.Errorf("invalid version in key versions: %v", err)
		}

		if nextVersion.GreaterThan(currentRancherVersion) {
			// Add Rancher upgrade step
			upgradeSteps = append(upgradeSteps, UpgradeStep{
				Type: "Rancher", From: currentRancher, To: v,
			})

			// Get Kubernetes upgrades for this Rancher version
			r1 := paths.RancherManager[currentRancher]
			r2 := paths.RancherManager[v]
			k8sUpgrades := GetAllowedK8sUpgrades(currentK8s, platformLower, r1, r2)

			// Add Kubernetes upgrade steps
			for _, upgrade := range k8sUpgrades {
				upgradeSteps = append(upgradeSteps, upgrade)
				currentK8s = upgrade.To // Update current Kubernetes version
			}

			currentRancher = v                  // Update current Rancher version
			currentRancherVersion = nextVersion // Update current Rancher version object
		}
	}

	return upgradeSteps, nil
}

// GetAllowedK8sUpgrades determines the Kubernetes upgrade path based on platform rules
func GetAllowedK8sUpgrades(currentK8s, platform string, r1, r2 RancherManagerVersion) []UpgradeStep {
	var upgrades []UpgradeStep
	k8sVersions := getSortedK8sVersions(platform, r1, r2)

	currentVer, err := parseK8sVersion(currentK8s)
	if err != nil {
		return upgrades
	}

	// Ensure current version is in the list
	if !versionInList(currentVer, k8sVersions) {
		k8sVersions = append(k8sVersions, currentVer)
		sort.Sort(version.Collection(k8sVersions))
	}

	// Decide whether to allow skipping minor versions based on platform
	allowSkip := platform == "rke1" || platform == "rke2" || platform == "k3s"

	for {
		nextVer := findNextAcceptableK8sVersion(currentVer, k8sVersions, allowSkip)
		if nextVer == nil {
			break
		}

		upgrades = append(upgrades, UpgradeStep{
			Type:     "Kubernetes",
			Platform: platform,
			From:     "v" + currentVer.Original(),
			To:       "v" + nextVer.Original(),
		})
		currentVer = nextVer
	}

	return upgrades
}

// findNextAcceptableK8sVersion finds the next acceptable Kubernetes version
func findNextAcceptableK8sVersion(currentVer *version.Version, k8sVersions []*version.Version, allowSkip bool) *version.Version {
	currentSegments := currentVer.Segments()
	if len(currentSegments) < 2 {
		return nil
	}
	currentMinor := currentSegments[1]
	maxAllowedMinor := currentMinor + 1
	if allowSkip {
		maxAllowedMinor = currentMinor + 2
	}

	var candidate *version.Version
	for _, v := range k8sVersions {
		if v.LessThanOrEqual(currentVer) {
			continue
		}
		nextSegments := v.Segments()
		if len(nextSegments) < 2 {
			continue
		}
		nextMinor := nextSegments[1]
		if nextMinor > maxAllowedMinor {
			break // No further versions are acceptable
		}
		candidate = v // Update candidate to the current acceptable version

		if !allowSkip {
			// For platforms that do not allow skipping, return the first acceptable version immediately
			break
		}
	}
	return candidate
}

// Checks if a version is in the list
func versionInList(ver *version.Version, list []*version.Version) bool {
	for _, v := range list {
		if v.Equal(ver) {
			return true
		}
	}
	return false
}

// getSortedK8sVersions retrieves and sorts the Kubernetes versions for the given platform
func getSortedK8sVersions(platform string, r1, r2 RancherManagerVersion) []*version.Version {
	versionSet := make(map[string]*version.Version)
	platforms := append(r1.SupportedPlatforms, r2.SupportedPlatforms...)
	platformLower := strings.ToLower(platform)

	for _, p := range platforms {
		pPlatformLower := strings.ToLower(p.Platform)
		if pPlatformLower == platformLower {
			minVerStr := cleanVersion(p.MinVersion)
			maxVerStr := cleanVersion(p.MaxVersion)
			minVer, err := version.NewVersion(minVerStr)
			if err != nil {
				continue
			}
			maxVer, err := version.NewVersion(maxVerStr)
			if err != nil {
				continue
			}
			// Generate all minor versions between minVer and maxVer
			versionsBetween := getMinorVersionsBetween(minVer, maxVer, p)
			for _, v := range versionsBetween {
				versionSet[v.Original()] = v
			}
		}
	}

	// Convert map to slice
	var versionList []*version.Version
	for _, v := range versionSet {
		versionList = append(versionList, v)
	}

	// Sort the versions
	sort.Sort(version.Collection(versionList))

	return versionList
}

// getMinorVersionsBetween returns all minor versions between min and max versions, including exact versions from data
func getMinorVersionsBetween(minVer, maxVer *version.Version, platformData Platform) []*version.Version {
	var versions []*version.Version

	// Include exact min and max versions with their metadata
	minVerWithMeta, err := version.NewVersion(cleanVersion(platformData.MinVersion))
	if err == nil {
		versions = append(versions, minVerWithMeta)
	}

	maxVerWithMeta, err := version.NewVersion(cleanVersion(platformData.MaxVersion))
	if err == nil && !maxVerWithMeta.Equal(minVerWithMeta) {
		versions = append(versions, maxVerWithMeta)
	}

	// Generate intermediate minor versions
	currentVer := minVer
	for {
		// Increment minor version
		segments := currentVer.Segments()
		if len(segments) < 2 {
			break
		}
		major := segments[0]
		minor := segments[1]
		newMinor := minor + 1
		newVerStr := fmt.Sprintf("%d.%d.0", major, newMinor)
		newVer, err := version.NewVersion(newVerStr)
		if err != nil {
			break
		}
		if newVer.GreaterThan(maxVer) {
			break
		}
		versions = append(versions, newVer)
		currentVer = newVer
	}

	return versions
}

// cleanVersion removes the "v" prefix from a version string
func cleanVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	return v
}

// parseK8sVersion parses a Kubernetes version string
func parseK8sVersion(v string) (*version.Version, error) {
	cleaned := cleanVersion(v)
	ver, err := version.NewVersion(cleaned)
	if err != nil {
		log.Printf("Error parsing Kubernetes version '%s': %v", v, err)
		return nil, err
	}
	return ver, nil
}

// GetKeyVersions returns the key Rancher versions for the upgrade plan
func GetKeyVersions(versions []string) []string {
	var keyVersions []*version.Version
	for _, v := range versions {
		if strings.HasSuffix(v, ".9") || v == "2.7.5" || v == "2.8.8" || v == "2.9.2" {
			ver, err := version.NewVersion(v)
			if err != nil {
				continue
			}
			keyVersions = append(keyVersions, ver)
		}
	}

	// Sort the versions
	sort.Sort(version.Collection(keyVersions))

	// Convert back to string slices
	sortedKeyVersions := make([]string, len(keyVersions))
	for i, v := range keyVersions {
		sortedKeyVersions[i] = v.String()
	}

	return sortedKeyVersions
}

// Main application entry point
func main() {
	// Initialize custom metrics
	initMetrics()

	// Main application Fiber instance
	app := fiber.New()

	// Add the logger middleware
	app.Use(logger.New(logger.Config{
		Format:     "[${time}] ${ip} ${status} - ${latency} ${method} ${path}\n",
		TimeFormat: "2006-01-02 15:04:05",
		TimeZone:   "Local",
	}))

	// Load upgrade paths
	upgradePaths, err := LoadUpgradePaths()
	if err != nil {
		log.Fatalf("Error loading upgrade paths: %v", err)
	}

	app.Static("/", "./static")

	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// API route to generate the upgrade plan
	app.Get("/api/plan-upgrade/:platform/:rancher/:k8s", func(c *fiber.Ctx) error {
		// Start timer
		timer := prometheus.NewTimer(requestDuration)
		defer timer.ObserveDuration()

		// Increment active requests gauge
		activeRequests.Inc()
		defer activeRequests.Dec()

		// Handle request timestamps for sliding window
		updateRequestTimestamps()

		platform := c.Params("platform")
		currentRancher := c.Params("rancher")
		currentK8s := c.Params("k8s")

		// Increment versions submitted counter
		versionsSubmitted.WithLabelValues(platform, currentRancher, currentK8s).Inc()

		var versions []string
		for v := range upgradePaths.RancherManager {
			versions = append(versions, v)
		}

		// Sort versions using semantic versioning
		parsedVersions := make([]*version.Version, 0, len(versions))
		for _, v := range versions {
			ver, err := version.NewVersion(v)
			if err != nil {
				continue
			}
			parsedVersions = append(parsedVersions, ver)
		}
		sort.Sort(version.Collection(parsedVersions))

		// Convert back to string slices
		sortedKeyVersions := make([]string, len(parsedVersions))
		for i, v := range parsedVersions {
			sortedKeyVersions[i] = v.String()
		}

		upgradePath, err := PlanUpgrade(currentRancher, currentK8s, platform, sortedKeyVersions, upgradePaths)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"upgrade_path": upgradePath,
		})
	})

	// Start the metrics server on port 9000
	go startMetricsServer()

	// Start the main application on port 3000
	log.Fatal(app.Listen(":3000"))
}

// updateRequestTimestamps handles the sliding window of request timestamps
func updateRequestTimestamps() {
	mu.Lock()
	defer mu.Unlock()

	now := time.Now()
	requestTimestamps = append(requestTimestamps, now)

	// Remove timestamps older than 60 seconds
	cutoff := now.Add(-60 * time.Second)
	idx := 0
	for i, t := range requestTimestamps {
		if t.After(cutoff) {
			idx = i
			break
		}
	}
	requestTimestamps = requestTimestamps[idx:]

	// Update the gauge
	totalRequestsLast60Seconds.Set(float64(len(requestTimestamps)))
}

// startMetricsServer starts a separate Fiber app to serve metrics on port 9000
func startMetricsServer() {
	metricsApp := fiber.New()

	// Set up Prometheus middleware
	prometheusMiddleware := fiberprometheus.New("fiber_app")
	prometheusMiddleware.RegisterAt(metricsApp, "/metrics")
	metricsApp.Use(prometheusMiddleware.Middleware)

	// Expose /metrics endpoint
	metricsApp.Get("/metrics", func(c *fiber.Ctx) error {
		// The Prometheus middleware handles this
		return nil
	})

	// Start the metrics server
	if err := metricsApp.Listen(":9000"); err != nil {
		log.Fatalf("Failed to start metrics server: %v", err)
	}
}
