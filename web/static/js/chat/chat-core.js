let currentConversationId = null;

/** Persist the visible chat in the URL so a reload can restore and reconnect it. */
function syncChatConversationHash(conversationId) {
    const normalizedConversationId = String(conversationId || '').trim();
    if (!normalizedConversationId || window.location.hash.split('?')[0] !== '#chat') return;
    const targetHash = '#chat?conversation=' + encodeURIComponent(normalizedConversationId);
    if (window.location.hash !== targetHash) {
        window.history.replaceState(null, '', targetHash);
    }
}
window.syncChatConversationHash = syncChatConversationHash;

function clearChatConversationHash() {
    if (window.location.hash.split('?')[0] !== '#chat' || window.location.hash === '#chat') return;
    window.history.replaceState(null, '', '#chat');
}
window.clearChatConversationHash = clearChatConversationHash;
let loadConversationRequestSeq = 0;
let loadConversationAbortController = null;
let loadConversationPendingId = '';
let chatConversationNavigationSeq = 0;

function isChatConversationLoadPending(conversationId) {
    const id = String(conversationId || '').trim();
    return !!id && loadConversationPendingId === id;
}
window.isChatConversationLoadPending = isChatConversationLoadPending;

function markChatConversationNavigation(nextConversationId, force = false) {
    const nextId = String(nextConversationId || '').trim();
    const visibleId = String(currentConversationId || '').trim();
    if (force || nextId !== visibleId) {
        chatConversationNavigationSeq++;
    }
    return chatConversationNavigationSeq;
}

/**
 * 离开聊天页时立即让尚在初始化的发送请求失去页面所有权。
 * 后端任务仍会继续执行；这里只中止浏览器前台流，避免首个 conversation
 * 事件在用户已经切到其他页面后再次抢占当前会话。
 */
function abandonChatConversationForPageNavigation() {
    markChatConversationNavigation('', true);
    if (typeof window.cancelScheduledChatConversationFromHash === 'function') {
        window.cancelScheduledChatConversationFromHash();
    }
    cancelPendingConversationLoad();
    detachLiveChatStreamForNavigation('', true);
}
window.abandonChatConversationForPageNavigation = abandonChatConversationForPageNavigation;

/**
 * 轻量会话 LRU 缓存。
 *
 * 缓存只用作请求失败时的降级数据，不能先于服务端响应直接渲染：
 * 运行中会话的 process details 会持续写入，直接渲染旧快照会让
 * UI 暂时回退到旧轮次，等 task-events 接管后又突然跳到最新轮次。
 */
const CONVERSATION_LITE_CACHE_MAX = 12;
const conversationLiteCache = new Map();

function getConversationLiteFromCache(conversationId) {
    if (!conversationId) return null;
    const hit = conversationLiteCache.get(conversationId);
    if (!hit) return null;
    conversationLiteCache.delete(conversationId);
    conversationLiteCache.set(conversationId, hit);
    return hit;
}

function putConversationLiteCache(conversationId, data) {
    if (!conversationId || !data) return;
    conversationLiteCache.delete(conversationId);
    conversationLiteCache.set(conversationId, data);
    while (conversationLiteCache.size > CONVERSATION_LITE_CACHE_MAX) {
        const oldest = conversationLiteCache.keys().next().value;
        conversationLiteCache.delete(oldest);
    }
}

function invalidateConversationLiteCache(conversationId) {
    if (conversationId) {
        conversationLiteCache.delete(conversationId);
    } else {
        conversationLiteCache.clear();
    }
}

window.invalidateConversationLiteCache = invalidateConversationLiteCache;

// @ 提及相关状态
let mentionTools = [];
let mentionToolsLoaded = false;
let mentionToolsLoadingPromise = null;
let mentionSuggestionsEl = null;
let mentionFilteredTools = [];
let externalMcpNames = []; // 外部MCP名称列表
const mentionState = {
    active: false,
    startIndex: -1,
    query: '',
    selectedIndex: 0,
};

// IME输入法状态跟踪
let isComposing = false;
let compositionEndTimer = null;

// 输入框草稿保存相关
const DRAFT_STORAGE_KEY = 'cyberstrike-chat-draft';
const RECENT_CONVERSATIONS_EXPANDED_KEY = 'cyberstrike-chat-recent-conversations-expanded';
let draftSaveTimer = null;
const DRAFT_SAVE_DELAY = 500; // 500ms防抖延迟

// 对话文件上传相关（后端会拼接路径与内容发给大模型，前端不再重复发文件列表）
const MAX_CHAT_FILES = 10;
const CHAT_FILE_DEFAULT_PROMPT = '请根据上传的文件内容进行分析。';
/** 与 handler.formatInterruptContinueUserMessage 首段一致；主对话不展示，仅迭代详情（user_interrupt_continue） */
const CHAT_INTERRUPT_CONTINUE_USER_PREFIX = '【用户补充 / 中断后继续】';
function isInterruptContinueInjectChatMessage(content) {
    return typeof content === 'string' && content.trimStart().startsWith(CHAT_INTERRUPT_CONTINUE_USER_PREFIX);
}
/**
 * 对话附件：选文件后异步 POST /api/chat-uploads，发送时只传 serverPath（绝对路径），请求体不再内联大文件内容。
 * @type {{ id: number, fileName: string, mimeType: string, serverPath: string|null, uploading: boolean, uploadPercent: number, uploadPromise: Promise<void>|null, uploadError: string|null }[]}
 */
