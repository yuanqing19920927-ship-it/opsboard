package alert

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"mantisops/server/internal/collector"
	"mantisops/server/internal/model"
	"mantisops/server/internal/store"
	"mantisops/server/internal/ws"
	pb "mantisops/server/proto/gen"
)

type ruleState struct {
	consecutiveHits    int
	consecutiveNormals int
	firing             bool
	silenced           bool
	eventID            int
	lastValue          float64
	lastTimestamp      time.Time
}

// NasProvider interface for reading NAS metrics and health.
type NasProvider interface {
	GetAllMetrics() map[int64]*collector.NasMetricsSnapshot
	GetDeviceHealth(nasID int64) *collector.NasDeviceHealth
	ListDeviceIDs() []int64
}

// DatabaseProvider interface for reading RDS (database) metrics and the
// monitored instance list. Implemented by api.DatabaseHandler, which keeps a
// 30s-refreshed cache of RDS metrics queried from VictoriaMetrics.
type DatabaseProvider interface {
	// DatabaseAlertTargets returns monitored RDS instances as hostID -> label.
	DatabaseAlertTargets() map[string]string
	// DatabaseMetrics returns the latest cached metric snapshot for one host.
	DatabaseMetrics(hostID string) map[string]float64
}

// Alerter is the core alert engine implementing evaluation loop, state machine, and notification dispatch.
type Alerter struct {
	store    *store.AlertStore
	hub      *ws.Hub
	metrics  MetricsProvider
	probes   ProbeProvider
	servers  ServerProvider
	nas      NasProvider
	network  NetworkProvider
	database DatabaseProvider
	mu       sync.RWMutex
	states   map[string]*ruleState
	stopCh   chan struct{}
}

// NewAlerter creates a new Alerter instance.
func NewAlerter(s *store.AlertStore, hub *ws.Hub, metrics MetricsProvider, probes ProbeProvider, servers ServerProvider, nas NasProvider, network NetworkProvider, database DatabaseProvider) *Alerter {
	return &Alerter{
		store:    s,
		hub:      hub,
		metrics:  metrics,
		probes:   probes,
		servers:  servers,
		nas:      nas,
		network:  network,
		database: database,
		states:   make(map[string]*ruleState),
		stopCh:   make(chan struct{}),
	}
}

// Start initializes and starts the alerter loops.
func (a *Alerter) Start() {
	a.recoverState()
	if err := a.store.ResetAllSendingNotifications(); err != nil {
		log.Printf("[alerter] reset stale notifications: %v", err)
	}
	go a.loop()
	go a.notifyLoop()
}

// Stop signals the alerter to stop.
func (a *Alerter) Stop() {
	close(a.stopCh)
}

// recoverState loads firing events from DB to restore in-memory state.
func (a *Alerter) recoverState() {
	events, err := a.store.ListFiringEvents()
	if err != nil {
		log.Printf("[alerter] recover state: %v", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range events {
		key := fmt.Sprintf("%d:%s", e.RuleID, e.TargetID)
		a.states[key] = &ruleState{
			firing:   true,
			silenced: e.Silenced,
			eventID:  e.ID,
		}
	}
	log.Printf("[alerter] recovered %d firing states", len(events))
}

// loop runs the main evaluation ticker.
func (a *Alerter) loop() {
	a.evaluate()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.evaluate()
		}
	}
}

// evaluate runs one full evaluation cycle.
func (a *Alerter) evaluate() {
	rules, err := a.store.ListEnabledRules()
	if err != nil {
		log.Printf("[alerter] list rules: %v", err)
		return
	}

	a.cleanupDisabledRules(rules)

	servers, err := a.servers.List()
	if err != nil {
		log.Printf("[alerter] list servers: %v", err)
		return
	}

	for _, rule := range rules {
		results := Evaluate(rule, servers, a.metrics, a.probes)
		for _, r := range results {
			if r.Skip {
				continue
			}
			a.processResult(rule, r)
		}
	}

	if a.nas != nil {
		a.evaluateNas(rules)
	}

	if a.network != nil {
		a.evaluateNetworkDevices(rules)
	}

	if a.database != nil {
		a.evaluateDatabases(rules)
	}

	a.cleanupGoneTargets(servers)
}

