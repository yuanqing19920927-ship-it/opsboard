import { useEffect, useState, useCallback, type ReactNode } from 'react'
import {
  getAlertRules, createAlertRule, updateAlertRule, deleteAlertRule,
  getAlertEvents, getAlertStats, ackAlertEvent, getEventNotifications,
  getAlertChannels, createAlertChannel, updateAlertChannel, deleteAlertChannel, testAlertChannel,
} from '../../api/alert'
import { getNasDevices } from '../../api/nas'
import type { NasDevice } from '../../api/nas'
import { getDatabases, type RDSInfo } from '../../api/client'
import type { AlertRule, AlertEvent, AlertStats, NotificationChannel, AlertNotificationDetail } from '../../types'
import { useAuthStore } from '../../stores/authStore'

// ── Constants ──────────────────────────────────────────────

const RULE_TYPE_OPTIONS: { value: string; label: string }[] = [
  { value: 'server_offline', label: '服务器离线' },
  { value: 'probe_down', label: '端口异常' },
  { value: 'cpu', label: 'CPU 使用率' },
  { value: 'memory', label: '内存使用率' },
  { value: 'disk', label: '磁盘使用率' },
  { value: 'container', label: '容器异常' },
  { value: 'gpu_temp', label: 'GPU 温度' },
  { value: 'gpu_memory', label: 'GPU 显存' },
  { value: 'network_rx', label: '网络入站' },
  { value: 'network_tx', label: '网络出站' },
  { value: 'db_cpu', label: '数据库 CPU 使用率' },
  { value: 'db_memory', label: '数据库内存使用率' },
  { value: 'db_disk', label: '数据库磁盘使用率' },
  { value: 'db_connection', label: '数据库连接数使用率' },
  { value: 'db_iops', label: '数据库 IOPS 使用率' },
  { value: 'nas_offline', label: 'NAS 离线' },
  { value: 'nas_raid_degraded', label: 'RAID 降级' },
  { value: 'nas_disk_smart', label: '硬盘 S.M.A.R.T. 异常' },
  { value: 'nas_disk_temperature', label: '硬盘温度过高' },
  { value: 'nas_volume_usage', label: '存储卷空间不足' },
  { value: 'nas_ups_battery', label: 'UPS 电池供电' },
]

const NAS_TYPE_VALUES = new Set([
  'nas_offline', 'nas_raid_degraded', 'nas_disk_smart',
  'nas_disk_temperature', 'nas_volume_usage', 'nas_ups_battery',
])

// NAS types that are boolean conditions — no threshold needed
const NAS_BOOLEAN_TYPES = new Set([
  'nas_offline', 'nas_raid_degraded', 'nas_disk_smart', 'nas_ups_battery',
])

const NAS_TYPE_DEFAULTS: Record<string, { threshold: number; unit: string }> = {
  nas_disk_temperature: { threshold: 55, unit: '°C' },
  nas_volume_usage: { threshold: 90, unit: '%' },
}

function isNasType(type: string): boolean {
  return NAS_TYPE_VALUES.has(type)
}

// Database (RDS) alert types — all percentage metrics with a threshold
const DB_TYPE_VALUES = new Set([
  'db_cpu', 'db_memory', 'db_disk', 'db_connection', 'db_iops',
])

const DB_TYPE_DEFAULTS: Record<string, { threshold: number; unit: string }> = {
  db_cpu: { threshold: 80, unit: '%' },
  db_memory: { threshold: 80, unit: '%' },
  db_disk: { threshold: 85, unit: '%' },
  db_connection: { threshold: 80, unit: '%' },
  db_iops: { threshold: 80, unit: '%' },
}

function isDbType(type: string): boolean {
  return DB_TYPE_VALUES.has(type)
}

const OPERATOR_OPTIONS = ['>', '>=', '<', '<=', '==', '!=']

const LEVEL_OPTIONS: { value: string; label: string; color: string }[] = [
  { value: 'critical', label: '严重', color: 'text-error' },
  { value: 'warning', label: '警告', color: 'text-warning' },
  { value: 'info', label: '通知', color: 'text-primary' },
]

const RESOLVE_TYPE_LABELS: Record<string, string> = {
  auto: '自动恢复',
  target_gone: '目标消失',
  rule_disabled: '规则禁用',
  rule_deleted: '规则删除',
}

const LEVEL_EMOJI: Record<string, string> = {
  critical: '🔴',
  warning: '🟡',
  info: '🔵',
}

const CHANNEL_TYPE_OPTIONS = [
  { value: 'dingtalk', label: '钉钉机器人' },
  { value: 'webhook', label: 'Webhook' },
]

// ── Helper functions ───────────────────────────────────────

