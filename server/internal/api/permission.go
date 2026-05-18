package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"mantisops/server/internal/store"
)

// --- Role hierarchy ---

var roleLevel = map[string]int{
	"viewer":   1,
	"operator": 2,
	"admin":    3,
}

func RequireRole(minRole string) gin.HandlerFunc {
	minLevel := roleLevel[minRole]
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		roleStr, _ := role.(string)
		if roleLevel[roleStr] < minLevel {
			c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// --- PermissionSet ---

type PermissionSet struct {
	Groups    map[string]bool
	Servers   map[string]bool // direct + expanded from groups
	Databases map[string]bool
	Probes    map[string]bool
}

func (ps *PermissionSet) CanSeeServer(hostID string) bool {
	if ps == nil {
		return true
	}
	return ps.Servers[hostID]
}

func (ps *PermissionSet) CanSeeProbe(probeID string) bool {
	if ps == nil {
		return true
	}
	return ps.Probes[probeID]
}

func (ps *PermissionSet) CanSeeDatabase(hostID string) bool {
	if ps == nil {
		return true
	}
	return ps.Databases[hostID]
}

// CanSeeAlertTarget checks if the user can see an alert based on its rule type and target_id.
// Rule types: server_offline/cpu/memory/network_rx/network_tx/gpu_* → target_id is host_id
// disk → target_id is "host_id:mount_point"
// container → target_id is "host_id:container_name"
// probe_down → target_id is probe rule ID string
func (ps *PermissionSet) CanSeeAlertTarget(ruleType, targetID string) bool {
	if ps == nil {
		return true
	}
	if ruleType == "probe_down" {
		return ps.Probes[targetID]
	}
	// database (RDS) targets: "db:<host_id>" → resource-level DB permission
	if strings.HasPrefix(targetID, "db:") {
		return ps.Databases[strings.TrimPrefix(targetID, "db:")]
	}
	// disk and container: extract host_id before colon
	hostID := targetID
	if ruleType == "disk" || ruleType == "container" {
		if idx := strings.IndexByte(targetID, ':'); idx > 0 {
			hostID = targetID[:idx]
		}
	}
	return ps.Servers[hostID]
}

// CanSeeLogSource checks if the user can see a log source.
// admin (ps==nil) sees all. Others only see "agent:{host_id}" sources.
func (ps *PermissionSet) CanSeeLogSource(source string) bool {
	if ps == nil {
		return true
	}
	// source=server is global system log, non-admin cannot see
	if source == "server" {
		return false
	}
	// source=agent:{host_id}
	if strings.HasPrefix(source, "agent:") {
		hostID := strings.TrimPrefix(source, "agent:")
		return ps.Servers[hostID]
	}
	return false
}

// CanSeeEvent checks if the user can see an alert event by its target_id.
// Probe IDs are numeric-only; server targets may include "host_id:suffix" for disk/container.
func (ps *PermissionSet) CanSeeEvent(targetID string) bool {
	if ps == nil {
		return true
	}
	// Check if it's a probe (numeric ID)
	if ps.Probes[targetID] {
		return true
	}
	// database (RDS) events: target_id is "db:<host_id>"
	if strings.HasPrefix(targetID, "db:") {
		return ps.Databases[strings.TrimPrefix(targetID, "db:")]
	}
	// Extract host_id (before colon for disk/container targets)
	hostID := targetID
	if idx := strings.IndexByte(targetID, ':'); idx > 0 {
		hostID = targetID[:idx]
	}
	return ps.Servers[hostID]
}

// AllVisibleTargetIDs returns all target IDs for SQL-level filtering (servers + probes).
func (ps *PermissionSet) AllVisibleTargetIDs() []string {
	if ps == nil {
		return nil
	}
	ids := make([]string, 0, len(ps.Servers)+len(ps.Probes))
	for id := range ps.Servers {
		ids = append(ids, id)
	}
	for id := range ps.Probes {
		ids = append(ids, id)
	}
	return ids
}

// AllVisibleLogSources returns source strings for SQL-level filtering.
func (ps *PermissionSet) AllVisibleLogSources() []string {
	if ps == nil {
		return nil
	}
	sources := make([]string, 0, len(ps.Servers))
	for hostID := range ps.Servers {
		sources = append(sources, "agent:"+hostID)
	}
	return sources
}

// --- PermissionCache ---

type PermissionCache struct {
	mu          sync.RWMutex
	cache       map[int64]*PermissionSet
	userStore   *store.UserStore
	serverStore *store.ServerStore
}

func NewPermissionCache(us *store.UserStore, ss *store.ServerStore) *PermissionCache {
	return &PermissionCache{
		cache:       make(map[int64]*PermissionSet),
		userStore:   us,
		serverStore: ss,
	}
}

func (pc *PermissionCache) Get(userID int64) (*PermissionSet, error) {
	pc.mu.RLock()
	if ps, ok := pc.cache[userID]; ok {
		pc.mu.RUnlock()
		return ps, nil
	}
	pc.mu.RUnlock()

	perms, err := pc.userStore.GetPermissions(userID)
	if err != nil {
		return nil, err
	}

	ps := &PermissionSet{
		Groups:    make(map[string]bool),
		Servers:   make(map[string]bool),
		Databases: make(map[string]bool),
		Probes:    make(map[string]bool),
	}

	for _, p := range perms {
		switch p.ResType {
		case "group":
			ps.Groups[p.ResID] = true
		case "server":
			ps.Servers[p.ResID] = true
		case "database":
			ps.Databases[p.ResID] = true
		case "probe":
			ps.Probes[p.ResID] = true
		}
	}

	// Expand groups → servers
	if len(ps.Groups) > 0 {
		servers, _ := pc.serverStore.List()
		for _, srv := range servers {
			if srv.GroupID != nil {
				gidStr := fmt.Sprintf("%d", *srv.GroupID)
				if ps.Groups[gidStr] {
					ps.Servers[srv.HostID] = true
				}
			}
		}
	}

	pc.mu.Lock()
	pc.cache[userID] = ps
	pc.mu.Unlock()
	return ps, nil
}

func (pc *PermissionCache) Invalidate(userID int64) {
	pc.mu.Lock()
	delete(pc.cache, userID)
	pc.mu.Unlock()
}

func (pc *PermissionCache) InvalidateAll() {
	pc.mu.Lock()
	pc.cache = make(map[int64]*PermissionSet)
	pc.mu.Unlock()
}

// GetPermissionSet is a helper to get PermissionSet from gin context.
// Returns nil for admin (no filtering needed).
func GetPermissionSet(c *gin.Context, pc *PermissionCache) *PermissionSet {
	role, _ := c.Get("role")
	if role == "admin" {
		return nil
	}
	userID, _ := c.Get("user_id")
	uid, ok := userID.(int64)
	if !ok {
		return &PermissionSet{} // empty set = see nothing
	}
	ps, err := pc.Get(uid)
	if err != nil {
		return &PermissionSet{}
	}
	return ps
}
