package endpoints

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alice-lg/birdwatcher/bird"
	"github.com/julienschmidt/httprouter"
)

const prometheusContentType = "text/plain; version=0.0.4; charset=utf-8"
const defaultPrometheusMetricsCacheTTL = 15 * time.Second

type prometheusLabel struct {
	name  string
	value string
}

type prometheusWriter struct {
	buffer    bytes.Buffer
	described map[string]bool
}

type prometheusMetricsCache struct {
	sync.Mutex
	body      string
	expiresAt time.Time
	ttl       time.Duration
}

var metricsCache = prometheusMetricsCache{ttl: defaultPrometheusMetricsCacheTTL}
var prometheusMetricsCollector = collectPrometheusMetrics

func Metrics() httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		if err := CheckAccess(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}

		w.Header().Set("Content-Type", prometheusContentType)
		w.Write([]byte(PrometheusMetrics(true)))
	}
}

func SetPrometheusMetricsCacheTTL(seconds int) {
	ttl := defaultPrometheusMetricsCacheTTL
	if seconds > 0 {
		ttl = time.Duration(seconds) * time.Second
	}

	setPrometheusMetricsCacheTTL(ttl)
}

func setPrometheusMetricsCacheTTL(ttl time.Duration) {
	metricsCache.Lock()
	defer metricsCache.Unlock()

	metricsCache.body = ""
	metricsCache.expiresAt = time.Time{}
	metricsCache.ttl = ttl
}

func PrometheusMetrics(useCache bool) string {
	if useCache {
		return metricsCache.get(func() string {
			return prometheusMetricsCollector(true)
		})
	}

	return prometheusMetricsCollector(false)
}

func (c *prometheusMetricsCache) get(collect func() string) string {
	c.Lock()
	defer c.Unlock()

	now := time.Now()
	if c.ttl > 0 && c.body != "" && now.Before(c.expiresAt) {
		return c.body
	}

	body := collect()
	if c.ttl > 0 {
		c.body = body
		c.expiresAt = time.Now().Add(c.ttl)
		return c.body
	}

	c.body = ""
	c.expiresAt = time.Time{}
	return body
}

func collectPrometheusMetrics(useCache bool) string {
	status, statusFromCache := bird.Status(useCache)

	protocols := bird.NilParse
	protocolsFromCache := false
	if !bird.IsSpecial(status) {
		protocols, protocolsFromCache = bird.Protocols(useCache)
	}

	return renderPrometheusMetrics(status, statusFromCache, protocols, protocolsFromCache)
}

func renderPrometheusMetrics(status bird.Parsed, statusFromCache bool, protocols bird.Parsed, protocolsFromCache bool) string {
	writer := newPrometheusWriter()

	statusOK := !bird.IsSpecial(status)
	protocolsOK := !bird.IsSpecial(protocols)

	writer.describe("birdwatcher_exporter_info", "gauge", "Birdwatcher exporter build and runtime information.")
	writer.sampleInt("birdwatcher_exporter_info", []prometheusLabel{
		{"version", VERSION},
		{"ip_version", bird.IPVersion},
	}, 1)

	writer.describe("birdwatcher_bird_up", "gauge", "Whether BIRD responded to the status query.")
	writer.sampleBool("birdwatcher_bird_up", nil, statusOK)

	writer.describe("birdwatcher_scrape_success", "gauge", "Whether birdwatcher collected a metric source successfully.")
	writer.sampleBool("birdwatcher_scrape_success", []prometheusLabel{{"command", "status"}}, statusOK)
	writer.sampleBool("birdwatcher_scrape_success", []prometheusLabel{{"command", "protocols"}}, protocolsOK)

	writer.describe("birdwatcher_cache_result", "gauge", "Whether the collected command result came from birdwatcher cache.")
	writer.sampleBool("birdwatcher_cache_result", []prometheusLabel{{"command", "status"}}, statusFromCache)
	writer.sampleBool("birdwatcher_cache_result", []prometheusLabel{{"command", "protocols"}}, protocolsFromCache)

	if statusOK {
		writeStatusMetrics(writer, status)
	}
	if protocolsOK {
		writeProtocolMetrics(writer, protocols)
	}

	return writer.String()
}

func newPrometheusWriter() *prometheusWriter {
	return &prometheusWriter{
		described: map[string]bool{},
	}
}