function formatTime(iso?: string): string {
  if (!iso) return '--'
  const d = new Date(iso)
  return d.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function ruleTypeLabel(type: string): string {
  return RULE_TYPE_OPTIONS.find((o) => o.value === type)?.label ?? type
}

function maskUrl(url: string): string {
  try {
    const u = new URL(url)
    return `${u.protocol}//${u.host}/***`
  } catch {
    return url.length > 30 ? url.slice(0, 30) + '...' : url
  }
}

// ── Shared form styles ─────────────────────────────────────

const inputClass =
  'w-full border border-[#e9ecef] rounded-[6px] px-3 py-2 text-sm text-[#495057] placeholder:text-[#adb5bd] focus:outline-none focus:border-[#2ca07a] focus:ring-2 focus:ring-[#2ca07a]/20 bg-white transition-colors'
const selectClass =
  'w-full border border-[#e9ecef] rounded-[6px] px-3 py-2 text-sm text-[#495057] focus:outline-none focus:border-[#2ca07a] focus:ring-2 focus:ring-[#2ca07a]/20 bg-white transition-colors appearance-none'
const fieldLabelClass = 'block text-[12px] font-medium text-[#878a99] mb-1'

// ── Toggle Switch ──────────────────────────────────────────

function ToggleSwitch({ enabled, onChange }: { enabled: boolean; onChange: () => void }) {
  return (
    <button
      onClick={onChange}
      className={`relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus:outline-none ${
        enabled ? 'bg-[#2ca07a]' : 'bg-[#ced4da]'
      }`}
    >
      <span
        className={`inline-block h-3.5 w-3.5 rounded-full bg-white shadow transition-transform ${
          enabled ? 'translate-x-4.5' : 'translate-x-0.5'
        }`}
      />
    </button>
  )
}

// Labeled form field cell. `label` shows a caption above the control so the
// user knows what each input means. Pass label="" for a button cell — an
// invisible caption keeps it bottom-aligned with the labeled fields.
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <label className={label ? fieldLabelClass : `${fieldLabelClass} invisible`} aria-hidden={label ? undefined : true}>
        {label || ' '}
      </label>
      {children}
    </div>
  )
}

// ── Component ──────────────────────────────────────────────