// evaluateDatabases evaluates all db_* alert rules against cached RDS metrics.
func (a *Alerter) evaluateDatabases(rules []model.AlertRule) {
	targets := a.database.DatabaseAlertTargets()
	if len(targets) == 0 {
		return
	}
	for _, rule := range rules {
		if !IsDatabaseRuleType(rule.Type) {
			continue
		}
		for hostID, label := range targets {
			// rule.TargetID empty = all databases; otherwise it is the
			// "db:<hostID>" form sent by the UI (bare hostID also tolerated).
			if rule.TargetID != "" && rule.TargetID != "db:"+hostID && rule.TargetID != hostID {
				continue
			}
			metrics := a.database.DatabaseMetrics(hostID)
			for _, r := range EvaluateDatabase(rule, hostID, label, metrics) {
				if r.Skip {
					continue
				}
				a.processResult(rule, r)
			}
		}
	}
}

// processResult implements the state machine for a single evaluation result.
func (a *Alerter) processResult(rule model.AlertRule, result EvalResult) {
	a.mu.Lock()
	defer a.mu.Unlock()

	st, ok := a.states[result.StateKey]
	if !ok {
		st = &ruleState{}
		a.states[result.StateKey] = st
	}
	st.lastValue = result.Value

	// Duration is stored in seconds; evaluation runs every 30s.
	// Convert to the number of consecutive evaluation cycles required.
	requiredCycles := rule.Duration / 30
	if requiredCycles < 1 {
		requiredCycles = 1
	}

	if result.Hit {
		st.consecutiveHits++
		st.consecutiveNormals = 0
		if st.consecutiveHits >= requiredCycles && !st.firing {
			a.fireAlert(rule, result, st)
		}
	} else {
		st.consecutiveNormals++
		st.consecutiveHits = 0
		if st.consecutiveNormals >= requiredCycles && st.firing {
			a.resolveAlert(st, "auto")
		}
	}
}

// fireAlert creates a new alert event and broadcasts it.
// Called with mu held.
func (a *Alerter) fireAlert(rule model.AlertRule, result EvalResult, st *ruleState) {
	event := &model.AlertEvent{
		RuleID:      rule.ID,
		RuleName:    rule.Name,
		TargetID:    result.TargetID,
		TargetLabel: result.Label,
		Level:       rule.Level,
		Value:       result.Value,
		Message:     result.Message,
		FiredAt:     time.Now(),
	}

	eventID, err := a.store.FireAlert(event)
	if err != nil {
		log.Printf("[alerter] fire alert rule=%d target=%s: %v", rule.ID, result.TargetID, err)
		return
	}

	st.firing = true
	st.eventID = int(eventID)
	event.ID = int(eventID)
	event.Status = "firing"

	a.hub.BroadcastAlertFiring(int(eventID), rule.Type, result.TargetID, map[string]interface{}{
		"type": "alert",
		"data": event,
	})
	log.Printf("[alerter] FIRE rule=%d target=%s value=%.2f", rule.ID, result.TargetID, result.Value)
}

// resolveAlert resolves an existing alert event and broadcasts it.
// Called with mu held.
func (a *Alerter) resolveAlert(st *ruleState, resolveType string) {
	if err := a.store.ResolveAlert(st.eventID, resolveType); err != nil {
		log.Printf("[alerter] resolve alert event=%d: %v", st.eventID, err)
		return
	}

	a.hub.BroadcastAlertResolved(st.eventID, map[string]interface{}{
		"type": "alert_resolved",
		"data": map[string]interface{}{"id": st.eventID},
	})
	log.Printf("[alerter] RESOLVE event=%d type=%s", st.eventID, resolveType)

	st.firing = false
	st.silenced = false
	st.eventID = 0
	st.consecutiveHits = 0
	st.consecutiveNormals = 0
}