let chatAttachments = [];
let chatAttachmentSeq = 0;

// 对话模式：eino_single = Eino ADK 单代理（/api/eino-agent/stream）；deep / plan_execute / supervisor = Eino 多代理（/api/multi-agent/stream，请求体 orchestration）
const AGENT_MODE_STORAGE_KEY = 'cyberstrike-chat-agent-mode';
const AGENT_MODE_CONVERSATION_STORAGE_PREFIX = 'cyberstrike-chat-agent-mode:conversation';
const AI_CHANNEL_STORAGE_KEY = 'cyberstrike-chat-ai-channel';
const REASONING_MODE_LS = 'cyberstrike-chat-reasoning-mode';
const REASONING_EFFORT_LS = 'cyberstrike-chat-reasoning-effort';
const CHAT_AI_CHANNEL_SUMMARY_NAME_MAX = 10;
const CHAT_AGENT_MODE_EINO_SINGLE = 'eino_single';
const CHAT_AGENT_EINO_MODES = ['deep', 'plan_execute', 'supervisor'];
let multiAgentAPIEnabled = false;
let chatAIChannels = {};
let chatDefaultAIChannel = '';
let chatAIChannelIdByNormalizedId = {};
let chatHitlAuditModelName = '';
let chatSystemModelRequestSeq = 0;
let chatSystemModelSaving = false;
let chatSystemModelCloseTimer = null;
let chatSystemModelOptions = [];
let chatSystemModelCurrent = '';
let chatSystemModelLoadError = '';
const CHAT_SYSTEM_MODEL_CACHE_TTL_MS = 5 * 60 * 1000;
const chatSystemModelCache = new Map();

// 人机协同（HITL）会话级配置
const HITL_STORAGE_PREFIX = 'cyberstrike-chat-hitl';
const HITL_MODE_OFF = 'off';
const HITL_MODE_APPROVAL = 'approval';
const HITL_MODE_REVIEW_EDIT = 'review_edit';
const HITL_MODE_OPTIONS = [HITL_MODE_OFF, HITL_MODE_APPROVAL, HITL_MODE_REVIEW_EDIT];
const DEFAULT_HITL_TIMEOUT_SECONDS = 300;
// Agent orchestration/control tools are safe baseline exemptions for every
// conversation. Keep this separate from config.tool_whitelist: the latter is
// enforced globally by the backend and must not be copied into this field.
const DEFAULT_HITL_SESSION_TOOL_WHITELIST = 'tool_search, skill, task, write_todos, transfer_to_agent, exit, TaskCreate, TaskGet, TaskUpdate, TaskList, upsert_project_fact, get_project_fact';
let hitlApplyFeedbackTimer = null;
let hitlAutoSaveTimer = null;
let hitlConfigSyncConversationId = '';
let hitlConfigSyncPromise = Promise.resolve();
const sessionSettingsSelects = new Map();
let sessionSettingsSelectDocBound = false;

function sessionSettingsSelectLabel(option) {
    return option ? (option.textContent || option.label || option.value || '') : '';
}

function syncSessionSettingsSelect(select) {
    const reg = sessionSettingsSelects.get(select);
    if (!reg) return;
    const selected = select.options[select.selectedIndex];
    reg.value.textContent = sessionSettingsSelectLabel(selected);
    reg.trigger.disabled = !!select.disabled;
    reg.wrapper.classList.toggle('is-disabled', !!select.disabled);
    reg.menu.innerHTML = '';

    Array.prototype.forEach.call(select.options, function (option, index) {
        const item = document.createElement('button');
        item.type = 'button';
        item.className = 'session-settings-select-option';
        item.setAttribute('role', 'option');
        item.setAttribute('data-index', String(index));
        item.setAttribute('aria-selected', option.selected ? 'true' : 'false');
        item.disabled = !!option.disabled;
        item.classList.toggle('is-selected', !!option.selected);

        const label = document.createElement('span');
        label.className = 'session-settings-select-option-label';
        label.textContent = sessionSettingsSelectLabel(option);
        item.appendChild(label);
        reg.menu.appendChild(item);
    });
}

function closeSessionSettingsSelect(select) {
    const reg = sessionSettingsSelects.get(select);
    if (!reg) return;
    reg.wrapper.classList.remove('open');
    reg.trigger.setAttribute('aria-expanded', 'false');
}

function closeAllSessionSettingsSelects() {
    sessionSettingsSelects.forEach(function (_reg, select) {
        closeSessionSettingsSelect(select);
    });
}