export default function Alerts() {
  const role = useAuthStore((s) => s.role)
  const canEdit = role === 'admin' || role === 'operator'

  // Tab state
  const [tab, setTab] = useState<'events' | 'rules' | 'channels'>('events')

  // Stats
  const [stats, setStats] = useState<AlertStats | null>(null)

  // Events state
  const [events, setEvents] = useState<AlertEvent[]>([])
  const [eventFilter, setEventFilter] = useState('all')

  // Rules state
  const [rules, setRules] = useState<AlertRule[]>([])
  const [showRuleForm, setShowRuleForm] = useState(false)
  const [ruleForm, setRuleForm] = useState({
    name: '', type: 'cpu', target_id: '', operator: '>', threshold: 90, unit: '%', duration: 60, level: 'warning',
  })
  const [nasDevices, setNasDevices] = useState<NasDevice[]>([])
  const [nasDevicesLoaded, setNasDevicesLoaded] = useState(false)
  const [databases, setDatabases] = useState<RDSInfo[]>([])
  const [databasesLoaded, setDatabasesLoaded] = useState(false)

  // Channels state
  const [channels, setChannels] = useState<NotificationChannel[]>([])
  const [showChannelForm, setShowChannelForm] = useState(false)
  const [channelForm, setChannelForm] = useState({ name: '', type: 'dingtalk', url: '', secret: '' })

  // Expanded event for notification details
  const [expandedEvent, setExpandedEvent] = useState<number | null>(null)
  const [notifications, setNotifications] = useState<AlertNotificationDetail[]>([])

  // Toast
  const [toast, setToast] = useState<{ msg: string; ok: boolean } | null>(null)

  const showToast = (msg: string, ok: boolean) => {
    setToast({ msg, ok })
    setTimeout(() => setToast(null), 3000)
  }

  // ── Data loading ───────────────────────────────────────

  const loadStats = useCallback(async () => {
    try { setStats(await getAlertStats()) } catch { /* ignore */ }
  }, [])

  const loadEvents = useCallback(async () => {
    try {
      const params: Record<string, string> = {}
      if (eventFilter === 'firing') params.status = 'firing'
      else if (eventFilter === 'resolved') params.status = 'resolved'
      else if (eventFilter === 'silenced') params.silenced = 'true'
      setEvents(await getAlertEvents(params))
    } catch { /* ignore */ }
  }, [eventFilter])

  const loadRules = useCallback(async () => {
    try { setRules(await getAlertRules()) } catch { /* ignore */ }
  }, [])

  const loadChannels = useCallback(async () => {
    try { setChannels(await getAlertChannels()) } catch { /* ignore */ }
  }, [])

  const loadNasDevices = useCallback(async () => {
    if (nasDevicesLoaded) return
    try {
      setNasDevices(await getNasDevices())
      setNasDevicesLoaded(true)
    } catch { /* ignore */ }
  }, [nasDevicesLoaded])

  const loadDatabases = useCallback(async () => {
    if (databasesLoaded) return
    try {
      setDatabases(await getDatabases())
      setDatabasesLoaded(true)
    } catch { /* ignore */ }
  }, [databasesLoaded])

  useEffect(() => { loadStats() }, [loadStats])

  useEffect(() => {
    if (tab === 'events') loadEvents()
    else if (tab === 'rules') loadRules()
    else if (tab === 'channels') loadChannels()
  }, [tab, loadEvents, loadRules, loadChannels])

  // Auto-refresh events every 15s
  useEffect(() => {
    if (tab !== 'events') return
    const timer = setInterval(() => { loadEvents(); loadStats() }, 15000)
    return () => clearInterval(timer)
  }, [tab, loadEvents, loadStats])

  // ── Event handlers ─────────────────────────────────────

  const handleAck = async (id: number) => {
    try {
      await ackAlertEvent(id)
      loadEvents()
      loadStats()
    } catch {
      showToast('确认失败', false)
    }
  }

  const handleExpandEvent = async (id: number) => {
    if (expandedEvent === id) {
      setExpandedEvent(null)
      return
    }
    setExpandedEvent(id)
    try {
      setNotifications(await getEventNotifications(id))
    } catch {
      setNotifications([])
    }
  }

  // ── Rule handlers ──────────────────────────────────────

  const handleCreateRule = async () => {
    if (!ruleForm.name) return
    try {
      await createAlertRule({
        name: ruleForm.name,
        type: ruleForm.type,
        target_id: ruleForm.target_id,
        operator: ruleForm.operator,
        threshold: Number(ruleForm.threshold),
        unit: ruleForm.unit,
        duration: Number(ruleForm.duration),
        level: ruleForm.level,
        enabled: true,
      })
      setRuleForm({ name: '', type: 'cpu', target_id: '', operator: '>', threshold: 90, unit: '%', duration: 60, level: 'warning' })
      setShowRuleForm(false)
      loadRules()
      showToast('规则创建成功', true)
    } catch {
      showToast('创建失败', false)
    }
  }

  const handleToggleRule = async (rule: AlertRule) => {
    try {
      await updateAlertRule(rule.id!, { enabled: !rule.enabled })
      loadRules()
    } catch {
      showToast('更新失败', false)
    }
  }

  const handleDeleteRule = async (id: number) => {
    if (!window.confirm('确定要删除此告警规则吗？')) return
    try {
      await deleteAlertRule(id)
      loadRules()
      showToast('规则已删除', true)
    } catch {
      showToast('删除失败', false)
    }
  }

  // ── Channel handlers ───────────────────────────────────

  const handleCreateChannel = async () => {
    if (!channelForm.name || !channelForm.url) return
    const config: Record<string, string> = { url: channelForm.url }
    if (channelForm.type === 'dingtalk' && channelForm.secret) config.secret = channelForm.secret
    try {
      await createAlertChannel({
        name: channelForm.name,
        type: channelForm.type,
        config: JSON.stringify(config),
        enabled: true,
      })
      setChannelForm({ name: '', type: 'dingtalk', url: '', secret: '' })
      setShowChannelForm(false)
      loadChannels()
      showToast('渠道创建成功', true)
    } catch {
      showToast('创建失败', false)
    }
  }

  const handleToggleChannel = async (ch: NotificationChannel) => {
    try {
      await updateAlertChannel(ch.id!, { enabled: !ch.enabled })
      loadChannels()
    } catch {
      showToast('更新失败', false)
    }
  }

  const handleDeleteChannel = async (id: number) => {
    if (!window.confirm('确定要删除此通知渠道吗？')) return
    try {
      await deleteAlertChannel(id)
      loadChannels()
      showToast('渠道已删除', true)
    } catch {
      showToast('删除失败', false)
    }
  }

  const handleTestChannel = async (id: number) => {
    try {
      await testAlertChannel(id)
      showToast('测试消息已发送', true)
    } catch {
      showToast('测试失败，请检查配置', false)
    }
  }

  const parseChannelConfig = (configStr: string): Record<string, string> => {
    try { return JSON.parse(configStr) } catch { return {} }
  }

  // ── Render ─────────────────────────────────────────────

  return (
    <div className="p-6">
      {/* Toast */}
      {toast && (
        <div className={`fixed top-6 right-6 z-50 px-5 py-3 rounded-[8px] text-sm font-medium shadow-lg transition-all ${
          toast.ok ? 'bg-[#0ab39c] text-white' : 'bg-[#f06548] text-white'
        }`}>
          {toast.msg}
        </div>
      )}

      {/* Header */}
      <div className="mb-6">
        <h4 className="text-[#495057] text-[18px] font-semibold mb-1">告警中心</h4>
        <p className="text-[#878a99] text-sm">Alerts — 告警事件监控、规则管理与通知渠道配置</p>
      </div>

      {/* Stats Row */}
      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
        {/* Firing */}
        <div className="bg-white rounded-[10px] shadow-sm p-5 border-0 border-b-4 border-b-[#f06548] relative overflow-hidden">
          <div className="flex items-center justify-between mb-3">
            <div>
              <p className="text-[#878a99] text-[13px] mb-1">当前触发中</p>
              <h4 className="text-[28px] font-bold text-[#495057]">{stats?.firing ?? 0}</h4>
            </div>
            <div className="w-12 h-12 rounded-full bg-[#f06548]/10 flex items-center justify-center">
              <span className="material-symbols-outlined text-[#f06548] text-xl">warning</span>
            </div>
          </div>
          <p className="text-[#878a99] text-[12px]">
            <span className="text-[#f06548] font-medium">活跃告警</span>
          </p>
        </div>

        {/* Today fired */}
        <div className="bg-white rounded-[10px] shadow-sm p-5 border-0 relative overflow-hidden">
          <div className="flex items-center justify-between mb-3">
            <div>
              <p className="text-[#878a99] text-[13px] mb-1">今日触发</p>
              <h4 className="text-[28px] font-bold text-[#495057]">{stats?.today_fired ?? 0}</h4>
            </div>
            <div className="w-12 h-12 rounded-full bg-[#f7b84b]/10 flex items-center justify-center">
              <span className="material-symbols-outlined text-[#f7b84b] text-xl">notification_add</span>
            </div>
          </div>
          <p className="text-[#878a99] text-[12px]">今日累计</p>
        </div>

        {/* Today resolved */}
        <div className="bg-white rounded-[10px] shadow-sm p-5 border-0 relative overflow-hidden">
          <div className="flex items-center justify-between mb-3">
            <div>
              <p className="text-[#878a99] text-[13px] mb-1">今日恢复</p>
              <h4 className="text-[28px] font-bold text-[#495057]">{stats?.today_resolved ?? 0}</h4>
            </div>
            <div className="w-12 h-12 rounded-full bg-[#0ab39c]/10 flex items-center justify-center">
              <span className="material-symbols-outlined text-[#0ab39c] text-xl">check_circle</span>
            </div>
          </div>
          <p className="text-[#878a99] text-[12px]">
            <span className="text-[#0ab39c] font-medium">已自动恢复</span>
          </p>
        </div>

        {/* Today silenced */}
        <div className="bg-white rounded-[10px] shadow-sm p-5 border-0 relative overflow-hidden">
          <div className="flex items-center justify-between mb-3">
            <div>
              <p className="text-[#878a99] text-[13px] mb-1">今日确认</p>
              <h4 className="text-[28px] font-bold text-[#495057]">{stats?.today_silenced ?? 0}</h4>
            </div>
            <div className="w-12 h-12 rounded-full bg-[#f7b84b]/10 flex items-center justify-center">
              <span className="material-symbols-outlined text-[#f7b84b] text-xl">done_all</span>
            </div>
          </div>
          <p className="text-[#878a99] text-[12px]">人工确认处理</p>
        </div>
      </div>

      {/* Tabs Card */}
      <div className="bg-white rounded-[10px] shadow-sm">
        {/* Tab navigation */}
        <div className="border-b-2 border-[#e9ecef] px-4">
          <nav className="flex gap-0 -mb-[2px]">
            {([
              { key: 'events' as const, label: '告警事件', count: stats?.firing ?? null },
              { key: 'rules' as const, label: '告警规则', count: rules.length || null },
              ...(canEdit ? [{ key: 'channels' as const, label: '通知渠道', count: channels.length || null }] : []),
            ]).map((t) => (
              <button
                key={t.key}
                onClick={() => setTab(t.key)}
                className={`px-4 py-3 text-sm font-medium border-b-2 transition-colors whitespace-nowrap flex items-center gap-1.5 ${
                  tab === t.key
                    ? 'border-b-[#2ca07a] text-[#2ca07a]'
                    : 'border-b-transparent text-[#878a99] hover:text-[#495057]'
                }`}
              >
                {t.label}
                {t.count !== null && (
                  <span className={`text-[11px] px-1.5 py-0.5 rounded-full font-semibold ${
                    tab === t.key ? 'bg-[#2ca07a]/15 text-[#2ca07a]' : 'bg-[#e9ecef] text-[#878a99]'
                  }`}>
                    {t.count}
                  </span>
                )}
              </button>
            ))}
          </nav>
        </div>

        {/* Tab content */}
        <div className="p-5">

          {/* ━━━ Tab 1: 告警事件 ━━━ */}
          {tab === 'events' && (
            <div>
              {/* Filter btn-group */}
              <div className="flex items-center justify-between mb-5 flex-wrap gap-3">
                <div className="inline-flex rounded-[6px] border border-[#e9ecef] overflow-hidden">
                  {([
                    { key: 'all', label: '全部' },
                    { key: 'firing', label: '触发中' },
                    { key: 'resolved', label: '已恢复' },
                    { key: 'silenced', label: '已静默' },
                  ]).map((f, idx) => (
                    <button
                      key={f.key}
                      onClick={() => setEventFilter(f.key)}
                      className={`px-4 py-1.5 text-[13px] font-medium transition-colors ${
                        idx > 0 ? 'border-l border-[#e9ecef]' : ''
                      } ${
                        eventFilter === f.key
                          ? 'bg-[#2ca07a] text-white'
                          : 'bg-white text-[#878a99] hover:bg-[#f8f9fa]'
                      }`}
                    >
                      {f.label}
                    </button>
                  ))}
                </div>
                <button className="flex items-center gap-1.5 px-3 py-1.5 text-[13px] text-[#878a99] border border-[#e9ecef] rounded-[6px] bg-white hover:bg-[#f8f9fa] transition-colors">
                  <span className="material-symbols-outlined text-base">filter_list</span>
                  筛选
                </button>
              </div>

              {/* Events table */}
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-[#e9ecef]">
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">级别</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">告警名称</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide hidden md:table-cell">目标</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide hidden lg:table-cell">触发值</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">状态</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide hidden sm:table-cell">触发时间</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {events.map((ev) => {
                      const isFiring = ev.status === 'firing'
                      const isSilenced = ev.silenced
                      const isExpanded = expandedEvent === ev.id

                      let rowBorderClass = ''
                      let rowBgClass = ''
                      if (isFiring && !isSilenced) {
                        rowBorderClass = 'border-l-[3px] border-l-[#f06548]'
                        rowBgClass = 'bg-[#f06548]/[0.04]'
                      } else if (isFiring && isSilenced) {
                        rowBorderClass = 'border-l-[3px] border-l-[#f7b84b]'
                        rowBgClass = 'bg-[#f7b84b]/[0.04]'
                      } else if (ev.status === 'resolved') {
                        rowBorderClass = 'border-l-[3px] border-l-[#0ab39c]'
                        rowBgClass = ''
                      }

                      return (
                        <tr key={ev.id} className={`border-b border-[#f3f3f9] ${rowBorderClass} ${rowBgClass} cursor-pointer hover:bg-[#f8f9fa] transition-colors`}
                          onClick={() => handleExpandEvent(ev.id)}>
                          <td colSpan={7} className="p-0">
                            <div className="flex flex-col">
                              {/* Main row */}
                              <div className="flex items-center">
                                <div className="px-4 py-3 w-14 flex-shrink-0 text-base">{LEVEL_EMOJI[ev.level] ?? '⚪'}</div>
                                <div className="px-4 py-3 flex-1 min-w-0">
                                  <span className="text-[#495057] font-semibold text-[13px] truncate block">{ev.rule_name}</span>
                                </div>
                                <div className="px-4 py-3 flex-1 min-w-0 hidden md:block">
                                  <span className="text-[11px] font-mono bg-[#f3f3f9] text-[#495057] px-2 py-0.5 rounded truncate inline-block max-w-full">{ev.target_label}</span>
                                </div>
                                <div className="px-4 py-3 w-24 hidden lg:block">
                                  <span className="text-[#495057] text-[13px]">{ev.value}</span>
                                </div>
                                <div className="px-4 py-3 w-28">
                                  {isFiring && !isSilenced && (
                                    <span className="text-[11px] px-2 py-0.5 rounded-[4px] bg-[#f06548]/15 text-[#f06548] font-semibold">触发中</span>
                                  )}
                                  {isFiring && isSilenced && (
                                    <span className="text-[11px] px-2 py-0.5 rounded-[4px] bg-[#f7b84b]/15 text-[#f7b84b] font-semibold">已确认</span>
                                  )}
                                  {ev.status === 'resolved' && (
                                    <span className="text-[11px] px-2 py-0.5 rounded-[4px] bg-[#0ab39c]/15 text-[#0ab39c] font-semibold">已恢复</span>
                                  )}
                                </div>
                                <div className="px-4 py-3 w-36 hidden sm:block">
                                  <span className="text-[#878a99] text-[12px]">{formatTime(ev.fired_at)}</span>
                                </div>
                                <div className="px-4 py-3 w-24" onClick={(e) => e.stopPropagation()}>
                                  {isFiring && !isSilenced && (
                                    <button
                                      onClick={() => handleAck(ev.id)}
                                      className="text-[12px] px-3 py-1 rounded-[4px] bg-[#f7b84b]/15 text-[#f7b84b] hover:bg-[#f7b84b]/25 font-semibold transition-colors border border-[#f7b84b]/30"
                                    >
                                      确认
                                    </button>
                                  )}
                                </div>
                              </div>

                              {/* Silenced extra info */}
                              {isFiring && isSilenced && (
                                <div className="px-4 pb-2 text-[11px] text-[#878a99]">
                                  确认人: {ev.acked_by ?? '--'} | 确认时间: {formatTime(ev.acked_at)}
                                </div>
                              )}
                              {ev.status === 'resolved' && (
                                <div className="px-4 pb-2 text-[11px] text-[#878a99]">
                                  恢复时间: {formatTime(ev.resolved_at)} | 恢复方式: {RESOLVE_TYPE_LABELS[ev.resolve_type ?? ''] ?? ev.resolve_type ?? '--'}
                                </div>
                              )}

                              {/* Notification details (expanded) */}
                              {isExpanded && (
                                <div className="px-4 pb-4">
                                  <div className="bg-[#f8f9fa] rounded-[6px] p-3 mt-1 border border-[#e9ecef]">
                                    <div className="text-[11px] font-semibold text-[#878a99] uppercase tracking-wide mb-2">通知详情</div>
                                    {notifications.length === 0 ? (
                                      <div className="text-[12px] text-[#adb5bd]">暂无通知记录</div>
                                    ) : (
                                      <div className="space-y-1">
                                        {notifications.map((n, i) => (
                                          <div key={i} className="flex items-center gap-3 text-[12px]">
                                            <span className="text-[#495057] font-medium">{n.channel_name}</span>
                                            <span className="text-[#878a99]">{n.channel_type}</span>
                                            <span className={n.status === 'sent' ? 'text-[#0ab39c]' : 'text-[#f06548]'}>
                                              {n.status === 'sent' ? '已发送' : n.status === 'failed' ? '发送失败' : n.status}
                                            </span>
                                            {n.last_error && <span className="text-[#f06548]/70 truncate">{n.last_error}</span>}
                                            <span className="text-[#adb5bd] ml-auto">{formatTime(n.sent_at)}</span>
                                          </div>
                                        ))}
                                      </div>
                                    )}
                                  </div>
                                </div>
                              )}
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>

              {events.length === 0 && (
                <div className="text-center py-16">
                  <span className="material-symbols-outlined text-4xl text-[#ced4da] mb-3 block">notifications_off</span>
                  <p className="text-[#878a99] text-sm">暂无告警事件</p>
                </div>
              )}
            </div>
          )}

          {/* ━━━ Tab 2: 告警规则 ━━━ */}
          {tab === 'rules' && (
            <div>
              {/* Header with add button */}
              <div className="flex items-center justify-between mb-5">
                <span className="text-[#495057] text-[14px] font-medium">告警规则列表</span>
                <button
                  onClick={() => {
                    setShowRuleForm(!showRuleForm)
                    if (!showRuleForm && isNasType(ruleForm.type)) loadNasDevices()
                    if (!showRuleForm && isDbType(ruleForm.type)) loadDatabases()
                  }}
                  className="flex items-center gap-1.5 px-4 py-2 text-[13px] font-medium text-white bg-[#2ca07a] hover:bg-[#259068] rounded-[6px] transition-colors"
                >
                  <span className="material-symbols-outlined text-base">add</span>
                  添加规则
                </button>
              </div>

              {/* Rule form */}
              {showRuleForm && (
                <div className="bg-[#f8f9fa] rounded-[8px] p-5 mb-5 border border-[#e9ecef]">
                  <h6 className="text-[#495057] font-semibold mb-4">新建告警规则</h6>
                  <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3 mb-3">
                    <Field label="规则名称">
                      <input
                        placeholder="例如：生产库 CPU 过高"
                        value={ruleForm.name}
                        onChange={(e) => setRuleForm({ ...ruleForm, name: e.target.value })}
                        className={inputClass}
                      />
                    </Field>
                    <Field label="告警类型">
                    <select
                      value={ruleForm.type}
                      onChange={(e) => {
                        const newType = e.target.value
                        const defaults = NAS_TYPE_DEFAULTS[newType] ?? DB_TYPE_DEFAULTS[newType]
                        setRuleForm({
                          ...ruleForm,
                          type: newType,
                          target_id: '',
                          threshold: defaults?.threshold ?? ruleForm.threshold,
                          unit: defaults?.unit ?? ruleForm.unit,
                        })
                        if (isNasType(newType)) loadNasDevices()
                        if (isDbType(newType)) loadDatabases()
                      }}
                      className={selectClass}
                    >
                      <optgroup label="服务器 / 探针">
                        {RULE_TYPE_OPTIONS.filter((o) => !isNasType(o.value) && !isDbType(o.value)).map((o) => (
                          <option key={o.value} value={o.value}>{o.label}</option>
                        ))}
                      </optgroup>
                      <optgroup label="数据库">
                        {RULE_TYPE_OPTIONS.filter((o) => isDbType(o.value)).map((o) => (
                          <option key={o.value} value={o.value}>{o.label}</option>
                        ))}
                      </optgroup>
                      <optgroup label="NAS">
                        {RULE_TYPE_OPTIONS.filter((o) => isNasType(o.value)).map((o) => (
                          <option key={o.value} value={o.value}>{o.label}</option>
                        ))}
                      </optgroup>
                    </select>
                    </Field>
                    <Field label="监控目标">
                    {isNasType(ruleForm.type) ? (
                      <select
                        value={ruleForm.target_id}
                        onChange={(e) => setRuleForm({ ...ruleForm, target_id: e.target.value })}
                        className={selectClass}
                      >
                        <option value="">所有 NAS</option>
                        {nasDevices.map((d) => (
                          <option key={d.id} value={`nas:${d.id}`}>{d.name} ({d.host})</option>
                        ))}
                      </select>
                    ) : isDbType(ruleForm.type) ? (
                      <select
                        value={ruleForm.target_id}
                        onChange={(e) => setRuleForm({ ...ruleForm, target_id: e.target.value })}
                        className={selectClass}
                      >
                        <option value="">所有数据库</option>
                        {databases.map((d) => (
                          <option key={d.host_id} value={`db:${d.host_id}`}>{d.name} ({d.engine})</option>
                        ))}
                      </select>
                    ) : (
                      <input
                        placeholder="留空=全部；或填 host_id"
                        value={ruleForm.target_id}
                        onChange={(e) => setRuleForm({ ...ruleForm, target_id: e.target.value })}
                        className={inputClass}
                      />
                    )}
                    </Field>
                    <Field label="比较运算符">
                    <select
                      value={ruleForm.operator}
                      onChange={(e) => setRuleForm({ ...ruleForm, operator: e.target.value })}
                      className={selectClass}
                    >
                      {OPERATOR_OPTIONS.map((o) => (
                        <option key={o} value={o}>{o}</option>
                      ))}
                    </select>
                    </Field>
                  </div>
                  <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
                    {!(isNasType(ruleForm.type) && NAS_BOOLEAN_TYPES.has(ruleForm.type)) && (
                      <Field label={`阈值${ruleForm.unit ? `（${ruleForm.unit}）` : ''}`}>
                        <input
                          type="number"
                          placeholder="如 90"
                          value={ruleForm.threshold}
                          onChange={(e) => setRuleForm({ ...ruleForm, threshold: Number(e.target.value) })}
                          className={inputClass}
                        />
                      </Field>
                    )}
                    <Field label="持续时间（秒）">
                      <input
                        type="number"
                        placeholder="连续超阈值多少秒后触发，如 60"
                        value={ruleForm.duration}
                        onChange={(e) => setRuleForm({ ...ruleForm, duration: Number(e.target.value) })}
                        className={inputClass}
                      />
                    </Field>
                    <Field label="告警级别">
                    <select
                      value={ruleForm.level}
                      onChange={(e) => setRuleForm({ ...ruleForm, level: e.target.value })}
                      className={selectClass}
                    >
                      {LEVEL_OPTIONS.map((o) => (
                        <option key={o.value} value={o.value}>{o.label}</option>
                      ))}
                    </select>
                    </Field>
                    <Field label="">
                    <div className="flex gap-2">
                      <button
                        onClick={handleCreateRule}
                        className="flex-1 px-4 py-2 text-[13px] font-medium text-white bg-[#2ca07a] hover:bg-[#259068] rounded-[6px] transition-colors"
                      >
                        保存
                      </button>
                      <button
                        onClick={() => setShowRuleForm(false)}
                        className="px-4 py-2 text-[13px] text-[#878a99] bg-white border border-[#e9ecef] hover:bg-[#f8f9fa] rounded-[6px] transition-colors"
                      >
                        取消
                      </button>
                    </div>
                    </Field>
                  </div>
                </div>
              )}

              {/* Rules table */}
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="border-b border-[#e9ecef]">
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">名称</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">类型</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide hidden md:table-cell">目标</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide hidden sm:table-cell">条件</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide hidden lg:table-cell">持续</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">级别</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">启用</th>
                      <th className="text-left px-4 py-3 text-[#878a99] font-medium text-[12px] uppercase tracking-wide">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rules.map((rule) => (
                      <tr key={rule.id} className="border-b border-[#f3f3f9] hover:bg-[#f8f9fa] transition-colors group">
                        <td className="px-4 py-3 text-[#495057] font-semibold text-[13px]">{rule.name}</td>
                        <td className="px-4 py-3">
                          <span className="text-[11px] px-2 py-0.5 rounded-[4px] bg-[#2ca07a]/10 text-[#2ca07a] font-medium">
                            {ruleTypeLabel(rule.type)}
                          </span>
                        </td>
                        <td className="px-4 py-3 hidden md:table-cell">
                          <span className="text-[11px] font-mono bg-[#f3f3f9] text-[#495057] px-2 py-0.5 rounded">{rule.target_id || '全部'}</span>
                        </td>
                        <td className="px-4 py-3 hidden sm:table-cell">
                          <span className="font-mono text-[12px] text-[#495057]">{rule.operator} {rule.threshold}{rule.unit}</span>
                        </td>
                        <td className="px-4 py-3 text-[#878a99] text-[13px] hidden lg:table-cell">{rule.duration}s</td>
                        <td className="px-4 py-3">
                          {rule.level === 'critical' && (
                            <span className="text-[11px] px-2 py-0.5 rounded-[4px] bg-[#f06548]/15 text-[#f06548] font-semibold">严重</span>
                          )}
                          {rule.level === 'warning' && (
                            <span className="text-[11px] px-2 py-0.5 rounded-[4px] bg-[#f7b84b]/15 text-[#f7b84b] font-semibold">警告</span>
                          )}
                          {rule.level === 'info' && (
                            <span className="text-[11px] px-2 py-0.5 rounded-[4px] bg-[#2ca07a]/15 text-[#2ca07a] font-semibold">通知</span>
                          )}
                        </td>
                        <td className="px-4 py-3">
                          <ToggleSwitch enabled={!!rule.enabled} onChange={() => handleToggleRule(rule)} />
                        </td>
                        <td className="px-4 py-3">
                          <div className="flex items-center gap-1">
                            <button
                              onClick={() => handleDeleteRule(rule.id!)}
                              className="sm:opacity-0 sm:group-hover:opacity-100 transition-opacity p-1.5 rounded-[4px] text-[#878a99] hover:text-[#f06548] hover:bg-[#f06548]/10"
                              title="删除"
                            >
                              <span className="material-symbols-outlined text-[16px]">delete</span>
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {rules.length === 0 && (
                <div className="text-center py-16">
                  <span className="material-symbols-outlined text-4xl text-[#ced4da] mb-3 block">rule</span>
                  <p className="text-[#878a99] text-sm">暂无告警规则</p>
                  <p className="text-[#adb5bd] text-xs mt-1">点击「添加规则」开始配置</p>
                </div>
              )}
            </div>
          )}

          {/* ━━━ Tab 3: 通知渠道 ━━━ */}
          {tab === 'channels' && (
            <div>
              {/* Header with add button */}
              <div className="flex items-center justify-between mb-5">
                <span className="text-[#495057] text-[14px] font-medium">通知渠道管理</span>
                <button
                  onClick={() => setShowChannelForm(!showChannelForm)}
                  className="flex items-center gap-1.5 px-4 py-2 text-[13px] font-medium text-white bg-[#2ca07a] hover:bg-[#259068] rounded-[6px] transition-colors"
                >
                  <span className="material-symbols-outlined text-base">add</span>
                  添加渠道
                </button>
              </div>

              {/* Channel form */}
              {showChannelForm && (
                <div className="bg-[#f8f9fa] rounded-[8px] p-5 mb-5 border border-[#e9ecef]">
                  <h6 className="text-[#495057] font-semibold mb-4">新建通知渠道</h6>
                  <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
                    <input
                      placeholder="渠道名称"
                      value={channelForm.name}
                      onChange={(e) => setChannelForm({ ...channelForm, name: e.target.value })}
                      className={inputClass}
                    />
                    <select
                      value={channelForm.type}
                      onChange={(e) => setChannelForm({ ...channelForm, type: e.target.value })}
                      className={selectClass}
                    >
                      {CHANNEL_TYPE_OPTIONS.map((o) => (
                        <option key={o.value} value={o.value}>{o.label}</option>
                      ))}
                    </select>
                    <input
                      placeholder="Webhook URL"
                      value={channelForm.url}
                      onChange={(e) => setChannelForm({ ...channelForm, url: e.target.value })}
                      className={inputClass}
                    />
                    {channelForm.type === 'dingtalk' && (
                      <input
                        placeholder="Secret（加签密钥，可选）"
                        value={channelForm.secret}
                        onChange={(e) => setChannelForm({ ...channelForm, secret: e.target.value })}
                        className={inputClass}
                      />
                    )}
                  </div>
                  <div className="flex gap-2 mt-4">
                    <button
                      onClick={handleCreateChannel}
                      className="px-5 py-2 text-[13px] font-medium text-white bg-[#2ca07a] hover:bg-[#259068] rounded-[6px] transition-colors"
                    >
                      保存
                    </button>
                    <button
                      onClick={() => setShowChannelForm(false)}
                      className="px-4 py-2 text-[13px] text-[#878a99] bg-white border border-[#e9ecef] hover:bg-[#f8f9fa] rounded-[6px] transition-colors"
                    >
                      取消
                    </button>
                  </div>
                </div>
              )}

              {/* Channel cards grid */}
              <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
                {channels.map((ch) => {
                  const config = parseChannelConfig(ch.config)
                  return (
                    <div key={ch.id} className="bg-white rounded-[10px] border border-[#e9ecef] p-5 hover:shadow-md transition-shadow group flex flex-col">
                      {/* Card header */}
                      <div className="flex items-center justify-between mb-3">
                        <div className="flex items-center gap-3">
                          <div className="w-10 h-10 rounded-full bg-[#2ca07a]/10 flex items-center justify-center flex-shrink-0">
                            <span className="material-symbols-outlined text-[#2ca07a] text-lg">
                              {ch.type === 'dingtalk' ? 'chat' : 'webhook'}
                            </span>
                          </div>
                          <div>
                            <div className="text-[#495057] font-semibold text-[13px]">{ch.name}</div>
                            <span className="text-[11px] px-1.5 py-0.5 rounded-[3px] bg-[#2ca07a]/10 text-[#2ca07a] font-medium">
                              {CHANNEL_TYPE_OPTIONS.find((o) => o.value === ch.type)?.label ?? ch.type}
                            </span>
                          </div>
                        </div>
                        <ToggleSwitch enabled={!!ch.enabled} onChange={() => handleToggleChannel(ch)} />
                      </div>

                      {/* Masked URL */}
                      <div className="bg-[#f8f9fa] rounded-[6px] px-3 py-2.5 mb-4 border border-[#e9ecef]">
                        <div className="text-[10px] text-[#878a99] mb-0.5 uppercase tracking-wide">Webhook URL</div>
                        <div className="text-[11px] font-mono text-[#495057] truncate">{maskUrl(config.url ?? '')}</div>
                      </div>

                      <div className="flex-1" />

                      {/* Action buttons */}
                      <div className="flex items-center gap-2 pt-3 border-t border-[#f3f3f9]">
                        <button
                          onClick={() => handleTestChannel(ch.id!)}
                          className="flex items-center gap-1 px-3 py-1.5 text-[12px] font-medium text-[#0ab39c] bg-[#0ab39c]/10 hover:bg-[#0ab39c]/20 rounded-[5px] transition-colors"
                        >
                          <span className="material-symbols-outlined text-[14px]">send</span>
                          测试
                        </button>
                        <button
                          onClick={() => handleDeleteChannel(ch.id!)}
                          className="sm:opacity-0 sm:group-hover:opacity-100 ml-auto transition-opacity p-1.5 rounded-[5px] text-[#878a99] hover:text-[#f06548] hover:bg-[#f06548]/10"
                          title="删除渠道"
                        >
                          <span className="material-symbols-outlined text-[16px]">delete</span>
                        </button>
                      </div>
                    </div>
                  )
                })}
              </div>

              {channels.length === 0 && !showChannelForm && (
                <div className="text-center py-16">
                  <span className="material-symbols-outlined text-4xl text-[#ced4da] mb-3 block">send</span>
                  <p className="text-[#878a99] text-sm">暂无通知渠道</p>
                  <p className="text-[#adb5bd] text-xs mt-1">点击「添加渠道」配置告警通知</p>
                </div>
              )}
            </div>
          )}

        </div>
      </div>
    </div>
  )
}