// evaluateNas evaluates all NAS-type alert rules against cached NAS metrics.
func (a *Alerter) evaluateNas(rules []model.AlertRule) {
	allMetrics := a.nas.GetAllMetrics()

	for _, rule := range rules {
		if !strings.HasPrefix(rule.Type, "nas_") {
			continue
		}
		if rule.Type == "nas_offline" {
			a.evaluateNasOffline(rule, allMetrics)
			continue
		}

		// Filter by target_id
		targetNasIDs := make(map[int64]bool)
		if rule.TargetID != "" {
			parts := strings.SplitN(rule.TargetID, ":", 2)
			if len(parts) == 2 {
				id, _ := strconv.ParseInt(parts[1], 10, 64)
				if id > 0 {
					targetNasIDs[id] = true
				}
			}
		}

		for nasID, snap := range allMetrics {
			if len(targetNasIDs) > 0 && !targetNasIDs[nasID] {
				continue
			}
			// Timestamp dedup
			stateKey := fmt.Sprintf("%d:nas:%d", rule.ID, nasID)
			a.mu.RLock()
			st := a.states[stateKey]
			a.mu.RUnlock()
			if st != nil && snap != nil && st.lastTimestamp == snap.Timestamp {
				continue
			}

			results := EvaluateNas(rule, nasID, snap)
			for _, r := range results {
				a.processResult(rule, r)
			}

			a.mu.Lock()
			if a.states[stateKey] == nil {
				a.states[stateKey] = &ruleState{}
			}
			if snap != nil {
				a.states[stateKey].lastTimestamp = snap.Timestamp
			}
			a.mu.Unlock()
		}
	}
}

// evaluateNasOffline evaluates NAS offline rules based on device health failure count.
func (a *Alerter) evaluateNasOffline(rule model.AlertRule, allMetrics map[int64]*collector.NasMetricsSnapshot) {
	allIDs := a.nas.ListDeviceIDs()
	for _, nasID := range allIDs {
		if rule.TargetID != "" {
			parts := strings.SplitN(rule.TargetID, ":", 2)
			if len(parts) == 2 {
				tid, _ := strconv.ParseInt(parts[1], 10, 64)
				if tid > 0 && tid != nasID {
					continue
				}
			}
		}
		targetID := fmt.Sprintf("nas:%d", nasID)
		stateKey := fmt.Sprintf("%d:%s", rule.ID, targetID)
		health := a.nas.GetDeviceHealth(nasID)
		var failCount int
		if health != nil {
			failCount = health.FailureCount
		}
		hit := health != nil && health.FailureCount >= 3

		a.processResult(rule, EvalResult{
			StateKey: stateKey,
			TargetID: targetID,
			Hit:      hit,
			Value:    float64(failCount),
			Message:  fmt.Sprintf("NAS device offline (failures: %d)", failCount),
		})
	}
}

// evaluateNetworkDevices evaluates all network_device_offline alert rules.
// The consecutive-count mechanism in processResult is bypassed for this type:
// ConnectivityMonitor already enforces a 3-fail threshold before setting
// status="offline", so we fire immediately on the first offline observation
// and resolve immediately when the device is back online.
// We achieve this by using rule.Duration=1 semantics — the rule struct is
// temporarily overridden so processResult fires/resolves on first hit/normal.
func (a *Alerter) evaluateNetworkDevices(rules []model.AlertRule) {
	for _, rule := range rules {
		if rule.Type != "network_device_offline" {
			continue
		}
		results := evalNetworkDeviceOffline(rule, a.network)
		// Override Duration to 1 so processResult fires/resolves immediately
		// without waiting for multiple consecutive cycles.
		immediateRule := rule
		immediateRule.Duration = 1
		for _, r := range results {
			a.processResult(immediateRule, r)
		}
	}
}