function enhanceSessionSettingsSelect(select) {
    if (!select || select.dataset.sessionSettingsSelect === '1') {
        if (select) syncSessionSettingsSelect(select);
        return;
    }
    const panel = select.closest && select.closest('.conversation-reasoning-card');
    if (!panel) return;

    select.dataset.sessionSettingsSelect = '1';
    select.classList.add('session-settings-native-select');
    select.tabIndex = -1;
    select.setAttribute('aria-hidden', 'true');

    const wrapper = document.createElement('div');
    wrapper.className = 'session-settings-select';
    const trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = 'session-settings-select-trigger';
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    const value = document.createElement('span');
    value.className = 'session-settings-select-value';
    const caret = document.createElement('span');
    caret.className = 'session-settings-select-caret';
    caret.setAttribute('aria-hidden', 'true');
    caret.textContent = '⌄';
    trigger.appendChild(value);
    trigger.appendChild(caret);

    const menu = document.createElement('div');
    menu.className = 'session-settings-select-menu';
    menu.setAttribute('role', 'listbox');

    select.parentNode.insertBefore(wrapper, select);
    wrapper.appendChild(trigger);
    wrapper.appendChild(menu);
    wrapper.appendChild(select);
    sessionSettingsSelects.set(select, { wrapper: wrapper, trigger: trigger, value: value, menu: menu });

    trigger.addEventListener('click', function (event) {
        event.stopPropagation();
        if (select.disabled) return;
        const willOpen = !wrapper.classList.contains('open');
        closeAllSessionSettingsSelects();
        wrapper.classList.toggle('open', willOpen);
        trigger.setAttribute('aria-expanded', willOpen ? 'true' : 'false');
    });

    trigger.addEventListener('keydown', function (event) {
        if (select.disabled) return;
        const enabled = Array.prototype.filter.call(select.options, function (option) { return !option.disabled; });
        if (!enabled.length) return;
        const currentOption = select.options[select.selectedIndex];
        const current = Math.max(0, enabled.indexOf(currentOption));
        let next = current;
        if (event.key === 'ArrowDown') next = Math.min(enabled.length - 1, current + 1);
        else if (event.key === 'ArrowUp') next = Math.max(0, current - 1);
        else if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = enabled.length - 1;
        else if (event.key === 'Escape') {
            closeSessionSettingsSelect(select);
            return;
        } else if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            wrapper.classList.add('open');
            trigger.setAttribute('aria-expanded', 'true');
            return;
        } else {
            return;
        }
        event.preventDefault();
        const nextOption = enabled[next];
        if (nextOption && select.value !== nextOption.value) {
            select.value = nextOption.value;
            select.dispatchEvent(new Event('change', { bubbles: true }));
        }
        syncSessionSettingsSelect(select);
    });

    menu.addEventListener('click', function (event) {
        const item = event.target.closest('.session-settings-select-option');
        if (!item || item.disabled) return;
        event.stopPropagation();
        const option = select.options[Number(item.dataset.index)];
        if (option && !option.disabled && select.value !== option.value) {
            select.value = option.value;
            select.dispatchEvent(new Event('change', { bubbles: true }));
        }
        syncSessionSettingsSelect(select);
        closeSessionSettingsSelect(select);
    });

    select.addEventListener('change', function () {
        syncSessionSettingsSelect(select);
    });
    syncSessionSettingsSelect(select);
}

function initSessionSettingsSelects() {
    const panel = document.getElementById('conversation-reasoning-body');
    if (!panel) return;
    panel.querySelectorAll('select').forEach(enhanceSessionSettingsSelect);
    if (!sessionSettingsSelectDocBound) {
        document.addEventListener('click', closeAllSessionSettingsSelects);
        document.addEventListener('keydown', function (event) {
            if (event.key === 'Escape') closeAllSessionSettingsSelects();
        });
        sessionSettingsSelectDocBound = true;
    }
}

function refreshSessionSettingsSelects() {
    sessionSettingsSelects.forEach(function (_reg, select) {
        syncSessionSettingsSelect(select);
    });
}

function syncChatReasoningBarHeight() {
    const reasoning = document.getElementById('chat-reasoning-wrapper');
    const inputBar = document.getElementById('chat-input-container');
    if (!reasoning || !inputBar) return;
    // The composer is now a two-layer surface and is intentionally taller than
    // the sidebar trigger. Do not mirror its height into the settings card.
    reasoning.style.removeProperty('--chat-input-bar-height');
    const chatContainer = inputBar.closest('.chat-container');
    const height = Math.ceil(inputBar.getBoundingClientRect().height || 0);
    if (chatContainer && height > 0) {
        chatContainer.style.setProperty('--chat-composer-total-height', height + 'px');
    }
}

function mountChatSessionSettingsPopover() {
    const wrap = document.getElementById('chat-reasoning-wrapper');
    const composerSurface = document.querySelector('.chat-composer-surface');
    if (!wrap || !composerSurface) return;
    if (wrap.parentElement !== composerSurface) {
        composerSurface.appendChild(wrap);
    }
    wrap.classList.add('chat-session-settings-popover');
    syncChatSessionSettingsLayerState();
}