func (w *prometheusWriter) describe(name string, metricType string, help string) {
	if w.described[name] {
		return
	}

	w.buffer.WriteString("# HELP ")
	w.buffer.WriteString(name)
	w.buffer.WriteByte(' ')
	w.buffer.WriteString(escapePrometheusHelp(help))
	w.buffer.WriteByte('\n')
	w.buffer.WriteString("# TYPE ")
	w.buffer.WriteString(name)
	w.buffer.WriteByte(' ')
	w.buffer.WriteString(metricType)
	w.buffer.WriteByte('\n')

	w.described[name] = true
}

func (w *prometheusWriter) sampleBool(name string, labels []prometheusLabel, value bool) {
	if value {
		w.sampleInt(name, labels, 1)
		return
	}
	w.sampleInt(name, labels, 0)
}

func (w *prometheusWriter) sampleInt(name string, labels []prometheusLabel, value int64) {
	w.sample(name, labels, strconv.FormatInt(value, 10))
}

func (w *prometheusWriter) sampleFloat(name string, labels []prometheusLabel, value float64) {
	w.sample(name, labels, strconv.FormatFloat(value, 'f', -1, 64))
}

func (w *prometheusWriter) sample(name string, labels []prometheusLabel, value string) {
	w.buffer.WriteString(name)
	writePrometheusLabels(&w.buffer, labels)
	w.buffer.WriteByte(' ')
	w.buffer.WriteString(value)
	w.buffer.WriteByte('\n')
}

func (w *prometheusWriter) String() string {
	return w.buffer.String()
}

func writePrometheusLabels(buffer *bytes.Buffer, labels []prometheusLabel) {
	if len(labels) == 0 {
		return
	}

	buffer.WriteByte('{')
	for i, label := range labels {
		if i > 0 {
			buffer.WriteByte(',')
		}
		buffer.WriteString(label.name)
		buffer.WriteString("=\"")
		buffer.WriteString(escapePrometheusLabelValue(label.value))
		buffer.WriteByte('"')
	}
	buffer.WriteByte('}')
}

func writeStatusMetrics(writer *prometheusWriter, status bird.Parsed) {
	statusFields, ok := asParsed(status["status"])
	if !ok {
		return
	}

	writer.describe("birdwatcher_bird_info", "gauge", "BIRD daemon information.")
	writer.sampleInt("birdwatcher_bird_info", []prometheusLabel{
		{"version", stringValue(statusFields, "version")},
		{"router_id", stringValue(statusFields, "router_id")},
		{"ip_version", bird.IPVersion},
	}, 1)

	writeTimestampMetric(writer, "birdwatcher_bird_current_server_timestamp_seconds", "BIRD current server time as Unix timestamp.", nil, stringValue(statusFields, "current_server"))
	writeTimestampMetric(writer, "birdwatcher_bird_last_reboot_timestamp_seconds", "BIRD last reboot time as Unix timestamp.", nil, stringValue(statusFields, "last_reboot"))
	writeTimestampMetric(writer, "birdwatcher_bird_last_reconfig_timestamp_seconds", "BIRD last reconfiguration time as Unix timestamp.", nil, stringValue(statusFields, "last_reconfig"))
}

func writeProtocolMetrics(writer *prometheusWriter, protocols bird.Parsed) {
	protocolMap, ok := asParsed(protocols["protocols"])
	if !ok {
		return
	}

	for _, name := range sortedParsedKeys(protocolMap) {
		protocol, ok := asParsed(protocolMap[name])
		if !ok {
			continue
		}

		baseLabels := protocolLabels(name, protocol)
		state := stringValue(protocol, "state")
		birdProtocol := stringValue(protocol, "bird_protocol")

		infoLabels := appendLabels(baseLabels,
			prometheusLabel{"description", stringValue(protocol, "description")},
			prometheusLabel{"peer_table", stringValue(protocol, "peer_table")},
			prometheusLabel{"neighbor_address", stringValue(protocol, "neighbor_address")},
			prometheusLabel{"neighbor_as", valueString(protocol["neighbor_as"])},
			prometheusLabel{"neighbor_id", stringValue(protocol, "neighbor_id")},
		)
		writer.describe("birdwatcher_protocol_info", "gauge", "BIRD protocol metadata.")
		writer.sampleInt("birdwatcher_protocol_info", infoLabels, 1)

		writer.describe("birdwatcher_protocol_up", "gauge", "Whether the BIRD protocol state is up.")
		writer.sampleBool("birdwatcher_protocol_up", baseLabels, state == "up")

		writer.describe("birdwatcher_protocol_state", "gauge", "BIRD protocol state represented as a labeled gauge.")
		writer.sampleInt("birdwatcher_protocol_state", appendLabels(baseLabels, prometheusLabel{"state", state}), 1)

		writeTimestampMetric(writer, "birdwatcher_protocol_state_changed_timestamp_seconds", "BIRD protocol state change time as Unix timestamp.", baseLabels, stringValue(protocol, "state_changed"))
		writeProtocolNumericMetrics(writer, baseLabels, protocol)
		writeProtocolRouteMetrics(writer, baseLabels, protocol)
		writeProtocolChannelRouteMetrics(writer, baseLabels, protocol)
		writeProtocolRouteChangeMetrics(writer, baseLabels, protocol)

		if birdProtocol == "BGP" {
			writeBgpMetrics(writer, baseLabels, protocol)
		}
	}
}

