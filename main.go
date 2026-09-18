// Command rancher-upgrade-tool serves rancher.tips: given a Rancher version and the
// Kubernetes versions of the cluster Rancher runs on and one cluster it manages, it
// returns every reachable Rancher version with an ordered, sourced route to each.
//
// The planning logic lives in internal/planner and the dataset in internal/catalog.
// This file is wiring: metrics, routes, and the fail-closed startup path.
package main

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/supporttools/rancher-upgrade-tool/internal/api"
	"github.com/supporttools/rancher-upgrade-tool/internal/catalog"
)

// version is the build identity, injected at link time with
//
//	-ldflags "-X main.buildVersion=v221"
//
// It exists so a deploy can be VERIFIED FROM OUTSIDE THE CLUSTER. Every prior
// attempt to verify a deploy leaned on ArgoCD's .status.sync.revision, which for
// an OCI Helm source is a sha256 digest, not a chart version -- and which reports
// the revision that was REQUESTED, not the one that synced. Both properties let a
// deploy of nothing report success: mst sat on image v216 while ArgoCD reported
// revision v221 Healthy and the pipeline went looking for a matching string.
//
// A version the running process reports about itself cannot be faked by the
// control plane's desired state, and needs no cluster credentials to read.
// Named buildVersion, not version: package main already imports
// github.com/hashicorp/go-version as `version`, and a package-level var of that
// name shadows it for every file in the package.
var buildVersion = "dev"

const (
	catalogPath = "./data/catalog.json"

	// appPort serves the UI and the API.
	appPort = ":3000"

	// defaultMetricsPort matches the chart. The app previously bound :9000 while
	// deployment.yaml annotated 9090, declared containerPort 9090 and service.yaml
	// exposed 9090, so Prometheus had been scraping a closed port in all six
	// environments since the chart was written.
	//
	// Overridable via METRICS_PORT, because 9090 is the conventional Prometheus port
	// and a developer running one locally would otherwise collide.
	defaultMetricsPort = ":9090"
)

var (
	totalRequestsLast60Seconds prometheus.Gauge
	versionsSubmitted          *prometheus.CounterVec
	fleetSize                  *prometheus.CounterVec
	requestDuration            prometheus.Histogram
	activeRequests             prometheus.Gauge

	requestTimestamps []time.Time
	mu                sync.Mutex
)

func initMetrics() {
	totalRequestsLast60Seconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "requests_in_last_60_seconds",
		Help: "Number of requests in the last 60 seconds",
	})

	versionsSubmitted = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "versions_submitted_total",
			Help: "Versions submitted, bucketed to catalog-known values (see api.LabelValues)",
		},
		[]string{"platform", "rancher_version", "k8s_version"},
	)

	// Bucketed, never labelled by cluster identity or user-supplied label. See
	// api.ClusterCountBucket for why: the fleet parameters are caller-controlled on
	// a public unauthenticated endpoint.
	fleetSize = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "fleet_size_total",
			Help: "Downstream cluster count per request, bucketed (see api.ClusterCountBucket)",
		},
		[]string{"clusters"},
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

	prometheus.MustRegister(
		totalRequestsLast60Seconds,
		versionsSubmitted,
		fleetSize,
		requestDuration,
		activeRequests,
	)
}

// loadCatalog reads and validates the dataset, failing closed.
//
// A service that refuses to start is preferable to one confidently serving wrong
// upgrade advice. The old loader did the opposite: it dropped entries it could not
// parse and carried on, which is why EKS was invisible for seven Rancher versions
// for two years without anything complaining.
func loadCatalog() (*catalog.Catalog, error) {
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		return nil, err
	}
	return catalog.Load(data)
}