function syncChatSessionSettingsLayerState() {
    const wrap = document.getElementById('chat-reasoning-wrapper');
    const inputBar = document.getElementById('chat-input-container');
    if (!inputBar) return;
    const open = !!wrap && wrap.style.display !== 'none' &&
        !wrap.classList.contains('conversation-reasoning-collapsed');
    inputBar.classList.toggle('is-session-settings-open', open);
}

function initChatReasoningBarHeightSync() {
    mountChatSessionSettingsPopover();
    syncChatReasoningBarHeight();
    window.addEventListener('resize', syncChatReasoningBarHeight);
    const inputBar = document.getElementById('chat-input-container');
    if (inputBar && typeof ResizeObserver !== 'undefined') {
        const ro = new ResizeObserver(syncChatReasoningBarHeight);
        ro.observe(inputBar);
    }
}

/** 非阻塞提示（与 chat-files-toast 样式共用） */
function showChatToast(message, type) {
    const text = message == null ? '' : String(message);
    if (!text) return;
    const el = document.createElement('div');
    el.className = 'chat-files-toast' + (type === 'error' ? ' chat-toast--error' : '');
    el.setAttribute('role', 'status');
    el.textContent = text;
    document.body.appendChild(el);
    requestAnimationFrame(function () {
        el.classList.add('chat-files-toast-visible');
    });
    const hideMs = type === 'error' ? 4500 : 2600;
    setTimeout(function () {
        el.classList.remove('chat-files-toast-visible');
        setTimeout(function () { el.remove(); }, 300);
    }, hideMs);
}
if (typeof window !== 'undefined') {
    window.showChatToast = showChatToast;
}

function normalizeOrchestrationClient(s) {
    const v = String(s || '').trim().toLowerCase().replace(/-/g, '_');
    if (v === 'plan_execute' || v === 'planexecute' || v === 'pe') return 'plan_execute';
    if (v === 'supervisor' || v === 'super' || v === 'sv') return 'supervisor';
    return 'deep';
}

function chatAgentModeIsEino(mode) {
    return CHAT_AGENT_EINO_MODES.indexOf(mode) >= 0;
}

function chatAgentModeIsEinoSingle(mode) {
    return mode === CHAT_AGENT_MODE_EINO_SINGLE;
}

function normalizeHitlMode(mode) {
    let v = String(mode || '').trim().toLowerCase().replace(/-/g, '_');
    if (v === 'feedback' || v === 'followup') {
        v = HITL_MODE_APPROVAL;
    }
    if (HITL_MODE_OPTIONS.includes(v)) return v;
    return HITL_MODE_OFF;
}

function normalizeHitlTimeoutForChat(value, fallback) {
    const n = Number(value);
    if (!Number.isFinite(n)) return fallback;
    return Math.max(0, Math.min(86400, Math.round(n)));
}

function defaultHitlConfig() {
    const serverDefault = (typeof window !== 'undefined' && window.csaiHitlDefaultConfig && typeof window.csaiHitlDefaultConfig === 'object')
        ? window.csaiHitlDefaultConfig
        : {};
    const serverReviewer = serverDefault.reviewer || ((typeof window !== 'undefined' && window.csaiHitlDefaultReviewer)
        ? window.csaiHitlDefaultReviewer
        : 'human');
    return {
        mode: normalizeHitlMode(serverDefault.mode || HITL_MODE_OFF),
        reviewer: normalizeHitlReviewer(serverReviewer),
        sensitiveTools: DEFAULT_HITL_SESSION_TOOL_WHITELIST,
        timeoutSeconds: normalizeHitlTimeoutForChat(serverDefault.timeoutSeconds, DEFAULT_HITL_TIMEOUT_SECONDS),
        updatedAt: ''
    };
}

function normalizeHitlReviewer(v) {
    const x = String(v || '').trim().toLowerCase();
    if (x === 'audit_agent' || x === 'agent' || x === 'ai') return 'audit_agent';
    return 'human';
}

/** 白名单字符串拆成数组（逗号或换行分隔，与 textarea 一致） */
function hitlToolsSplitToArray(s) {
    return String(s || '')
        .split(/[,\n\r]+/)
        .map(function (x) { return x.trim(); })
        .filter(Boolean);
}

/** 与 config.yaml hitl.tool_whitelist 合并为输入框展示（全局项在前，去重不区分大小写） */
function hitlMergeToolsForDisplay(globalArr, sessionToolsArr) {
    const seen = Object.create(null);
    const out = [];
    function addOne(t) {
        const n = String(t || '').trim();
        if (!n) return;
        const k = n.toLowerCase();
        if (seen[k]) return;
        seen[k] = true;
        out.push(n);
    }
    if (Array.isArray(globalArr)) {
        globalArr.forEach(addOne);
    }
    if (Array.isArray(sessionToolsArr)) {
        sessionToolsArr.forEach(addOne);
    }
    return out.join(', ');
}