// cleanupDisabledRules removes non-firing states for rules that are no longer enabled.
func (a *Alerter) cleanupDisabledRules(enabledRules []model.AlertRule) {
	enabledIDs := make(map[int]bool, len(enabledRules))
	for _, r := range enabledRules {
		enabledIDs[r.ID] = true
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for key, st := range a.states {
		ruleID := extractRuleID(key)
		if ruleID > 0 && !enabledIDs[ruleID] && !st.firing {
			delete(a.states, key)
		}
	}
}

// cleanupGoneTargets resolves alerts for targets that no longer exist.
func (a *Alerter) cleanupGoneTargets(servers []model.Server) {
	serverIDs := make(map[string]bool, len(servers))
	for _, s := range servers {
		serverIDs[s.HostID] = true
	}

	// Collect disk mounts and container names per host
	hostDisks := make(map[string]map[string]bool)
	hostContainers := make(map[string]map[string]bool)
	for hostID := range serverIDs {
		m := a.metrics.GetLatestMetrics(hostID)
		if m != nil {
			hostDisks[hostID] = extractDiskMounts(m)
			hostContainers[hostID] = extractContainerNames(m)
		}
	}

	// Collect probe result IDs
	probeIDs := make(map[string]bool)
	for _, pr := range a.probes.GetAllResults() {
		probeIDs[fmt.Sprintf("%d", pr.RuleID)] = true
	}

	// Collect NAS device IDs
	nasIDs := make(map[string]bool)
	if a.nas != nil {
		for _, id := range a.nas.ListDeviceIDs() {
			nasIDs[fmt.Sprintf("nas:%d", id)] = true
		}
	}

	// Collect network device IDs
	netdevIDs := make(map[string]bool)
	if a.network != nil {
		if devices, err := a.network.GetAllDevices(); err == nil {
			for _, dev := range devices {
				netdevIDs[fmt.Sprintf("netdev:%d", dev.ID)] = true
			}
		}
	}

	// Collect database (RDS) host IDs
	dbIDs := make(map[string]bool)
	if a.database != nil {
		for hostID := range a.database.DatabaseAlertTargets() {
			dbIDs["db:"+hostID] = true
		}
	}

	// Find firing states whose targets are gone
	type goneEntry struct {
		key     string
		eventID int
	}
	var gone []goneEntry

	a.mu.Lock()
	for key, st := range a.states {
		if !st.firing {
			continue
		}
		if !a.isTargetPresent(key, serverIDs, hostDisks, hostContainers, probeIDs, nasIDs, netdevIDs, dbIDs) {
			gone = append(gone, goneEntry{key: key, eventID: st.eventID})
		}
	}
	a.mu.Unlock()

	// Resolve gone targets outside lock, then delete states
	for _, g := range gone {
		if err := a.store.ResolveAlert(g.eventID, "target_gone"); err != nil {
			log.Printf("[alerter] resolve gone target event=%d: %v", g.eventID, err)
			continue
		}
		a.hub.BroadcastAlertResolved(g.eventID, map[string]interface{}{
			"type": "alert_resolved",
			"data": map[string]interface{}{"id": g.eventID},
		})
		log.Printf("[alerter] RESOLVE (target_gone) event=%d key=%s", g.eventID, g.key)

		a.mu.Lock()
		delete(a.states, g.key)
		a.mu.Unlock()
	}
}

// isTargetPresent checks if the target referenced by a state key still exists.
// Called with mu held for reading.
func (a *Alerter) isTargetPresent(key string, serverIDs map[string]bool, hostDisks, hostContainers map[string]map[string]bool, probeIDs, nasIDs, netdevIDs, dbIDs map[string]bool) bool {
	// State key format: "ruleID:targetID" or "ruleID:hostID:mount_or_container"
	// NAS keys:    "ruleID:nas:nasID" or "ruleID:nas:nasID:subLabel"
	// Netdev keys: "ruleID:netdev:deviceID"
	// DB keys:     "ruleID:db:hostID"
	parts := strings.SplitN(key, ":", 3)
	if len(parts) < 2 {
		return true // can't parse, assume present
	}

	targetID := parts[1]

	// Check network device targets: key is "ruleID:netdev:deviceID"
	if targetID == "netdev" && len(parts) == 3 {
		netdevKey := fmt.Sprintf("netdev:%s", parts[2])
		return netdevIDs[netdevKey]
	}

	// Check database targets: key is "ruleID:db:hostID"
	if targetID == "db" && len(parts) == 3 {
		return dbIDs["db:"+parts[2]]
	}

	// Check NAS targets: key starts with "ruleID:nas:..."
	if targetID == "nas" && len(parts) == 3 {
		// parts[2] could be "123" or "123:md0" — extract "nas:123"
		subParts := strings.SplitN(parts[2], ":", 2)
		nasKey := fmt.Sprintf("nas:%s", subParts[0])
		return nasIDs[nasKey]
	}

	// Check if it's a probe target (pure numeric after ruleID)
	if len(parts) == 2 {
		if _, err := strconv.Atoi(targetID); err == nil {
			// Could be a probe target ID — check both server and probe
			if serverIDs[targetID] {
				return true
			}
			return probeIDs[targetID]
		}
		// Simple server target
		return serverIDs[targetID]
	}

	// 3-part key: ruleID:hostID:subTarget (disk mount or container name)
	hostID := parts[1]
	subTarget := parts[2]

	if !serverIDs[hostID] {
		return false
	}

	// Check disk mounts
	if mounts, ok := hostDisks[hostID]; ok {
		if mounts[subTarget] {
			return true
		}
	}
	// Check container names
	if containers, ok := hostContainers[hostID]; ok {
		if containers[subTarget] {
			return true
		}
	}

	// Sub-target not found in current metrics
	return false
}

// notifyLoop runs the notification dispatch ticker.
func (a *Alerter) notifyLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.processNotifications()
		}
	}
}

