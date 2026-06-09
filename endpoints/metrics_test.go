package endpoints

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alice-lg/birdwatcher/bird"
)

func withPrometheusMetricsCollector(t *testing.T, collector func(bool) string) {
	t.Helper()

	oldCollector := prometheusMetricsCollector
	prometheusMetricsCollector = collector
	setPrometheusMetricsCacheTTL(defaultPrometheusMetricsCacheTTL)

	t.Cleanup(func() {
		prometheusMetricsCollector = oldCollector
		setPrometheusMetricsCacheTTL(defaultPrometheusMetricsCacheTTL)
	})
}

func TestRenderPrometheusMetrics(t *testing.T) {
	oldVersion := VERSION
	oldIPVersion := bird.IPVersion
	oldLocal := time.Local
	VERSION = "test-version"
	bird.IPVersion = "4"
	time.Local = time.UTC
	defer func() {
		VERSION = oldVersion
		bird.IPVersion = oldIPVersion
		time.Local = oldLocal
	}()

	status := bird.Parsed{
		"status": bird.Parsed{
			"current_server": "1970-01-01 00:00:10",
			"last_reboot":    "1970-01-01 00:00:01",
			"last_reconfig":  "1970-01-01 00:00:05",
			"router_id":      "172.25.3.2",
			"version":        "2.0.7",
		},
	}
	protocols := bird.Parsed{
		"protocols": bird.Parsed{
			"R194_42": bird.Parsed{
				"protocol":         "R194_42",
				"bird_protocol":    "BGP",
				"table":            "master",
				"state":            "up",
				"state_changed":    "1970-01-01 00:00:20",
				"description":      "Nada Co",
				"neighbor_address": "172.31.194.42",
				"neighbor_as":      int64(1764),
				"neighbor_id":      "172.31.194.42",
				"bgp_state":        "Established",
				"preference":       int64(100),
				"import_limit":     int64(200000),
				"route_limit":      "710/200000",
				"hold_timer":       "151/180",
				"keepalive_timer":  "43/60",
				"routes": bird.Parsed{
					"exported": int64(154998),
					"imported": int64(710),
				},
				"channels": bird.Parsed{
					"ipv4": bird.Parsed{
						"routes": bird.Parsed{
							"exported": int64(154998),
							"imported": int64(710),
						},
					},
				},
				"route_changes": bird.Parsed{
					"import_updates": bird.Parsed{
						"accepted": int64(710),
						"filtered": int64(0),
					},
				},
			},
		},
	}

	got := renderPrometheusMetrics(status, true, protocols, false)
	checks := []string{
		`# HELP birdwatcher_exporter_info Birdwatcher exporter build and runtime information.`,
		`birdwatcher_exporter_info{version="test-version",ip_version="4"} 1`,
		`birdwatcher_bird_up 1`,
		`birdwatcher_scrape_success{command="status"} 1`,
		`birdwatcher_scrape_success{command="protocols"} 1`,
		`birdwatcher_cache_result{command="status"} 1`,
		`birdwatcher_cache_result{command="protocols"} 0`,
		`birdwatcher_bird_info{version="2.0.7",router_id="172.25.3.2",ip_version="4"} 1`,
		`birdwatcher_bird_last_reboot_timestamp_seconds 1`,
		`birdwatcher_protocol_info{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",description="Nada Co",peer_table="",neighbor_address="172.31.194.42",neighbor_as="1764",neighbor_id="172.31.194.42"} 1`,
		`birdwatcher_protocol_up{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4"} 1`,
		`birdwatcher_protocol_state{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",state="up"} 1`,
		`birdwatcher_protocol_state_changed_timestamp_seconds{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4"} 20`,
		`birdwatcher_protocol_numeric_value{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",field="preference"} 100`,
		`birdwatcher_protocol_routes{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",route_type="imported"} 710`,
		`birdwatcher_protocol_channel_routes{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",channel="ipv4",route_type="exported"} 154998`,
		`birdwatcher_protocol_route_changes_total{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",direction="import",change_type="updates",result="accepted"} 710`,
		`birdwatcher_bgp_session_up{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",neighbor_address="172.31.194.42",neighbor_as="1764",bgp_state="Established"} 1`,
		`birdwatcher_bgp_neighbor_as{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",neighbor_address="172.31.194.42",neighbor_as="1764"} 1764`,
		`birdwatcher_bgp_route_limit{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",neighbor_address="172.31.194.42",neighbor_as="1764",kind="max"} 200000`,
		`birdwatcher_bgp_hold_timer_seconds{protocol="R194_42",bird_protocol="BGP",table="master",ip_version="4",neighbor_address="172.31.194.42",neighbor_as="1764",kind="remaining"} 151`,
	}

	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("missing metric line %q in:\n%s", check, got)
		}
	}
}

