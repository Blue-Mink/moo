import React, { useState, useEffect, useCallback } from 'react';
import {
  fetchNotifySettings, updateNotifySettings,
  fetchNotifyLog, clearNotifyLog,
  fetchNotifyChannels, addNotifyChannel, updateNotifyChannel, deleteNotifyChannel, testNotifyChannel, testNotifyChannelDraft,
  fireNotifyEvent,
  type NotifySettings, type NotifyLogEntry, type NotifyChannel, type ChannelDef, type ChannelFieldDef,
} from '../api/client';
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import { Bell, BellRing, ChevronDown, Loader2, Plus, RefreshCw, Send, Settings2, Trash2, Check } from 'lucide-react';
import { toast } from 'sonner';
import { cn } from "@/lib/utils";

// ── 通知设置 tab（0.6.121，形式对齐 fn-knock 事件中心：推送渠道/通知规则/通知记录）──

type NotifySubTab = 'channel' | 'rule' | 'record';

const NOTIFY_SUB_TABS: { key: NotifySubTab; label: string }[] = [
  { key: 'channel', label: '推送渠道' },
  { key: 'rule', label: '通知规则' },
  { key: 'record', label: '通知记录' },
];

function notifyFmtTime(ts: number): string {
  const d = new Date(ts * 1000);
  const p = (n: number) => String(n).padStart(2, '0');
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** 应用内记录展示用：去掉 markdown 加粗标记（外部渠道才需要 **，页面直接渲染文本）。 */
const stripMd = (s: string) => s.replace(/\*\*/g, '');

const TYPE_ICON: Record<string, string> = {
  wecom: '🏢', dingtalk: '🔔', feishu: '🐦', serverchan: '📮',
  pushplus: '➕', bark: '📱', webhook: '🔗',
};

// ── 渠道表单弹窗（按 ChannelDef.fields 动态渲染）──

interface ChannelFormState {
  id?: string;           // 有 = 编辑，无 = 新增
  type: string;
  name: string;
  enabled: boolean;
  params: Record<string, string>;
  format?: string;       // 0.6.144 通知形式（wecom 专属）：markdown 默认 / markdown_v2 / card
  verbosity?: string;    // 0.6.175 通知信息长度（全部渠道）：friendly 默认 / concise / full
}

// 0.6.177 通知形式选项按渠道官方能力标注（三形式对齐企微：默认/表格/卡片）
// 0.6.179：仅企微官方支持 markdown 表格语法（markdown_v2 富消息 ≤20 行）；
// 其他渠道官方消息类型无表格语法 → markdown_v2 与默认渲染一致，弹窗隐藏中间项。
const TABLE_FORMAT_TYPES = new Set<string>(['wecom']);
const FORMAT_OPTIONS: Record<string, [string, string, string]> = {
  wecom: ['默认 markdown（纯文本）', 'markdown_v2（列表以表格呈现）', '卡片（点按查看详情）'],
  dingtalk: ['默认 markdown（列表）', 'markdown（列表，不支持表格）', '卡片 actionCard（点按查看详情）'],
  feishu: ['默认富文本', '富文本（列表，不支持表格）', '卡片（按钮跳转详情）'],
  serverchan: ['默认 markdown', 'markdown（微信侧渲染）', 'markdown + 详情链接'],
  pushplus: ['默认富文本', '富文本（列表）', 'HTML + 详情链接'],
  bark: ['默认 markdown', 'markdown（列表）', '点击通知跳转详情'],
}

const ChannelFormDialog: React.FC<{
  defs: ChannelDef[];
  initial: ChannelFormState;
  onClose: () => void;
  onSaved: () => void;
}> = ({ defs, initial, onClose, onSaved }) => {
  const isEdit = Boolean(initial.id);
  const [type, setType] = useState(initial.type || (defs[0]?.type ?? 'wecom'));
  const [name, setName] = useState(initial.name);
  const [enabled, setEnabled] = useState(initial.enabled);
  const [params, setParams] = useState<Record<string, string>>(initial.params ?? {});
  const [format, setFormat] = useState(initial.format || 'markdown'); // 0.6.144 通知形式
  // 0.6.179：markdown_v2 为企微专属选项（官方表格语法）；其他渠道命中该值
  //（存量配置 / 添加模式切换类型残留）时归一为默认 markdown。
  const effFormat = format === 'markdown_v2' && !TABLE_FORMAT_TYPES.has(type)
    ? 'markdown'
    : (format || 'markdown');
  const [verbosity, setVerbosity] = useState(initial.verbosity || 'friendly'); // 0.6.175 通知信息长度
  const [saving, setSaving] = useState(false);
  const [testingDraft, setTestingDraft] = useState(false);

  const def = defs.find((d) => d.type === type);

  // 弹窗内「测试提供商」（0.6.139，参照 knock）：用当前表单值直接发测试消息，无需先保存
  const runDraftTest = async () => {
    setTestingDraft(true);
    try {
      await testNotifyChannelDraft({
        id: isEdit ? initial.id : undefined, // 0.6.144：编辑模式带 ID，后端回填脱敏参数真实值
        type, name: name.trim() || 'Moo 测试渠道', params,
        format: type === 'webhook' ? undefined : effFormat, // 0.6.177 全渠道形式（0.6.179 非企微 md_v2 归一）
      });
      toast.success('测试消息已发送，请查收');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '测试失败');
    } finally {
      setTestingDraft(false);
    }
  };

  const save = async () => {
    if (!name.trim()) {
      toast.error('请填写渠道名称');
      return;
    }
    setSaving(true);
    try {
      // 0.6.144：形式仅 wecom 消费，其他类型不传（后端忽略）
      const payload: Record<string, unknown> = {
        name: name.trim(), enabled, params,
        format: type === 'webhook' ? '' : effFormat, // 0.6.177 全渠道形式（0.6.179 非企微 md_v2 归一）
        verbosity: verbosity || 'friendly', // 0.6.175 信息长度（全部渠道）
      };
      if (isEdit && initial.id) {
        await updateNotifyChannel(initial.id, payload);
        toast.success('渠道已更新');
      } else {
        payload.type = type;
        await addNotifyChannel(payload as unknown as Omit<NotifyChannel, 'id'>);
        toast.success('渠道已添加');
      }
      onSaved();
      onClose();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      {/* 0.6.144：与背后内容卡片同宽同圆角（基类 ui/dialog 统一：100vw-24px + 18px 圆角；
          桌面端设置卡 = max-w-3xl(768px) - 48px = 720px） */}
      <DialogContent className="sm:w-[720px]">
        <DialogHeader>
          <DialogTitle>{isEdit ? '编辑渠道' : '添加渠道'}</DialogTitle>
          <DialogDescription>
            {def?.desc ?? '选择推送渠道类型'}
          </DialogDescription>
        </DialogHeader>
        {/* 0.6.144 补丁2：表单区左右各收 6px（框收窄一点：微信 webview 下超宽框的
            1px 聚焦环左缘渲染不完整，收窄后验证） */}
        <div className="space-y-3 px-1.5 py-1 max-h-[58vh] overflow-y-auto">
          <div>
            <label className="text-xs text-muted-foreground">渠道类型</label>
            <Select value={type} onValueChange={(v) => { setType(v); setParams({}); }} disabled={isEdit}>
              <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
              <SelectContent>
                {defs.map((d) => (
                  <SelectItem key={d.type} value={d.type}>{TYPE_ICON[d.type] ?? '•'} {d.label}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {/* 0.6.177 通知形式：全渠道生效（webhook 自定义模板除外），三形式对齐企微
              ——各平台按官方能力最佳近似：钉钉 actionCard 整卡跳转 / 飞书
              interactive 卡片+按钮 / Server酱询链接行 / PushPlus html链接 / Bark url 点击跳转；
              0.6.179：「表格」中间项仅企微显示（其余渠道官方无表格语法，与默认同渲染，隐藏防误导） */}
          {type !== 'webhook' && (
            <div>
              <label className="text-xs text-muted-foreground">通知形式</label>
              <Select value={effFormat} onValueChange={setFormat}>
                <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="markdown">{FORMAT_OPTIONS[type]?.[0] ?? '默认 markdown（纯文本）'}</SelectItem>
                  {TABLE_FORMAT_TYPES.has(type) && (
                    <SelectItem value="markdown_v2">{FORMAT_OPTIONS[type]?.[1] ?? 'markdown_v2（列表以表格呈现）'}</SelectItem>
                  )}
                  <SelectItem value="card">{FORMAT_OPTIONS[type]?.[2] ?? '卡片（点按查看详情）'}</SelectItem>
                </SelectContent>
              </Select>
              {format === 'card' && (
                <p className="mt-0.5 text-[11px] text-muted-foreground leading-relaxed">
                  {type === 'wecom'
                    ? '卡片点击跳转通知详情页；需在本页「应用访问入口」配置（默认端口 38100），未配置时自动按默认 markdown 发送。'
                    : type === 'dingtalk'
                    ? '卡片 = 钉钉 actionCard，整卡点击跳转通知详情页；需配置「应用访问入口」，未配置时降纤 markdown 发送。'
                    : type === 'feishu'
                    ? '卡片 = 飞书交互卡片 + 「打开通知详情」按钮；需配置「应用访问入口」，未配置时降级富文本发送。'
                    : type === 'serverchan'
                    ? '尾部追加「打开通知详情」链接；需配置「应用访问入口」，未配置时不加链接。'
                    : type === 'pushplus'
                    ? '用 html 模板追加详情链接（自定义了消息模板时不覆盖）；需配置「应用访问入口」。'
                    : 'iOS 通知点击直接打开详情页；需配置「应用访问入口」。'}
                </p>
              )}
            </div>
          )}
          {/* 0.6.176 通知内容（既 0.6.175 信息长度）：全部渠道生效，与通知形式正交（形式管「怎么呈现」，
              内容管「发多少」：简洁=关键信息 / 友好=关键摘要+折叠 / 完整=全部信息） */}
          <div>
            <label className="text-xs text-muted-foreground">通知内容</label>
            <Select value={verbosity || 'friendly'} onValueChange={setVerbosity}>
              <SelectTrigger className="mt-1 h-9 text-sm"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="concise">简洁（关键信息）</SelectItem>
                <SelectItem value="friendly">友好（关键摘要+折叠）</SelectItem>
                <SelectItem value="full">完整（全部信息）</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div>
            <label className="text-xs text-muted-foreground">渠道名称</label>
            <Input
              className="mt-1 h-9 text-sm"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="如：家庭群 / 手机推送"
              maxLength={32}
            />
          </div>
          {def?.fields.map((f) => (
            <ParamField key={f.key} field={f} value={params[f.key] ?? ''}
              onChange={(v) => setParams((p) => ({ ...p, [f.key]: v }))} />
          ))}
          {!isEdit && (
            <div className="flex items-center justify-between pt-1">
              <span className="text-xs text-muted-foreground">添加后启用</span>
              <Switch checked={enabled} onCheckedChange={setEnabled} aria-label="添加后启用" />
            </div>
          )}
        </div>
        <DialogFooter className="gap-1.5">
          <Button variant="ghost" size="sm" onClick={onClose}>取消</Button>
          {/* 0.6.139 参照 knock「新增通知提供商」弹窗：测试提供商（未保存也能测） */}
          <Button variant="outline" size="sm" onClick={runDraftTest} disabled={testingDraft || saving}>
            {testingDraft && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
            测试提供商
          </Button>
          <Button size="sm" onClick={save} disabled={saving}>
            {saving && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

const ParamField: React.FC<{
  field: ChannelFieldDef;
  value: string;
  onChange: (v: string) => void;
}> = ({ field, value, onChange }) => {
  const masked = field.sensitive && value.startsWith('****');
  return (
    <div>
      <label className="text-xs text-muted-foreground">
        {field.label}
        {field.required && <span className="text-red-500 ml-0.5">*</span>}
      </label>
      <Input
        className="mt-1 h-9 text-sm"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={masked ? '（留空保持不变）' : (field.placeholder || field.default)}
        disabled={masked}
      />
      {masked && <p className="mt-0.5 text-[11px] text-muted-foreground">当前已配置，留空保持不变</p>}
    </div>
  );
};

// ── 主组件 ──

const NotifySettingsTab: React.FC = () => {
  const [sub, setSub] = useState<NotifySubTab>('channel');
  const [settings, setSettings] = useState<NotifySettings | null>(null);
  const [channels, setChannels] = useState<NotifyChannel[]>([]);
  const [defs, setDefs] = useState<ChannelDef[]>([]);
  const [savingKey, setSavingKey] = useState<string | null>(null);
  const [log, setLog] = useState<NotifyLogEntry[]>([]);
  const [logLoading, setLogLoading] = useState(false);
  const [logTick, setLogTick] = useState(0);
  // 记录详情折叠卡（0.6.143）：展开的行索引集合
  const [openLogIdx, setOpenLogIdx] = useState<Set<number>>(new Set());
  const toggleLogIdx = (i: number) =>
    setOpenLogIdx((prev) => {
      const n = new Set(prev);
      if (n.has(i)) n.delete(i); else n.add(i);
      return n;
    });
  // 0.6.170：手动触发事件（通知规则行的门铃按钮，纯图标无文字）
  const [firingKey, setFiringKey] = useState<string | null>(null);
  const fireEvent = async (e: { key: string; label: string }) => {
    setFiringKey(e.key);
    try {
      const r = await fireNotifyEvent(e.key);
      if (r.sent) toast.success(`已触发「${e.label}」通知：应用内已记录，已启用渠道请查收`);
      else toast.error(r.reason || '触发未生效');
      setLogTick((n) => n + 1);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : '触发失败');
    } finally {
      setFiringKey(null);
    }
  };
  const [confirmClear, setConfirmClear] = useState(false);
  const [form, setForm] = useState<ChannelFormState | null>(null);
  const [testing, setTesting] = useState<string | null>(null);
  const [confirmDel, setConfirmDel] = useState<string | null>(null);
  // 详情页地址（0.6.144，卡片形式跳转用的全局配置；走 notify-settings view_base）
  const [viewBaseDraft, setViewBaseDraft] = useState('');
  const [savingViewBase, setSavingViewBase] = useState(false);
  // 应用访问入口折叠（0.6.144 补丁：「详情页地址」改名+折叠卡，沿用 0.6.130 全默认折叠约定）
  const [viewOpen, setViewOpen] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem('new-store.notify-viewbase.collapsed');
      return v === null ? false : v === '0';
    } catch { return false; }
  });
  const toggleViewOpen = () => {
    setViewOpen((v) => {
      try { localStorage.setItem('new-store.notify-viewbase.collapsed', v ? '1' : '0'); } catch { /* ignore */ }
      return !v;
    });
  };
  // 通知类型折叠（本地持久化，默认折叠）：与「应用源列表 / 排序卡」折叠同款。
  // 折叠按钮位于「外部渠道通知」开关卡的开关后面（0.6.125 用户定稿）。
  const [eventsCollapsed, setEventsCollapsed] = useState<boolean>(() => {
    try {
      const v = localStorage.getItem('new-store.notify-events.collapsed');
      return v === null ? true : v === '1';
    } catch { return true; }
  });
  const toggleEventsCollapsed = () => {
    setEventsCollapsed((v) => {
      try { localStorage.setItem('new-store.notify-events.collapsed', v ? '0' : '1'); } catch { /* ignore */ }
      return !v;
    });
  };
  // 各分组独立折叠（0.6.125：应用生命周期/源与网络等每个分组后面一个折叠按钮；
  // 0.6.130 用户定稿：设置里所有折叠默认都是折叠——无记录=折叠，显式 false=用户展开过保留，
  // {分组名: 是否折叠} 持久化）
  const [groupCollapsed, setGroupCollapsed] = useState<Record<string, boolean>>(() => {
    try {
      const v = localStorage.getItem('new-store.notify-groups.collapsed');
      return v ? (JSON.parse(v) as Record<string, boolean>) : {};
    } catch { return {}; }
  });
  const toggleGroup = (name: string) => {
    setGroupCollapsed((cur) => {
      // 0.6.135：语义——无记录/true=折叠（默认）、显式 false=展开。
      // 点击切换：当前展开（===false）→存 true 折叠；否则→存 false 展开
      const next = { ...cur, [name]: cur[name] === false };
      try { localStorage.setItem('new-store.notify-groups.collapsed', JSON.stringify(next)); } catch { /* ignore */ }
      return next;
    });
  };

  const loadChannels = useCallback(async () => {
    try {
      const d = await fetchNotifyChannels();
      setChannels(d.channels);
      setDefs(d.definitions);
    } catch { /* 静默：手动重试 */ }
  }, []);

  const loadLog = useCallback(async () => {
    setLogLoading(true);
    try {
      setLog(await fetchNotifyLog());
    } catch { /* 忽略：下一轮/手动刷新再试 */ }
    finally { setLogLoading(false); }
  }, []);

  useEffect(() => {
    fetchNotifySettings()
      .then((d) => {
        setSettings(d);
        setViewBaseDraft(d.view_base || '');
      })
      .catch(() => toast.error('通知设置加载失败'));
    void loadChannels();
  }, [loadChannels]);

  useEffect(() => {
    if (sub === 'record') void loadLog();
  }, [sub, logTick, loadLog]);

  useEffect(() => () => setConfirmClear(false), []);

  // 保存一个开关（总开关 key='enabled' 或事件 key）：乐观更新 + 失败回滚。
  const saveToggle = async (key: string, next: boolean) => {
    if (!settings) return;
    const prev = settings;
    setSettings((cur) => cur && (key === 'enabled'
      ? { ...cur, enabled: next }
      : { ...cur, events: { ...cur.events, [key]: next } }));
    setSavingKey(key);
    try {
      const r = await updateNotifySettings(key === 'enabled' ? { enabled: next } : { events: { [key]: next } });
      setSettings((cur) => cur && { ...cur, enabled: r.enabled, events: r.events });
    } catch (e) {
      setSettings(prev);
      toast.error(e instanceof Error ? e.message : '保存失败');
    } finally {
      setSavingKey(null);
    }
  };

  // 渠道开关（单独快速切换，不走弹窗）
  const toggleChannel = async (ch: NotifyChannel, next: boolean) => {
    try {
      await updateNotifyChannel(ch.id, { ...ch, enabled: next });
      setChannels((cur) => cur.map((c) => (c.id === ch.id ? { ...c, enabled: next } : c)));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存失败');
    }
  };

  // 详情页地址保存（0.6.144）：空串 = 清除。
  const saveViewBase = async () => {
    setSavingViewBase(true);
    try {
      await updateNotifySettings({ view_base: viewBaseDraft.trim() });
      toast.success(viewBaseDraft.trim() ? '应用访问入口已保存' : '应用访问入口已清除');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存失败');
    } finally {
      setSavingViewBase(false);
    }
  };

  const runTest = async (id: string) => {
    setTesting(id);
    try {
      await testNotifyChannel(id);
      toast.success('测试消息已发送，请查收');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '测试失败');
    } finally {
      setTesting(null);
    }
  };

  const handleDelete = async (id: string) => {
    if (confirmDel !== id) {
      setConfirmDel(id);
      setTimeout(() => setConfirmDel((c) => (c === id ? null : c)), 3000);
      return;
    }
    setConfirmDel(null);
    try {
      await deleteNotifyChannel(id);
      setChannels((cur) => cur.filter((c) => c.id !== id));
      toast.success('渠道已删除');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '删除失败');
    }
  };

  const handleClear = async () => {
    if (!confirmClear) {
      setConfirmClear(true);
      setTimeout(() => setConfirmClear(false), 3000);
      return;
    }
    setConfirmClear(false);
    try {
      await clearNotifyLog();
      setLog([]);
      toast.success('通知记录已清空');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '清空失败');
    }
  };

  const labelOf = (key: string) => settings?.catalog.find((e) => e.key === key)?.label ?? key;

  // 按 group 分组（保持目录顺序）
  const groups: { name: string; items: NotifySettings['catalog'] }[] = [];
  for (const e of settings?.catalog ?? []) {
    const g = groups.find((x) => x.name === e.group);
    if (g) g.items.push(e);
    else groups.push({ name: e.group, items: [e] });
  }

  return (
    <div className="px-3 py-4 sm:px-6 sm:py-5">
      {/* 子 tab 分段控件（对齐 knock 事件中心：推送渠道/通知规则/推送记录） */}
      <div className="flex items-center gap-1 bg-muted/40 rounded-xl p-1 mb-3 w-fit max-w-full overflow-x-auto" role="tablist">
        {NOTIFY_SUB_TABS.map(({ key, label }) => (
          <button
            key={key}
            role="tab"
            aria-selected={sub === key}
            onClick={() => setSub(key)}
            className={cn(
              'h-7 rounded-lg px-3.5 text-xs font-medium transition-colors whitespace-nowrap focus:outline-none',
              sub === key ? 'bg-card text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'
            )}
          >
            {label}
          </button>
        ))}
      </div>

      {sub === 'channel' && (
        <div className="space-y-3">
          {/* 外部渠道通知（0.6.124：标题行 + 添加渠道，说明文字在下；
              总开关移至「通知规则」且只管外部渠道，应用内通知栏始终开） */}
          <div>
            <div className="flex items-center justify-between gap-2">
              <span className="text-sm font-medium">外部渠道通知</span>
              <div className="flex items-center gap-1.5 shrink-0">
                {/* 0.6.166：「发送欢迎语」按钮按用户要求整个移除——欢迎语就是
                    「通知规则」里的普通事件开关（Moo 自身组），无需手动重发入口 */}
                <Button size="sm" variant="outline" className="h-7 px-2.5 text-xs"
                  onClick={() => setForm({ type: defs[0]?.type ?? 'wecom', name: '', enabled: true, params: {}, format: 'markdown', verbosity: 'friendly' })}>
                  <Plus className="mr-1 h-3.5 w-3.5" /> 添加渠道
                </Button>
              </div>
            </div>
            <p className="mt-1 text-xs text-muted-foreground leading-relaxed">
              {channels.length}/10 个渠道 · 消息按规则事件触发，多渠道并发推送。
              支持企业微信 / 钉钉 / 飞书 / Server酱 / PushPlus / Bark / 通用 Webhook（QQ 机器人等）。
            </p>
          </div>

          {/* 应用访问入口（0.6.144：卡片形式跳转用的全局配置；0.6.144 补丁：
              「详情页地址」→「应用访问入口（默认端口 38100）」，标题字体对齐「外部渠道通知」，
              填写区折叠展开（默认折叠，状态持久化），头部带已配置/未配置状态点；
              0.6.144 补丁2：仅当存在卡片形式渠道时才显示——view_base 只被卡片跳转消费，
              其余形式渠道对它无意义（未配置时卡片渠道也自动降级 markdown）） */}
          {channels.some((c) => c.format === 'card') && (
          <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-3">
            <button
              type="button"
              onClick={toggleViewOpen}
              aria-expanded={viewOpen}
              className="w-full flex items-center justify-between gap-2 text-left focus:outline-none"
            >
              <span className="text-sm font-medium">
                应用访问入口
                <span className="ml-1.5 text-xs font-normal text-muted-foreground">（默认端口 38100）</span>
              </span>
              <span className="flex shrink-0 items-center gap-2">
                <span className={cn("text-[11px]", viewBaseDraft.trim() ? "text-emerald-600" : "text-amber-600")}>
                  {viewBaseDraft.trim() ? '已配置' : '未配置'}
                </span>
                <ChevronDown className={cn("h-4 w-4 text-muted-foreground transition-transform", viewOpen && "rotate-180")} />
              </span>
            </button>
            {viewOpen && (
              <div className="mt-2.5">
                <div className="flex gap-1.5">
                  <Input
                    className="h-9 text-sm flex-1"
                    value={viewBaseDraft}
                    onChange={(e) => setViewBaseDraft(e.target.value)}
                    placeholder="如 http://<NAS_IP>:38100（手机可访问的 Moo 入口）"
                    spellCheck={false}
                  />
                  <Button size="sm" variant="outline" onClick={saveViewBase} disabled={savingViewBase} className="h-9 shrink-0">
                    {savingViewBase && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
                    保存
                  </Button>
                </div>
                <p className="mt-1.5 text-[11px] text-muted-foreground leading-relaxed">
                  卡片消息点按后，在该地址下打开通知详情页看完整内容；未配置时，卡片形式的渠道自动按默认 markdown 发送。
                </p>
              </div>
            )}
          </div>
          )}

          {channels.length === 0 && (
            <div className="bg-card rounded-[18px] border border-dashed border-border/40 py-10 text-center">
              <p className="text-xs text-muted-foreground">尚未配置推送渠道</p>
              <p className="mt-1 text-[11px] text-muted-foreground/70">
                支持企业微信 / 钉钉 / 飞书 / Server酱 / PushPlus / Bark / 通用 Webhook（QQ 机器人等）
              </p>
            </div>
          )}
          {channels.map((ch) => {
            const def = defs.find((d) => d.type === ch.type);
            return (
              <div key={ch.id} className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-3">
                <div className="flex items-center gap-3">
                  <div className="h-9 w-9 rounded-xl bg-muted/60 flex items-center justify-center shrink-0 text-base">
                    {TYPE_ICON[ch.type] ?? '•'}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className={cn('text-sm font-medium truncate', !ch.enabled && 'text-muted-foreground')}>{ch.name}</span>
                      <Badge variant="outline" className="text-[11px] shrink-0">{def?.label ?? ch.type}</Badge>
                    </div>
                    <p className="mt-0.5 text-[11px] text-muted-foreground truncate">
                      {ch.params[def?.fields.find((f) => f.required)?.key ?? ''] || def?.label}
                      {ch.type !== 'webhook' && ch.format === 'card' && (
                        <span className="ml-1.5">· 卡片</span>
                      )}
                      {/* 0.6.179：「表格」徽章仅企微（其余渠道 md_v2 与默认同渲染，不标） */}
                      {ch.type === 'wecom' && ch.format === 'markdown_v2' && (
                        <span className="ml-1.5">· 表格</span>
                      )}
                      {ch.verbosity && ch.verbosity !== 'friendly' && (
                        <span className="ml-1.5">
                          {ch.verbosity === 'concise' ? '· 简洁' : '· 完整'}
                        </span>
                      )}
                    </p>
                  </div>
                  <Switch
                    checked={ch.enabled}
                    onCheckedChange={(v) => toggleChannel(ch, v)}
                    aria-label={`启用渠道 ${ch.name}`}
                  />
                </div>
                <div className="mt-2.5 pt-2.5 border-t border-border/15 flex items-center gap-1">
                  <Button variant="ghost" size="sm" className="h-7 px-2 text-xs" onClick={() => runTest(ch.id)} disabled={testing === ch.id}>
                    {testing === ch.id
                      ? <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                      : <Send className="mr-1.5 h-3.5 w-3.5" />}
                    发送测试
                  </Button>
                  <Button variant="ghost" size="sm" className="h-7 px-2 text-xs text-muted-foreground"
                    onClick={() => setForm({ id: ch.id, type: ch.type, name: ch.name, enabled: ch.enabled, params: ch.params, format: ch.format, verbosity: ch.verbosity })}>
                    <Settings2 className="mr-1.5 h-3.5 w-3.5" /> 编辑
                  </Button>
                  <Button variant="ghost" size="sm"
                    className={cn('h-7 px-2 text-xs', confirmDel === ch.id ? 'text-red-500' : 'text-muted-foreground')}
                    onClick={() => handleDelete(ch.id)}>
                    {confirmDel === ch.id ? <Check className="mr-1.5 h-3.5 w-3.5" /> : <Trash2 className="mr-1.5 h-3.5 w-3.5" />}
                    {confirmDel === ch.id ? '确认删除？' : '删除'}
                  </Button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {sub === 'rule' && (
        <div className="space-y-3">
          {/* 外部渠道通知总开关（0.6.124 从「推送渠道」移入；只管外部渠道，
              应用内顶部通知栏始终开、不受此开关控制）。
              折叠按钮在开关后面（0.6.125 用户定稿）：控制下方全部通知类型。 */}
          <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore px-4 py-4">
            <div className="flex items-center gap-3">
              <div className="h-10 w-10 rounded-xl bg-primary/10 flex items-center justify-center shrink-0">
                <Bell className="h-5 w-5 text-primary" />
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-sm font-medium">外部渠道通知</div>
                <p className="mt-0.5 text-xs text-muted-foreground leading-relaxed">
                  关闭后企业微信 / 钉钉等外部渠道不再推送；应用内顶部通知栏始终开启，不受此开关控制。
                </p>
              </div>
              <Switch
                checked={settings?.enabled ?? true}
                onCheckedChange={(v) => saveToggle('enabled', v)}
                disabled={!settings || savingKey === 'enabled'}
                aria-label="外部渠道通知总开关"
              />
              <Button
                variant="ghost"
                size="icon"
                className="h-7 w-7 shrink-0"
                onClick={toggleEventsCollapsed}
                title={eventsCollapsed ? '展开通知类型' : '折叠通知类型'}
                aria-label={eventsCollapsed ? '展开通知类型' : '折叠通知类型'}
              >
                <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", eventsCollapsed && "-rotate-90")} />
              </Button>
            </div>
          </div>

          {/* 通知类型列表（总开关卡后面；默认折叠，持久化）：
              每个分组（应用生命周期 / 源与网络 等）后面各有一个自己的折叠按钮 */}
          {!eventsCollapsed && (
            <>
              <p className="px-1 text-xs text-muted-foreground">
                选择哪些事件需要外部渠道推送。关闭后该事件不再推送外部渠道（应用内顶部通知栏始终弹，不受此控制）；未显式关闭的按类型默认值。
              </p>
              {settings ? groups.map((g) => (
                <div key={g.name}>
                  <button
                    type="button"
                    onClick={() => toggleGroup(g.name)}
                    className="flex w-full items-center justify-between px-1 mb-1 text-sm font-medium"
                    title={groupCollapsed[g.name] === false ? `折叠 ${g.name}` : `展开 ${g.name}`}
                    aria-label={groupCollapsed[g.name] === false ? `折叠 ${g.name}` : `展开 ${g.name}`}
                  >
                    <span>{g.name}（{g.items.length}）</span>
                    <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", groupCollapsed[g.name] !== false && "-rotate-90")} />
                  </button>
                  {groupCollapsed[g.name] === false && (
                    <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore divide-y divide-border/15 overflow-hidden">
                      {g.items.map((e) => (
                        <div key={e.key} className="flex items-center gap-2 px-4 py-2.5">
                          <div className="flex-1 min-w-0">
                            <div className="text-[13px] font-medium leading-tight">{e.label}</div>
                            <div className="mt-0.5 text-[11px] text-muted-foreground leading-snug">{e.desc}</div>
                          </div>
                          {/* 0.6.170：触发通知门铃（纯图标无文字）——点一下手动触发
                              该事件（应用内落记录 + 已启用渠道同步推送）；
                              事件开关关时禁用；与文字/开关各留 8px 不拥挤 */}
                          <Button
                            type="button"
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8 shrink-0 text-muted-foreground hover:text-foreground hover:bg-primary/10"
                            onClick={() => fireEvent(e)}
                            disabled={firingKey === e.key || settings.events[e.key] === false}
                            title={`触发「${e.label}」通知`}
                            aria-label={`触发「${e.label}」通知`}
                          >
                            {firingKey === e.key
                              ? <Loader2 className="h-4 w-4 animate-spin" />
                              : <BellRing className="h-4 w-4" />}
                          </Button>
                          <Switch
                            checked={settings.events[e.key] !== false}
                            onCheckedChange={(v) => saveToggle(e.key, v)}
                            disabled={savingKey === e.key}
                            aria-label={`通知：${e.label}`}
                          />
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              )) : (
                <div className="flex justify-center py-10">
                  <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
                </div>
              )}
            </>
          )}
        </div>
      )}

      {sub === 'record' && (
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-2">
            <p className="text-xs text-muted-foreground">
              共 {log.length} 条（仅保留最近 200 条）
            </p>
            <div className="flex items-center gap-1 shrink-0">
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => setLogTick((n) => n + 1)}
                disabled={logLoading}
              >
                <RefreshCw className={cn('mr-1.5 h-3.5 w-3.5', logLoading && 'animate-spin')} />
                刷新
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className={cn('h-7 px-2 text-xs', confirmClear ? 'text-red-500' : 'text-muted-foreground')}
                onClick={handleClear}
                disabled={log.length === 0}
              >
                <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                {confirmClear ? '确认清空？' : '清空'}
              </Button>
            </div>
          </div>
          <div className="bg-card rounded-[18px] border border-border/20 shadow-appstore divide-y divide-border/15 overflow-hidden">
            {logLoading && log.length === 0 && (
              <div className="flex justify-center py-10">
                <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
              </div>
            )}
            {!logLoading && log.length === 0 && (
              <div className="py-10 text-center text-xs text-muted-foreground">
                暂无通知记录——开启的通知事件会记录在这里
              </div>
            )}
            {log.map((e, i) => {
              const failedChannels = e.channels ? Object.entries(e.channels) : [];
              // 徽章 = 事件 × 投递综合结果（0.6.142 用户截图反馈：事件成功但渠道
              // 发送失败时绿色「成功」与红色失败文案自相矛盾）：
              // 事件失败=失败；事件成功+全渠道成功/无渠道=成功；事件成功+任一渠道失败=部分成功
              const st = !e.ok ? 'fail' : failedChannels.length > 0 ? 'partial' : 'ok';
              return (
                <div key={`${e.ts}-${i}`} className="px-4 py-2.5">
                  <div className="flex items-center gap-2.5">
                    <span className="text-[11px] text-muted-foreground tabular-nums shrink-0 w-[76px]">
                      {notifyFmtTime(e.ts)}
                    </span>
                    <Badge
                      variant="outline"
                      className={cn(
                        'shrink-0 text-[11px] px-1.5 justify-center',
                        st === 'ok'
                          ? 'border-emerald-500/25 bg-emerald-500/10 text-emerald-700'
                          : st === 'partial'
                            ? 'border-amber-500/25 bg-amber-500/10 text-amber-600'
                            : 'border-red-500/25 bg-red-500/10 text-red-600'
                      )}
                    >
                      {st === 'ok' ? '成功' : st === 'partial' ? '部分成功' : '失败'}
                    </Badge>
                    <span className="text-[13px] truncate" title={`${labelOf(e.event)}：${e.msg}`}>
                      {e.msg || labelOf(e.event)}
                    </span>
                  </div>
                  {/* 0.6.169：所有记录的折叠卡统一为欢迎语卡形式（0.6.155–0.6.167
                      用户确认「美观」的样式）：主题色渐变 + rounded-xl + 大字粗头
                      + chevron；默认折叠（0.6.130 约定）。
                      头 = 记录消息去「Moo · 」前缀（欢迎语即「欢迎使用Moo」）；
                      正文 = 完整正文（无正文记录回退完整消息文本，0.6.168），去 ** */}
                  <div className="mt-1.5 pl-[92px]">
                    <div className="overflow-hidden rounded-xl border border-primary/20 bg-gradient-to-br from-primary/10 via-card to-card shadow-appstore">
                      <button
                        type="button"
                        onClick={() => toggleLogIdx(i)}
                        className="flex w-full items-center justify-between gap-2 px-3.5 py-2.5 hover:bg-primary/5"
                        aria-expanded={openLogIdx.has(i)}
                      >
                        <span className="truncate text-[13px] font-semibold">
                          {(e.msg || labelOf(e.event)).replace(/^Moo · /, '')}
                        </span>
                        <ChevronDown
                          className={cn(
                            'h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform',
                            openLogIdx.has(i) && 'rotate-180',
                          )}
                        />
                      </button>
                      {openLogIdx.has(i) && (e.content || e.msg) && (
                        <div className="max-h-56 overflow-y-auto whitespace-pre-wrap border-t border-primary/15 px-3.5 pb-2.5 text-[11.5px] leading-relaxed text-foreground/85">
                          {stripMd(e.content || e.msg)}
                        </div>
                      )}
                    </div>
                  </div>
                  {failedChannels.length > 0 && (
                    <div className="mt-1 pl-[92px] text-[11px] text-red-500/90">
                      {failedChannels.map(([ch, err]) => (
                        <div key={ch}>渠道「{ch}」发送失败：{err}</div>
                      ))}
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}

      {form && (
        <ChannelFormDialog
          defs={defs}
          initial={form}
          onClose={() => setForm(null)}
          onSaved={() => { void loadChannels(); void loadLog(); }}
        />
      )}
    </div>
  );
};

export default NotifySettingsTab;
