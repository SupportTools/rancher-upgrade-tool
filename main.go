package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"io/ioutil"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/hashicorp/go-version"
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

// LoadUpgradePaths loads the upgrade path data from JSON
func LoadUpgradePaths() UpgradePaths {
	file, err := os.Open("./data/upgrade-paths.json")
	if err != nil {
		log.Fatalf("Failed to load upgrade paths: %v", err)
	}
	defer file.Close()

	bytes, _ := ioutil.ReadAll(file)
	var paths UpgradePaths
	json.Unmarshal(bytes, &paths)
	return paths
}

// PlanUpgrade generates the Rancher + Kubernetes upgrade plan
func PlanUpgrade(currentRancher, currentK8s, platform string, versions []string, paths UpgradePaths) ([]UpgradeStep, error) {
	var upgradeSteps []UpgradeStep
	keyVersions := GetKeyVersions(versions)

	currentRancherVersion, err := version.NewVersion(currentRancher)
	if err != nil {
		return nil, fmt.Errorf("Invalid current Rancher version: %v", err)
	}

	for _, v := range keyVersions {
		nextVersion, err := version.NewVersion(v)
		if err != nil {
			return nil, fmt.Errorf("Invalid version in key versions: %v", err)
		}

		if nextVersion.GreaterThan(currentRancherVersion) {
			// Add Rancher upgrade step
			upgradeSteps = append(upgradeSteps, UpgradeStep{
				Type: "Rancher", From: currentRancher, To: v,
			})

			// Get Kubernetes upgrades for this Rancher version
			r1 := paths.RancherManager[currentRancher]
			r2 := paths.RancherManager[v]
			k8sUpgrades := GetAllowedK8sUpgradesWithSingleSkip(currentK8s, platform, r1, r2)

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

// GetAllowedK8sUpgradesWithSingleSkip ensures only one minor version skip at a time
func GetAllowedK8sUpgradesWithSingleSkip(currentK8s, platform string, r1, r2 RancherManagerVersion) []UpgradeStep {
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

	for {
		nextVer := findNextAcceptableK8sVersion(currentVer, k8sVersions)
		if nextVer == nil {
			break
		}

		upgrades = append(upgrades, UpgradeStep{
			Type:     "Kubernetes",
			Platform: platform,
			From:     "v" + currentVer.String(),
			To:       "v" + nextVer.String(),
		})
		currentVer = nextVer
	}

	return upgrades
}

// findNextAcceptableK8sVersion finds the furthest acceptable Kubernetes version
func findNextAcceptableK8sVersion(currentVer *version.Version, k8sVersions []*version.Version) *version.Version {
	currentMinor := currentVer.Segments()[1]
	maxAllowedMinor := currentMinor + 2

	var candidate *version.Version
	for _, v := range k8sVersions {
		if v.LessThanOrEqual(currentVer) {
			continue
		}
		nextMinor := v.Segments()[1]
		if nextMinor > maxAllowedMinor {
			break // No further versions are acceptable
		}
		candidate = v // Keep updating candidate to get the furthest acceptable version
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

// isEligibleUpgrade ensures only one minor version skip at a time
func isEligibleUpgrade(current, next *version.Version) bool {
	currentMinor := current.Segments()[1]
	nextMinor := next.Segments()[1]

	// Allow skipping up to one minor version
	return nextMinor > currentMinor && nextMinor <= currentMinor+2
}

// getSortedK8sVersions retrieves and sorts the K8s versions for the given platform
func getSortedK8sVersions(platform string, r1, r2 RancherManagerVersion) []*version.Version {
	versionSet := make(map[string]*version.Version)
	platforms := append(r1.SupportedPlatforms, r2.SupportedPlatforms...)
	for _, p := range platforms {
		if p.Platform == platform {
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
			// Generate minor versions between minVer and maxVer
			versionsBetween := getMinorVersionsBetween(minVer, maxVer)
			for _, v := range versionsBetween {
				versionSet[v.String()] = v
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

// getMinorVersionsBetween returns all minor versions between min and max versions
func getMinorVersionsBetween(minVer, maxVer *version.Version) []*version.Version {
	var versions []*version.Version
	currentVer := minVer
	for {
		if currentVer.GreaterThan(maxVer) {
			break
		}
		versions = append(versions, currentVer)

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
		currentVer = newVer
	}
	return versions
}

func cleanVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	v = strings.Split(v, "+")[0] // Remove build metadata
	v = strings.Split(v, "-")[0] // Remove pre-release metadata
	return v
}

func parseK8sVersion(v string) (*version.Version, error) {
	cleaned := cleanVersion(v)
	return version.NewVersion(cleaned)
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

func main() {
	app := fiber.New()
	upgradePaths := LoadUpgradePaths()

	app.Static("/", "./static")

	// API route to generate the upgrade plan
	app.Get("/api/plan-upgrade/:platform/:rancher/:k8s", func(c *fiber.Ctx) error {
		platform := c.Params("platform")
		currentRancher := c.Params("rancher")
		currentK8s := c.Params("k8s")

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
		sortedVersions := make([]string, len(parsedVersions))
		for i, v := range parsedVersions {
			sortedVersions[i] = v.String()
		}

		upgradePath, err := PlanUpgrade(currentRancher, currentK8s, platform, sortedVersions, upgradePaths)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": err.Error(),
			})
		}

		return c.JSON(fiber.Map{
			"upgrade_path": upgradePath,
		})
	})

	log.Fatal(app.Listen(":3000"))
}