/** 保存/发请求前去掉全局白名单工具，避免会话里重复存 config 已有项 */
function hitlStripGlobalToolsFromFormString(globalArr, commaStr) {
    if (!Array.isArray(globalArr) || globalArr.length === 0) {
        return typeof commaStr === 'string' ? commaStr.trim() : '';
    }
    const g = Object.create(null);
    globalArr.forEach(function (t) {
        const k = String(t || '').trim().toLowerCase();
        if (k) g[k] = true;
    });
    return hitlToolsSplitToArray(commaStr)
        .filter(function (p) {
            return p && !g[p.toLowerCase()];
        })
        .join(', ');
}

function getHitlStorageKeyByConversation(conversationId) {
    return `${HITL_STORAGE_PREFIX}:${String(conversationId || '').trim()}`;
}

function chatTranslate(key, fallback) {
    if (typeof window.t === 'function') {
        const translated = window.t(key);
        if (translated && translated !== key) return translated;
    }
    return fallback;
}

function getHitlModeLabel(mode) {
    const safeMode = normalizeHitlMode(mode);
    switch (safeMode) {
        case HITL_MODE_APPROVAL:
            return chatTranslate('chat.hitlModeApproval', '审批模式');
        case HITL_MODE_REVIEW_EDIT:
            return chatTranslate('chat.hitlModeReviewEdit', '审查编辑');
        default:
            return chatTranslate('chat.hitlModeOff', '关闭');
    }
}

function getHitlConfigForConversation(conversationId) {
    const fallback = defaultHitlConfig();
    const cid = conversationId ? String(conversationId).trim() : '';
    if (!cid) {
        return fallback;
    }
    const key = getHitlStorageKeyByConversation(cid);
    try {
        const raw = localStorage.getItem(key);
        if (!raw) {
            return fallback;
        }
        const parsed = JSON.parse(raw);
        if (!parsed || typeof parsed !== 'object') {
            return fallback;
        }
        return {
            mode: normalizeHitlMode(parsed.mode),
            reviewer: normalizeHitlReviewer(parsed.reviewer),
            sensitiveTools: typeof parsed.sensitiveTools === 'string' ? parsed.sensitiveTools : fallback.sensitiveTools,
            timeoutSeconds: normalizeHitlTimeoutForChat(parsed.timeoutSeconds, fallback.timeoutSeconds),
            updatedAt: typeof parsed.updatedAt === 'string' ? parsed.updatedAt : ''
        };
    } catch (e) {
        return fallback;
    }
}

function setHitlReviewerUI(reviewer) {
    const v = normalizeHitlReviewer(reviewer);
    const hidden = document.getElementById('hitl-reviewer-select');
    if (hidden) hidden.value = v;
    document.querySelectorAll('.hitl-reviewer-toggle-btn').forEach(function (btn) {
        const active = btn.getAttribute('data-reviewer') === v;
        btn.classList.toggle('is-active', active);
        btn.setAttribute('aria-pressed', active ? 'true' : 'false');
    });
}

async function onHitlReviewerChanged(reviewer) {
    setHitlReviewerUI(reviewer);
    updateChatReasoningSummary();
    const cfg = readHitlConfigFromForm();
    const cid = typeof currentConversationId === 'string' ? currentConversationId.trim() : '';
    saveHitlConfigForConversation(cid, cfg, { syncGlobalLast: true });
    try {
        if (cid && typeof window.saveHitlConversationConfig === 'function') {
            await window.saveHitlConversationConfig(cid, cfg);
        } else if (typeof window.putHitlDefaultConfig === 'function') {
            await window.putHitlDefaultConfig(cfg);
        } else if (typeof window.putHitlDefaultReviewer === 'function') {
            await window.putHitlDefaultReviewer(cfg.reviewer);
        }
        const ok = typeof window.t === 'function' ? window.t('hitl.pageReviewerSaved') : '审批方已保存。';
        showChatToast(ok, 'success');
    } catch (e) {
        logger.warn('onHitlReviewerChanged', e);
        const prefix = typeof window.t === 'function' ? window.t('chat.hitlApplyFail') : '同步到服务器失败';
        showChatToast(prefix, 'error');
    }
}

function bindHitlReviewerToggleListeners() {
    document.querySelectorAll('.hitl-reviewer-toggle-btn').forEach(function (btn) {
        if (btn.dataset.hitlReviewerBound === '1') return;
        btn.dataset.hitlReviewerBound = '1';
        btn.addEventListener('click', function () {
            const v = btn.getAttribute('data-reviewer');
            if (!v) return;
            onHitlReviewerChanged(v);
        });
    });
}