func writeProtocolNumericMetrics(writer *prometheusWriter, baseLabels []prometheusLabel, protocol bird.Parsed) {
	writer.describe("birdwatcher_protocol_numeric_value", "gauge", "Numeric values reported for a BIRD protocol.")

	for _, field := range sortedParsedKeys(protocol) {
		value, ok := asFloat64(protocol[field])
		if !ok {
			continue
		}

		writer.sampleFloat("birdwatcher_protocol_numeric_value", appendLabels(baseLabels, prometheusLabel{"field", field}), value)
	}
}

func writeProtocolRouteMetrics(writer *prometheusWriter, baseLabels []prometheusLabel, protocol bird.Parsed) {
	routes, ok := asParsed(protocol["routes"])
	if !ok {
		return
	}

	writer.describe("birdwatcher_protocol_routes", "gauge", "BIRD protocol route counts.")
	for _, routeType := range sortedParsedKeys(routes) {
		value, ok := asFloat64(routes[routeType])
		if !ok {
			continue
		}

		writer.sampleFloat("birdwatcher_protocol_routes", appendLabels(baseLabels, prometheusLabel{"route_type", routeType}), value)
	}
}

func writeProtocolChannelRouteMetrics(writer *prometheusWriter, baseLabels []prometheusLabel, protocol bird.Parsed) {
	channels, ok := asParsed(protocol["channels"])
	if !ok {
		return
	}

	writer.describe("birdwatcher_protocol_channel_routes", "gauge", "BIRD protocol route counts per channel.")
	for _, channelName := range sortedParsedKeys(channels) {
		channel, ok := asParsed(channels[channelName])
		if !ok {
			continue
		}

		routes, ok := asParsed(channel["routes"])
		if !ok {
			continue
		}

		for _, routeType := range sortedParsedKeys(routes) {
			value, ok := asFloat64(routes[routeType])
			if !ok {
				continue
			}

			writer.sampleFloat("birdwatcher_protocol_channel_routes", appendLabels(baseLabels,
				prometheusLabel{"channel", channelName},
				prometheusLabel{"route_type", routeType},
			), value)
		}
	}
}

func writeProtocolRouteChangeMetrics(writer *prometheusWriter, baseLabels []prometheusLabel, protocol bird.Parsed) {
	routeChanges, ok := asParsed(protocol["route_changes"])
	if !ok {
		return
	}

	writer.describe("birdwatcher_protocol_route_changes_total", "counter", "BIRD protocol route change counters.")
	for _, changeName := range sortedParsedKeys(routeChanges) {
		change, ok := asParsed(routeChanges[changeName])
		if !ok {
			continue
		}

		direction, changeType := splitRouteChangeName(changeName)
		if direction == "" || changeType == "" {
			continue
		}

		for _, result := range sortedParsedKeys(change) {
			value, ok := asFloat64(change[result])
			if !ok {
				continue
			}

			writer.sampleFloat("birdwatcher_protocol_route_changes_total", appendLabels(baseLabels,
				prometheusLabel{"direction", direction},
				prometheusLabel{"change_type", changeType},
				prometheusLabel{"result", result},
			), value)
		}
	}
}