// processNotifications processes all pending notifications.
func (a *Alerter) processNotifications() {
	if err := a.store.ResetStaleNotifications(); err != nil {
		log.Printf("[alerter] reset stale notifications: %v", err)
	}

	pending, err := a.store.ListPendingNotifications()
	if err != nil {
		log.Printf("[alerter] list pending notifications: %v", err)
		return
	}

	for _, p := range pending {
		claimed, err := a.store.ClaimNotification(p.ID)
		if err != nil {
			log.Printf("[alerter] claim notification %d: %v", p.ID, err)
			continue
		}
		if !claimed {
			continue
		}

		ch := p.Channel
		evt := p.Event
		if err := SendNotification(&ch, &evt, p.NotifyType); err != nil {
			log.Printf("[alerter] send notification %d: %v", p.ID, err)
			_ = a.store.MarkNotificationFailed(p.ID, err.Error())
		} else {
			_ = a.store.MarkNotificationSent(p.ID)
		}
	}
}

// AckEvent acknowledges (silences) an alert event.
func (a *Alerter) AckEvent(eventID int, username string) error {
	if err := a.store.AckEvent(eventID, username); err != nil {
		return err
	}

	a.mu.Lock()
	for _, st := range a.states {
		if st.eventID == eventID {
			st.silenced = true
			break
		}
	}
	a.mu.Unlock()

	a.hub.BroadcastAlertAcked(eventID, map[string]interface{}{
		"type": "alert_acked",
		"data": map[string]interface{}{"id": eventID, "acked_by": username},
	})
	return nil
}

// OnRuleChanged resolves all firing events for a rule and clears related states.
func (a *Alerter) OnRuleChanged(ruleID int, resolveType string) {
	if err := a.store.ResolveEventsByRule(ruleID, resolveType); err != nil {
		log.Printf("[alerter] resolve events for rule %d: %v", ruleID, err)
	}

	prefix := fmt.Sprintf("%d:", ruleID)
	a.mu.Lock()
	for key, st := range a.states {
		if strings.HasPrefix(key, prefix) {
			if st.firing {
				a.hub.BroadcastAlertResolved(st.eventID, map[string]interface{}{
					"type": "alert_resolved",
					"data": map[string]interface{}{"id": st.eventID},
				})
			}
			delete(a.states, key)
		}
	}
	a.mu.Unlock()
}

// OnRuleUpdated resets consecutive counters for a rule so it re-evaluates from scratch.
func (a *Alerter) OnRuleUpdated(ruleID int) {
	prefix := fmt.Sprintf("%d:", ruleID)
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, st := range a.states {
		if strings.HasPrefix(key, prefix) {
			st.consecutiveHits = 0
			st.consecutiveNormals = 0
		}
	}
}

// --- helpers ---

func extractRuleID(stateKey string) int {
	idx := strings.Index(stateKey, ":")
	if idx < 0 {
		return 0
	}
	id, err := strconv.Atoi(stateKey[:idx])
	if err != nil {
		return 0
	}
	return id
}

func extractDiskMounts(m *pb.MetricsPayload) map[string]bool {
	mounts := make(map[string]bool)
	for _, d := range m.Disks {
		mounts[d.MountPoint] = true
	}
	return mounts
}

func extractContainerNames(m *pb.MetricsPayload) map[string]bool {
	names := make(map[string]bool)
	for _, c := range m.Containers {
		names[c.Name] = true
	}
	return names
}
