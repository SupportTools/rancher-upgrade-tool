package main

import (
	"encoding/json"
	"log"
	"sort"
	"strings"

	"io/ioutil"
	"os"

	"github.com/gofiber/fiber/v2"
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

// UpgradeStep represents a single upgrade step with from/to versions
type UpgradeStep struct {
	Type     string `json:"type"`     // Rancher or Kubernetes
	Platform string `json:"platform"` // RKE1, RKE2, etc. (for Kubernetes)
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

// GetAllowedK8sUpgrades determines valid Kubernetes versions for the given platform
func GetAllowedK8sUpgrades(currentK8s, platform string, r1, r2 RancherManagerVersion) []UpgradeStep {
	var upgrades []UpgradeStep
	seen := map[string]bool{} // Track seen versions to avoid duplicates

	for _, p1 := range r1.SupportedPlatforms {
		for _, p2 := range r2.SupportedPlatforms {
			if p1.Platform == platform && p2.Platform == platform {
				minVersion := max(p1.MinVersion, p2.MinVersion)
				maxVersion := min(p1.MaxVersion, p2.MaxVersion)

				if minVersion <= maxVersion && minVersion >= currentK8s && !seen[minVersion] {
					upgrades = append(upgrades, UpgradeStep{
						Type:     "Kubernetes",
						Platform: platform,
						From:     currentK8s,
						To:       minVersion,
					})
					seen[minVersion] = true
					currentK8s = minVersion // Update current version
				}

				if GetMajorVersion(minVersion) != GetMajorVersion(maxVersion) && !seen[maxVersion] {
					upgrades = append(upgrades, UpgradeStep{
						Type:     "Kubernetes",
						Platform: platform,
						From:     currentK8s,
						To:       maxVersion,
					})
					seen[maxVersion] = true
				}
			}
		}
	}
	return upgrades
}

// GetMajorVersion extracts the major version from a Kubernetes version (e.g., v1.18.3 -> v1.18)
func GetMajorVersion(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return version
}

// PlanUpgrade generates the optimized Rancher + Kubernetes upgrade plan for a platform
func PlanUpgrade(currentRancher, currentK8s, platform string, versions []string, paths UpgradePaths) ([]UpgradeStep, error) {
	var upgradeSteps []UpgradeStep
	keyVersions := GetKeyVersions(versions)

	for _, version := range keyVersions {
		if version > currentRancher {
			// Add Rancher upgrade step
			upgradeSteps = append(upgradeSteps, UpgradeStep{
				Type: "Rancher",
				From: currentRancher,
				To:   version,
			})

			// Get Kubernetes upgrades between Rancher versions
			r1 := paths.RancherManager[currentRancher]
			r2 := paths.RancherManager[version]
			k8sUpgrades := GetAllowedK8sUpgrades(currentK8s, platform, r1, r2)

			// Append Kubernetes upgrades
			upgradeSteps = append(upgradeSteps, k8sUpgrades...)

			// Update current versions
			if len(k8sUpgrades) > 0 {
				currentK8s = k8sUpgrades[len(k8sUpgrades)-1].To
			}
			currentRancher = version
		}
	}
	return upgradeSteps, nil
}

// GetKeyVersions returns key Rancher versions for the upgrade plan
func GetKeyVersions(versions []string) []string {
	var keyVersions []string
	for _, version := range versions {
		if strings.HasSuffix(version, ".9") || version == "2.7.5" || version == "2.8.8" || version == "2.9.2" {
			keyVersions = append(keyVersions, version)
		}
	}
	sort.Strings(keyVersions)
	return keyVersions
}

// Helper functions for version comparison
func max(v1, v2 string) string {
	if v1 > v2 {
		return v1
	}
	return v2
}

func min(v1, v2 string) string {
	if v1 < v2 {
		return v1
	}
	return v2
}

func main() {
	app := fiber.New()
	upgradePaths := LoadUpgradePaths()

	app.Static("/", "./static")

	// API route to plan the full upgrade path for Rancher and Kubernetes
	app.Get("/api/plan-upgrade/:platform/:rancher/:k8s", func(c *fiber.Ctx) error {
		platform := c.Params("platform")
		currentRancher := c.Params("rancher")
		currentK8s := c.Params("k8s")

		var versions []string
		for v := range upgradePaths.RancherManager {
			versions = append(versions, v)
		}
		sort.Strings(versions)

		upgradePath, err := PlanUpgrade(currentRancher, currentK8s, platform, versions, upgradePaths)
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