func writeBgpMetrics(writer *prometheusWriter, baseLabels []prometheusLabel, protocol bird.Parsed) {
	neighborLabels := appendLabels(baseLabels,
		prometheusLabel{"neighbor_address", stringValue(protocol, "neighbor_address")},
		prometheusLabel{"neighbor_as", valueString(protocol["neighbor_as"])},
	)
	bgpState := stringValue(protocol, "bgp_state")
	if bgpState == "" {
		bgpState = stringValue(protocol, "connection")
	}

	writer.describe("birdwatcher_bgp_session_up", "gauge", "Whether the BGP session is established.")
	writer.sampleBool("birdwatcher_bgp_session_up", appendLabels(neighborLabels, prometheusLabel{"bgp_state", bgpState}), bgpState == "Established")

	if neighborAS, ok := asFloat64(protocol["neighbor_as"]); ok {
		writer.describe("birdwatcher_bgp_neighbor_as", "gauge", "BGP neighbor autonomous system number.")
		writer.sampleFloat("birdwatcher_bgp_neighbor_as", neighborLabels, neighborAS)
	}

	writeLimitPairMetric(writer, "birdwatcher_bgp_route_limit", "BGP route limit values.", neighborLabels, stringValue(protocol, "route_limit"), "current", "max")
	writeLimitPairMetric(writer, "birdwatcher_bgp_hold_timer_seconds", "BGP hold timer values in seconds.", neighborLabels, stringValue(protocol, "hold_timer"), "remaining", "configured")
	writeLimitPairMetric(writer, "birdwatcher_bgp_keepalive_timer_seconds", "BGP keepalive timer values in seconds.", neighborLabels, stringValue(protocol, "keepalive_timer"), "remaining", "configured")
}

func writeLimitPairMetric(writer *prometheusWriter, name string, help string, labels []prometheusLabel, value string, firstKind string, secondKind string) {
	first, second, ok := parseIntPair(value)
	if !ok {
		return
	}

	writer.describe(name, "gauge", help)
	writer.sampleInt(name, appendLabels(labels, prometheusLabel{"kind", firstKind}), first)
	writer.sampleInt(name, appendLabels(labels, prometheusLabel{"kind", secondKind}), second)
}

func writeTimestampMetric(writer *prometheusWriter, name string, help string, labels []prometheusLabel, value string) {
	timestamp, ok := parseBirdTime(value)
	if !ok {
		return
	}

	writer.describe(name, "gauge", help)
	writer.sampleInt(name, labels, timestamp.Unix())
}

func protocolLabels(protocolName string, protocol bird.Parsed) []prometheusLabel {
	name := stringValue(protocol, "protocol")
	if name == "" {
		name = protocolName
	}

	return []prometheusLabel{
		{"protocol", name},
		{"bird_protocol", stringValue(protocol, "bird_protocol")},
		{"table", stringValue(protocol, "table")},
		{"ip_version", bird.IPVersion},
	}
}

func appendLabels(labels []prometheusLabel, extra ...prometheusLabel) []prometheusLabel {
	out := make([]prometheusLabel, 0, len(labels)+len(extra))
	out = append(out, labels...)
	out = append(out, extra...)
	return out
}

func splitRouteChangeName(name string) (string, string) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) != 2 {
		return "", ""
	}

	return parts[0], parts[1]
}

func parseIntPair(value string) (int64, int64, bool) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return 0, 0, false
	}

	first, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	second, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if err != nil {
		return 0, 0, false
	}

	return first, second, true
}

func parseBirdTime(value string) (time.Time, bool) {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if value == "" {
		return time.Time{}, false
	}

	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05.000000",
		time.RFC3339,
		time.RFC3339Nano,
	}

	for _, layout := range layouts {
		if strings.Contains(layout, "Z07") {
			parsed, err := time.Parse(layout, value)
			if err == nil {
				return parsed, true
			}
			continue
		}

		parsed, err := time.ParseInLocation(layout, value, time.Local)
		if err == nil {
			return parsed, true
		}
	}

	return time.Time{}, false
}

func sortedParsedKeys(parsed bird.Parsed) []string {
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func asParsed(value interface{}) (bird.Parsed, bool) {
	switch typed := value.(type) {
	case bird.Parsed:
		return typed, true
	case map[string]interface{}:
		return bird.Parsed(typed), true
	default:
		return nil, false
	}
}

func asFloat64(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case jsonNumber:
		value, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return value, true
	default:
		return 0, false
	}
}

func stringValue(parsed bird.Parsed, key string) string {
	return valueString(parsed[key])
}

func valueString(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

type jsonNumber interface {
	Float64() (float64, error)
}

func escapePrometheusHelp(value string) string {
	value = strings.Replace(value, "\\", "\\\\", -1)
	value = strings.Replace(value, "\n", "\\n", -1)
	return value
}

func escapePrometheusLabelValue(value string) string {
	value = strings.Replace(value, "\\", "\\\\", -1)
	value = strings.Replace(value, "\n", "\\n", -1)
	value = strings.Replace(value, "\"", "\\\"", -1)
	return value
}