func main() {
	initMetrics()

	cat, err := loadCatalog()
	if err != nil {
		log.Fatalf("refusing to start: %v", err)
	}
	log.Printf("catalog loaded: %d Rancher versions, generated %s", len(cat.Rancher), cat.GeneratedAt)

	app := fiber.New()
	app.Use(logger.New(logger.Config{
		Format:     "[${time}] ${ip} ${status} - ${latency} ${method} ${path}\n",
		TimeFormat: "2006-01-02 15:04:05",
		TimeZone:   "Local",
	}))

	app.Static("/", "./static")

	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.SendString("OK")
	})

	// Read by scripts/verify-deploy.sh over the environment's public ingress. This
	// is the actual-state check the deploy gate had no way to make.
	app.Get("/version", func(c *fiber.Ctx) error {
		now := time.Now()
		body := fiber.Map{
			"version":           buildVersion,
			"catalog_generated": cat.GeneratedAt,
			"catalog_stale":     cat.IsStale(now),
		}
		if age, ok := cat.Age(now); ok {
			body["catalog_age_days"] = int(age.Hours() / 24)
		}
		return c.JSON(body)
	})

	plan := api.Handler(cat)
	app.Get("/api/plan-upgrade", func(c *fiber.Ctx) error {
		timer := prometheus.NewTimer(requestDuration)
		defer timer.ObserveDuration()

		activeRequests.Inc()
		defer activeRequests.Dec()

		updateRequestTimestamps()

		// Label values are bounded to what the catalog knows. They were previously
		// raw user input on a public unauthenticated endpoint, so any visitor could
		// allocate unbounded Prometheus series by walking version strings.
		// The fleet parameters widen the attack surface: cluster COUNT, per-cluster
		// platforms and free-form cluster LABELS are all caller-controlled. Labels
		// never reach a metric, and the count is bucketed, so the label space stays
		// finite and known in advance.
		p, r, k := api.LabelValues(cat,
			c.Query("downstream_platform_1", c.Query("downstream_platform")),
			c.Query("rancher"),
			c.Query("downstream_k8s_1", c.Query("downstream_k8s")))
		versionsSubmitted.WithLabelValues(p, r, k).Inc()
		fleetSize.WithLabelValues(api.ClusterCountBucket(api.FleetSize(func(key string) string {
			return c.Query(key)
		}))).Inc()

		return plan(c)
	})

	go startMetricsServer()

	log.Fatal(app.Listen(appPort))
}

// updateRequestTimestamps maintains the 60-second sliding window gauge.
func updateRequestTimestamps() {
	mu.Lock()
	defer mu.Unlock()

	now := time.Now()
	requestTimestamps = append(requestTimestamps, now)

	cutoff := now.Add(-60 * time.Second)
	idx := len(requestTimestamps)
	for i, t := range requestTimestamps {
		if t.After(cutoff) {
			idx = i
			break
		}
	}
	requestTimestamps = requestTimestamps[idx:]

	totalRequestsLast60Seconds.Set(float64(len(requestTimestamps)))
}

func metricsPort() string {
	if p := os.Getenv("METRICS_PORT"); p != "" {
		if !strings.HasPrefix(p, ":") {
			return ":" + p
		}
		return p
	}
	return defaultMetricsPort
}

// startMetricsServer serves /metrics from the default Prometheus registry, which is
// where initMetrics registers.
//
// It previously delegated the endpoint to fiberprometheus and ALSO registered a
// stub handler returning nil, so /metrics answered 200 with an empty body. Combined
// with the app binding :9000 while the chart declared 9090, the custom metrics were
// unreachable twice over: nothing scraped the port, and the endpoint had nothing to
// serve. Fixing only the port would have shipped a scrapeable endpoint that reported
// nothing and looked healthy.
//
// It deliberately does NOT call log.Fatal on a bind failure.
//
// It used to, and that makes the metrics port a single point of failure for the
// whole service: anything already holding the port kills the API, which is the
// product. Observability failing is worth shouting about, not worth an outage.
// A missing scrape target is itself an alertable condition now that the port
// matches the chart and Prometheus can actually reach it.
func startMetricsServer() {
	metricsApp := fiber.New(fiber.Config{DisableStartupMessage: true})
	metricsApp.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	port := metricsPort()
	if err := metricsApp.Listen(port); err != nil {
		log.Printf("METRICS UNAVAILABLE: could not listen on %s: %v. "+
			"The API is unaffected and still serving; Prometheus will see this host "+
			"as a down target.", port, err)
	}
}