func TestRenderPrometheusMetricsWhenBirdIsUnavailable(t *testing.T) {
	got := renderPrometheusMetrics(bird.BirdError, false, bird.NilParse, false)

	checks := []string{
		`birdwatcher_bird_up 0`,
		`birdwatcher_scrape_success{command="status"} 0`,
		`birdwatcher_scrape_success{command="protocols"} 0`,
	}

	for _, check := range checks {
		if !strings.Contains(got, check) {
			t.Fatalf("missing metric line %q in:\n%s", check, got)
		}
	}
}

func TestPrometheusMetricsCachesScrapeResult(t *testing.T) {
	calls := 0
	withPrometheusMetricsCollector(t, func(useCache bool) string {
		if !useCache {
			t.Fatal("expected cached metrics collection")
		}
		calls++
		return fmt.Sprintf("birdwatcher_test_metric %d\n", calls)
	})

	first := PrometheusMetrics(true)
	second := PrometheusMetrics(true)

	if calls != 1 {
		t.Fatalf("collector called %d times, want 1", calls)
	}
	if first != second {
		t.Fatalf("cached scrape changed:\nfirst: %s\nsecond: %s", first, second)
	}
}

func TestPrometheusMetricsCollapsesConcurrentScrapes(t *testing.T) {
	calls := 0
	var firstCollect sync.Once
	collecting := make(chan struct{})
	releaseCollect := make(chan struct{})

	withPrometheusMetricsCollector(t, func(useCache bool) string {
		if !useCache {
			t.Fatal("expected cached metrics collection")
		}
		calls++
		firstCollect.Do(func() {
			close(collecting)
			<-releaseCollect
		})
		return fmt.Sprintf("birdwatcher_test_metric %d\n", calls)
	})

	const scrapeCount = 8
	results := make(chan string, scrapeCount)
	var wg sync.WaitGroup
	for i := 0; i < scrapeCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- PrometheusMetrics(true)
		}()
	}

	<-collecting
	close(releaseCollect)
	wg.Wait()
	close(results)

	if calls != 1 {
		t.Fatalf("collector called %d times, want 1", calls)
	}

	var first string
	for result := range results {
		if first == "" {
			first = result
			continue
		}
		if result != first {
			t.Fatalf("concurrent scrape returned different result:\nfirst: %s\nresult: %s", first, result)
		}
	}
}

func TestMetricsEndpointIgnoresUncachedQuery(t *testing.T) {
	oldConf := Conf
	Conf = ServerConfig{AllowUncached: true}
	t.Cleanup(func() {
		Conf = oldConf
	})

	calls := 0
	withPrometheusMetricsCollector(t, func(useCache bool) string {
		if !useCache {
			t.Fatal("metrics endpoint must not bypass scrape cache")
		}
		calls++
		return "birdwatcher_test_metric 1\n"
	})

	handler := Metrics()
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/metrics?uncached=true", nil)
		req.RemoteAddr = "192.0.2.10:12345"
		res := httptest.NewRecorder()

		handler(res, req, nil)

		if res.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", res.Code, http.StatusOK)
		}
		if got := res.Header().Get("Content-Type"); got != prometheusContentType {
			t.Fatalf("Content-Type = %q, want %q", got, prometheusContentType)
		}
	}

	if calls != 1 {
		t.Fatalf("collector called %d times, want 1", calls)
	}
}

func TestPrometheusEscaping(t *testing.T) {
	got := escapePrometheusLabelValue("a\\b\"c\nd")
	want := "a\\\\b\\\"c\\nd"

	if got != want {
		t.Fatalf("escapePrometheusLabelValue() = %q, want %q", got, want)
	}
}
