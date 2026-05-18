package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"mantisops/server/internal/store"
)

type DatabaseHandler struct {
	cloudStore *store.CloudStore
	vm         *store.VictoriaStore
	permCache  *PermissionCache
	mu         sync.RWMutex
	cache      map[string]map[string]float64
}

func NewDatabaseHandler(cloudStore *store.CloudStore, vm *store.VictoriaStore, pc *PermissionCache) *DatabaseHandler {
	h := &DatabaseHandler{
		cloudStore: cloudStore,
		vm:         vm,
		permCache:  pc,
		cache:      make(map[string]map[string]float64),
	}
	// 后台每 30 秒刷新 RDS 指标缓存
	go h.refreshLoop()
	return h
}

func (h *DatabaseHandler) refreshLoop() {
	h.refresh()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		h.refresh()
	}
}

func (h *DatabaseHandler) refresh() {
	data := h.queryVM()
	h.mu.Lock()
	h.cache = data
	h.mu.Unlock()
}

type rdsInfo struct {
	HostID      string             `json:"host_id"`
	Name        string             `json:"name"`
	Engine      string             `json:"engine"`
	Spec        string             `json:"spec"`
	Endpoint    string             `json:"endpoint"`
	AccountID   int                `json:"account_id"`
	AccountName string             `json:"account_name"`
	Metrics     map[string]float64 `json:"metrics"`
}

func (h *DatabaseHandler) List(c *gin.Context) {
	h.mu.RLock()
	metricsMap := h.cache
	h.mu.RUnlock()

	// 从数据库加载已监控的 RDS 实例
	_, rdsList, err := h.cloudStore.LoadMonitoredInstances()
	if err != nil {
		log.Printf("[db-api] load monitored RDS error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load RDS instances"})
		return
	}

	// 构建 account_id -> account_name 映射
	accountNames := h.buildAccountNameMap(rdsList)

	var result []rdsInfo
	for _, inst := range rdsList {
		info := rdsInfo{
			HostID:      inst.HostID,
			Name:        inst.InstanceName,
			Engine:      inst.Engine,
			Spec:        inst.Spec,
			Endpoint:    inst.Endpoint,
			AccountID:   inst.CloudAccountID,
			AccountName: accountNames[inst.CloudAccountID],
			Metrics:     metricsMap[inst.HostID],
		}
		if info.Metrics == nil {
			info.Metrics = make(map[string]float64)
		}
		result = append(result, info)
	}
	// Resource-level permission filtering
	if ps := GetPermissionSet(c, h.permCache); ps != nil {
		filtered := result[:0]
		for _, d := range result {
			if ps.CanSeeDatabase(d.HostID) {
				filtered = append(filtered, d)
			}
		}
		result = filtered
	}
	if result == nil {
		result = []rdsInfo{}
	}
	c.JSON(http.StatusOK, result)
}

func (h *DatabaseHandler) Get(c *gin.Context) {
	hostID := c.Param("id")

	// 从数据库加载已监控的 RDS 实例，查找匹配的
	_, rdsList, err := h.cloudStore.LoadMonitoredInstances()
	if err != nil {
		log.Printf("[db-api] load monitored RDS error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load RDS instances"})
		return
	}

	var found *store.CloudInstance
	for i, inst := range rdsList {
		if inst.HostID == hostID {
			found = &rdsList[i]
			break
		}
	}
	if found == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "database not found"})
		return
	}
	if ps := GetPermissionSet(c, h.permCache); ps != nil && !ps.CanSeeDatabase(found.HostID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "insufficient permissions"})
		return
	}

	// 获取 account name
	accountNames := h.buildAccountNameMap([]store.CloudInstance{*found})

	h.mu.RLock()
	metrics := h.cache[hostID]
	h.mu.RUnlock()

	info := rdsInfo{
		HostID:      found.HostID,
		Name:        found.InstanceName,
		Engine:      found.Engine,
		Spec:        found.Spec,
		Endpoint:    found.Endpoint,
		AccountID:   found.CloudAccountID,
		AccountName: accountNames[found.CloudAccountID],
		Metrics:     metrics,
	}
	if info.Metrics == nil {
		info.Metrics = make(map[string]float64)
	}
	c.JSON(http.StatusOK, info)
}

// buildAccountNameMap 根据实例列表中的 account_id 构建 ID -> Name 映射
func (h *DatabaseHandler) buildAccountNameMap(instances []store.CloudInstance) map[int]string {
	names := make(map[int]string)
	if len(instances) == 0 {
		return names
	}
	// 收集唯一的 account IDs
	ids := make(map[int]bool)
	for _, inst := range instances {
		ids[inst.CloudAccountID] = true
	}
	// 逐个查询 account name
	for id := range ids {
		account, err := h.cloudStore.GetAccount(id)
		if err != nil {
			log.Printf("[db-api] get account %d error: %v", id, err)
			continue
		}
		names[id] = account.Name
	}
	return names
}

// DatabaseAlertTargets returns monitored RDS instances as hostID -> display
// label, for the alert engine. Implements alert.DatabaseProvider.
func (h *DatabaseHandler) DatabaseAlertTargets() map[string]string {
	_, rdsList, err := h.cloudStore.LoadMonitoredInstances()
	if err != nil {
		log.Printf("[db-api] alert targets: load monitored RDS error: %v", err)
		return nil
	}
	targets := make(map[string]string, len(rdsList))
	for _, inst := range rdsList {
		label := inst.InstanceName
		if label == "" {
			label = inst.HostID
		}
		targets[inst.HostID] = label
	}
	return targets
}

// DatabaseMetrics returns a copy of the latest cached metric snapshot for one
// RDS host. Implements alert.DatabaseProvider.
func (h *DatabaseHandler) DatabaseMetrics(hostID string) map[string]float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	src := h.cache[hostID]
	if src == nil {
		return nil
	}
	cp := make(map[string]float64, len(src))
	for k, v := range src {
		cp[k] = v
	}
	return cp
}

func (h *DatabaseHandler) queryVM() map[string]map[string]float64 {
	result := make(map[string]map[string]float64)

	data, err := h.vm.Query(`{__name__=~"mantisops_rds_.*"}`)
	if err != nil {
		log.Printf("[db-api] vm query error: %v", err)
		return result
	}

	var vmResp struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &vmResp); err != nil {
		return result
	}

	const prefix = "mantisops_rds_"
	for _, r := range vmResp.Data.Result {
		if len(r.Value) < 2 {
			continue
		}
		hostID := r.Metric["host_id"]
		metricName := r.Metric["__name__"]
		short := metricName
		if len(metricName) > len(prefix) {
			short = metricName[len(prefix):]
		}
		if result[hostID] == nil {
			result[hostID] = make(map[string]float64)
		}
		if valStr, ok := r.Value[1].(string); ok {
			if v, err := strconv.ParseFloat(valStr, 64); err == nil {
				result[hostID][short] = v
			}
		}
	}
	return result
}