function saveHitlConfigForConversation(conversationId, cfg, opts) {
    void opts;
    if (!conversationId) {
        return;
    }
    const payload = {
        mode: normalizeHitlMode(cfg && cfg.mode),
        reviewer: normalizeHitlReviewer(cfg && cfg.reviewer),
        sensitiveTools: typeof (cfg && cfg.sensitiveTools) === 'string' ? cfg.sensitiveTools : '',
        timeoutSeconds: normalizeHitlTimeoutForChat(cfg && cfg.timeoutSeconds, DEFAULT_HITL_TIMEOUT_SECONDS),
        updatedAt: typeof (cfg && cfg.updatedAt) === 'string' ? cfg.updatedAt : ''
    };
    const key = getHitlStorageKeyByConversation(conversationId);
    try {
        localStorage.setItem(key, JSON.stringify(payload));
    } catch (e) {
        logger.warn('saveHitlConfigForConversation failed', e);
    }
}

function readHitlConfigFromForm() {
    const modeEl = document.getElementById('hitl-mode-select');
    const reviewerEl = document.getElementById('hitl-reviewer-select');
    const toolsEl = document.getElementById('hitl-sensitive-tools');
    const timeoutEl = document.getElementById('hitl-timeout-select');
    const mode = normalizeHitlMode(modeEl ? modeEl.value : HITL_MODE_OFF);
    const reviewer = normalizeHitlReviewer(reviewerEl ? reviewerEl.value : 'human');
    let sensitiveTools = toolsEl ? String(toolsEl.value || '').trim() : '';
    const g = typeof window !== 'undefined' ? window.csaiHitlGlobalToolWhitelist : null;
    if (Array.isArray(g) && g.length > 0) {
        sensitiveTools = hitlStripGlobalToolsFromFormString(g, sensitiveTools);
    }
    return {
        mode,
        reviewer,
        sensitiveTools,
        timeoutSeconds: normalizeHitlTimeoutForChat(timeoutEl ? timeoutEl.value : DEFAULT_HITL_TIMEOUT_SECONDS, DEFAULT_HITL_TIMEOUT_SECONDS),
        updatedAt: new Date().toISOString()
    };
}

function updateHitlStatusUI(_cfg) {
    /* 侧栏已改为自动保存；同步更新输入框快捷摘要。 */
    updateChatReasoningSummary();
}

function applyHitlConfigToUI(cfg) {
    const conf = cfg || defaultHitlConfig();
    const modeEl = document.getElementById('hitl-mode-select');
    const toolsEl = document.getElementById('hitl-sensitive-tools');
    const timeoutEl = document.getElementById('hitl-timeout-select');
    const uiMode = normalizeHitlMode(conf.mode);
    if (modeEl) modeEl.value = uiMode;
    setHitlReviewerUI(conf.reviewer);
    // Keep this field scoped to the current conversation. The config-level
    // allowlist is applied by the backend and must not be copied into the
    // editable session value. Empty/legacy sessions receive only the stable
    // Agent control-tool baseline shown by the original UI.
    const toolsVal = typeof conf.sensitiveTools === 'string' && conf.sensitiveTools.trim()
        ? conf.sensitiveTools.trim()
        : DEFAULT_HITL_SESSION_TOOL_WHITELIST;
    if (toolsEl) {
        toolsEl.value = toolsVal;
    }
    if (timeoutEl) {
        const timeoutSeconds = normalizeHitlTimeoutForChat(conf.timeoutSeconds, DEFAULT_HITL_TIMEOUT_SECONDS);
        const supported = Array.from(timeoutEl.options || []).some(function (option) {
            return Number(option.value) === timeoutSeconds;
        });
        timeoutEl.value = String(supported ? timeoutSeconds : DEFAULT_HITL_TIMEOUT_SECONDS);
    }
    updateHitlStatusUI(conf);
    refreshSessionSettingsSelects();
}

function bindHitlSidebarModeListener() {
    const modeEl = document.getElementById('hitl-mode-select');
    const timeoutEl = document.getElementById('hitl-timeout-select');
    [modeEl, timeoutEl].forEach(function (el) {
        if (!el || el.dataset.hitlModeBound === '1') return;
        el.dataset.hitlModeBound = '1';
        el.addEventListener('change', function () {
            applyHitlConfigToUI(readHitlConfigFromForm());
            refreshSessionSettingsSelects();
            scheduleHitlSidebarAutosave(0);
            updateChatReasoningSummary();
        });
    });
}

function refreshHitlConfigByCurrentConversation() {
    const cfg = getHitlConfigForConversation(currentConversationId || '');
    applyHitlConfigToUI(cfg);
}

async function waitForHitlConfigReady(conversationId) {
    const cid = String(conversationId || '').trim();
    if (cid && hitlConfigSyncConversationId === cid) {
        await hitlConfigSyncPromise;
        return;
    }
    const defaultReady = window.csaiHitlDefaultConfigReady || window.csaiHitlDefaultReviewerReady;
    if (!cid && defaultReady && typeof defaultReady.then === 'function') {
        await defaultReady.catch(function () {});
        if (!currentConversationId) refreshHitlConfigByCurrentConversation();
    }
}

function showHitlApplyFeedback(text, isError, partial) {
    const el = document.getElementById('hitl-apply-feedback');
    if (hitlApplyFeedbackTimer) {
        clearTimeout(hitlApplyFeedbackTimer);
        hitlApplyFeedbackTimer = null;
    }
    if (!el) {
        if (text && isError) {
            showChatToast(text, 'error');
        }
        return;
    }
    el.classList.toggle('hitl-apply-feedback--error', !!isError);
    el.classList.toggle('hitl-apply-feedback--partial', !!partial && !isError);
    if (!text) {
        el.textContent = '';
        el.style.display = 'none';
        el.classList.remove('hitl-apply-feedback--error', 'hitl-apply-feedback--partial');
        return;
    }
    el.textContent = text;
    el.style.display = 'block';
    if (!isError) {
        hitlApplyFeedbackTimer = setTimeout(function () {
            el.textContent = '';
            el.style.display = 'none';
            el.classList.remove('hitl-apply-feedback--error');
            el.classList.remove('hitl-apply-feedback--partial');
            hitlApplyFeedbackTimer = null;
        }, 3200);
    }
}

/** 侧栏人机协同：自动写入本地、合并展示并尽量同步服务端 */
async function applyHitlSidebarConfig() {
    const btn = document.getElementById('hitl-apply-btn');
    showHitlApplyFeedback('', false);
    if (btn) btn.disabled = true;
    try {
        const cfg = readHitlConfigFromForm();
        const cid = typeof currentConversationId === 'string' ? currentConversationId.trim() : '';
        saveHitlConfigForConversation(cid, cfg, { syncGlobalLast: true });

        const toolsArr = hitlToolsSplitToArray(cfg.sensitiveTools || '');

        let yamlMerged = false;
        if (!cid && toolsArr.length > 0 && typeof window.mergeHitlGlobalToolWhitelist === 'function') {
            const newGlobal = await window.mergeHitlGlobalToolWhitelist(toolsArr);
            if (Array.isArray(newGlobal)) {
                window.csaiHitlGlobalToolWhitelist = newGlobal;
            }
            yamlMerged = true;
        }

        applyHitlConfigToUI(cfg);

        if (cid && typeof window.saveHitlConversationConfig === 'function') {
            await window.saveHitlConversationConfig(cid, cfg);
            const ok = typeof window.t === 'function' ? window.t('chat.hitlApplyOkSync') : '人机协同配置已保存并同步到服务器。';
            showHitlApplyFeedback(ok, false);
        } else if (typeof window.putHitlDefaultConfig === 'function') {
            await window.putHitlDefaultConfig(cfg);
            const okDefault = typeof window.t === 'function' ? window.t('chat.hitlApplyOkDefaultConfig') : '人机协同默认配置已写入 config.yaml 并生效。';
            showHitlApplyFeedback(okDefault, false);
        } else if (yamlMerged) {
            const okYaml = typeof window.t === 'function' ? window.t('chat.hitlApplyOkWhitelistYaml') : '免审批工具已合并进 config.yaml 并生效。会话配置会自动保存。';
            showHitlApplyFeedback(okYaml, false);
        } else {
            const localOnly = typeof window.t === 'function' ? window.t('chat.hitlApplyOkLocal') : '已保存到本浏览器。';
            showHitlApplyFeedback(localOnly, false);
        }
        if (typeof window.refreshHitlPageWhitelist === 'function') {
            window.refreshHitlPageWhitelist();
        }
    } catch (e) {
        logger.warn('applyHitlSidebarConfig', e);
        const prefix = typeof window.t === 'function' ? window.t('chat.hitlApplyFail') : '同步到服务器失败';
        const detail = (e && e.message) ? e.message : String(e);
        showHitlApplyFeedback(prefix + (detail ? '：' + detail : ''), true);
    } finally {
        if (btn) btn.disabled = false;
    }
}

function scheduleHitlSidebarAutosave(delayMs) {
    if (hitlAutoSaveTimer) {
        clearTimeout(hitlAutoSaveTimer);
        hitlAutoSaveTimer = null;
    }
    hitlAutoSaveTimer = setTimeout(function () {
        hitlAutoSaveTimer = null;
        applyHitlSidebarConfig();
    }, typeof delayMs === 'number' ? delayMs : 500);
}

function bindHitlSensitiveToolsAutosaveListener() {
    const toolsEl = document.getElementById('hitl-sensitive-tools');
    if (!toolsEl || toolsEl.dataset.hitlAutosaveBound === '1') return;
    toolsEl.dataset.hitlAutosaveBound = '1';
    toolsEl.addEventListener('input', function () {
        scheduleHitlSidebarAutosave(700);
    });
    toolsEl.addEventListener('blur', function () {
        scheduleHitlSidebarAutosave(0);
    });
}

/** 将 localStorage 规范为 eino_single | deep | plan_execute | supervisor */
function chatAgentModeNormalizeStored(stored, cfg) {
    const pub = cfg && cfg.multi_agent ? cfg.multi_agent : null;
    const multiOn = !!(pub && pub.enabled);
    const s = stored;
    if (chatAgentModeIsEinoSingle(s)) return s;
    if (chatAgentModeIsEino(s)) {
        return multiOn ? s : CHAT_AGENT_MODE_EINO_SINGLE;
    }
    return CHAT_AGENT_MODE_EINO_SINGLE;
}

function normalizeConversationAgentModeForUI(mode) {
    const v = String(mode || '').trim().toLowerCase().replace(/-/g, '_');
    if (chatAgentModeIsEinoSingle(v)) return v;
    if (chatAgentModeIsEino(v)) {
        return multiAgentAPIEnabled ? v : CHAT_AGENT_MODE_EINO_SINGLE;
    }
    return '';
}

function conversationAgentModeStorageKey(conversationId) {
    return `${AGENT_MODE_CONVERSATION_STORAGE_PREFIX}:${String(conversationId || '').trim()}`;
}

function readConversationAgentModePreference(conversationId) {
    if (!conversationId) return '';
    try {
        return normalizeConversationAgentModeForUI(localStorage.getItem(conversationAgentModeStorageKey(conversationId)) || '');
    } catch (e) {
        return '';
    }
}

function saveConversationAgentModePreference(conversationId, mode) {
    const normalized = normalizeConversationAgentModeForUI(mode);
    if (!conversationId || !normalized) return;
    try {
        localStorage.setItem(conversationAgentModeStorageKey(conversationId), normalized);
    } catch (e) { /* ignore */ }
}

function applyConversationAgentMode(conversationId, conversation) {
    const saved = readConversationAgentModePreference(conversationId);
    const fromServer = normalizeConversationAgentModeForUI(conversation && (conversation.agentMode || conversation.agent_mode));
    const mode = saved || fromServer;
    if (!mode) return;
    syncAgentModeFromValue(mode);
}

if (typeof window !== 'undefined') {
    window.csaiHitlGlobalToolWhitelist = window.csaiHitlGlobalToolWhitelist || [];
    window.csaiHitlDefaultConfig = window.csaiHitlDefaultConfig || {
        mode: HITL_MODE_OFF,
        reviewer: 'human',
        timeoutSeconds: DEFAULT_HITL_TIMEOUT_SECONDS
    };
    window.csaiHitlDefaultReviewer = window.csaiHitlDefaultReviewer || 'human';
    window.csaiChatAgentMode = {
        EINO_MODES: CHAT_AGENT_EINO_MODES,
        EINO_SINGLE: CHAT_AGENT_MODE_EINO_SINGLE,
        isEino: chatAgentModeIsEino,
        isEinoSingle: chatAgentModeIsEinoSingle,
        normalizeStored: chatAgentModeNormalizeStored,
        normalizeOrchestration: normalizeOrchestrationClient
    };
    window.applyHitlSidebarConfig = applyHitlSidebarConfig;
    window.readHitlConfigFromForm = readHitlConfigFromForm;
    window.applyHitlConfigToUI = applyHitlConfigToUI;
    window.refreshHitlConfigByCurrentConversation = refreshHitlConfigByCurrentConversation;
    window.saveHitlConfigForConversation = saveHitlConfigForConversation;
    window.getHitlConfigForConversation = getHitlConfigForConversation;
    bindHitlSidebarModeListener();
    bindHitlReviewerToggleListeners();
    bindHitlSensitiveToolsAutosaveListener();
    window.setHitlReviewerUI = setHitlReviewerUI;
    window.onHitlReviewerChanged = onHitlReviewerChanged;
    window.bindHitlReviewerToggleListeners = bindHitlReviewerToggleListeners;
    window.hitlMergeToolsForDisplay = hitlMergeToolsForDisplay;
    window.hitlStripGlobalToolsFromFormString = hitlStripGlobalToolsFromFormString;
    window.hitlToolsSplitToArray = hitlToolsSplitToArray;
    window.updateHitlStatusUI = updateHitlStatusUI;
}

function syncHitlSidebarAriaExpanded() {
    var card = document.getElementById('hitl-sidebar-card');
    var toggle = document.getElementById('hitl-sidebar-toggle');
    if (!card || !toggle) return;
    toggle.setAttribute('aria-expanded', card.classList.contains('hitl-sidebar-collapsed') ? 'false' : 'true');
}

function closeHitlSidebarCard() {
    var card = document.getElementById('hitl-sidebar-card');
    if (!card || card.classList.contains('hitl-sidebar-collapsed')) return;
    card.classList.add('hitl-sidebar-collapsed');
    syncHitlSidebarAriaExpanded();
    try {
        localStorage.setItem('hitl-sidebar-collapsed', '1');
    } catch (e) {}
}

function toggleHitlSidebarCard() {
    var card = document.getElementById('hitl-sidebar-card');
    if (!card) return;
    card.classList.toggle('hitl-sidebar-collapsed');
    syncHitlSidebarAriaExpanded();
    try {
        localStorage.setItem('hitl-sidebar-collapsed', card.classList.contains('hitl-sidebar-collapsed') ? '1' : '0');
    } catch (e) {}
}