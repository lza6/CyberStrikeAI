window.toggleHitlSidebarCard = toggleHitlSidebarCard;

document.addEventListener('DOMContentLoaded', function () {
    var card = document.getElementById('hitl-sidebar-card');
    if (card && localStorage.getItem('hitl-sidebar-collapsed') === '0') {
        card.classList.remove('hitl-sidebar-collapsed');
    }
    syncHitlSidebarAriaExpanded();
});

function getAgentModeLabelForValue(mode) {
    if (typeof window.t === 'function') {
        switch (mode) {
            case 'deep':
                return window.t('chat.agentModeDeep');
            case 'plan_execute':
                return window.t('chat.agentModePlanExecuteLabel');
            case 'supervisor':
                return window.t('chat.agentModeSupervisorLabel');
            case CHAT_AGENT_MODE_EINO_SINGLE:
                return window.t('chat.agentModeEinoSingle');
            default:
                return mode;
        }
    }
    switch (mode) {
        case CHAT_AGENT_MODE_EINO_SINGLE: return 'Eino 单代理';
        case 'deep': return 'Deep';
        case 'plan_execute': return 'Plan-Execute';
        case 'supervisor': return 'Supervisor';
        default: return mode;
    }
}

function getAgentModeIconClassForValue(mode) {
    switch (mode) {
        case CHAT_AGENT_MODE_EINO_SINGLE: return 'eino';
        case 'deep': return 'deep';
        case 'plan_execute': return 'plan';
        case 'supervisor': return 'supervisor';
        default: return 'default';
    }
}

function renderAgentModeLogoMarkup() {
    return '<svg class="agent-mode-logo__svg" viewBox="0 0 24 24" fill="none" aria-hidden="true"><rect x="3" y="11" width="18" height="10" rx="2"/><circle cx="12" cy="5" r="2"/><path d="M12 7v4"/><path d="M8 16h.01"/><path d="M16 16h.01"/></svg>';
}

function syncAgentModeFromValue(value) {
    const hid = document.getElementById('agent-mode-select');
    const label = document.getElementById('agent-mode-text');
    const icon = document.getElementById('agent-mode-icon');
    if (hid) hid.value = value;
    if (label) label.textContent = getAgentModeLabelForValue(value);
    if (icon) {
        icon.className = 'role-selector-icon agent-mode-logo agent-mode-logo--' + getAgentModeIconClassForValue(value);
        icon.innerHTML = renderAgentModeLogoMarkup();
    }
    document.querySelectorAll('.agent-mode-option').forEach(function (el) {
        const v = el.getAttribute('data-value');
        el.classList.toggle('selected', v === value);
    });
    syncReasoningRowVisibility(value);
}

function syncReasoningRowVisibility(modeVal) {
    mountChatSessionSettingsPopover();
    const wrap = document.getElementById('chat-reasoning-wrapper');
    if (!wrap) return;
    const show = modeVal === CHAT_AGENT_MODE_EINO_SINGLE || (multiAgentAPIEnabled && chatAgentModeIsEino(modeVal));
    wrap.style.display = show ? '' : 'none';
    if (!show) {
        closeChatReasoningPanel();
    } else {
        syncChatReasoningBarHeight();
        updateChatReasoningSummary();
    }
}

function normalizeChatAIChannelId(s) {
    return String(s || '').trim().toLowerCase().replace(/_/g, '-').replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
}

function resolveChatAIChannelId(id) {
    const raw = String(id || '').trim();
    if (!raw) return '';
    if (chatAIChannels[raw]) return raw;
    const normalized = normalizeChatAIChannelId(raw);
    return normalized && chatAIChannelIdByNormalizedId[normalized] ? chatAIChannelIdByNormalizedId[normalized] : '';
}

function populateChatAIChannelSelect(ai) {
    const select = document.getElementById('chat-ai-channel-select');
    if (!select) return;
    const cfg = ai && typeof ai === 'object' ? ai : {};
    chatAIChannels = cfg.channels && typeof cfg.channels === 'object' ? cfg.channels : {};
    chatAIChannelIdByNormalizedId = {};
    Object.keys(chatAIChannels).forEach(function (id) {
        const normalized = normalizeChatAIChannelId(id);
        if (normalized && !chatAIChannelIdByNormalizedId[normalized]) {
            chatAIChannelIdByNormalizedId[normalized] = id;
        }
    });
    chatDefaultAIChannel = resolveChatAIChannelId(cfg.default_channel || '');
    select.innerHTML = '';
    const fallbackOpt = document.createElement('option');
    fallbackOpt.value = '';
    fallbackOpt.textContent = typeof window.t === 'function' ? window.t('chat.aiChannelDefault') : '跟随默认通道';
    select.appendChild(fallbackOpt);
    Object.keys(chatAIChannels).sort().forEach(function (id) {
        const ch = chatAIChannels[id] || {};
        const opt = document.createElement('option');
        opt.value = id;
        opt.textContent = (ch.name || id) + (ch.model ? ' · ' + ch.model : '');
        select.appendChild(opt);
    });
    let stored = '';
    try { stored = localStorage.getItem(AI_CHANNEL_STORAGE_KEY) || ''; } catch (e) {}
    stored = resolveChatAIChannelId(stored);
    select.value = stored || '';
    refreshSessionSettingsSelects();
    updateChatReasoningSummary();
}

function selectedChatAIChannelId() {
    const select = document.getElementById('chat-ai-channel-select');
    return resolveChatAIChannelId(select ? select.value : '');
}

function currentChatAIChannelLabel() {
    const id = selectedChatAIChannelId() || chatDefaultAIChannel;
    const ch = id ? chatAIChannels[id] : null;
    if (!ch) {
        return chatTranslate('chat.aiChannelDefaultShort', '默认通道');
    }
    return ch.name || id;
}

function currentChatModelLabel() {
    const id = selectedChatAIChannelId() || chatDefaultAIChannel;
    const ch = id ? chatAIChannels[id] : null;
    const model = ch && typeof ch.model === 'string' ? ch.model.trim() : '';
    return model || currentChatAIChannelLabel();
}

function currentSystemModelLabel() {
    const ch = chatDefaultAIChannel ? chatAIChannels[chatDefaultAIChannel] : null;
    const model = ch && typeof ch.model === 'string' ? ch.model.trim() : '';
    return model || (ch && (ch.name || chatDefaultAIChannel)) || currentChatModelLabel();
}

function currentHitlAuditModelLabel() {
    return chatHitlAuditModelName || currentSystemModelLabel();
}

function resolveChatPickerChannelId() {
    return selectedChatAIChannelId() || chatDefaultAIChannel;
}

function chatSystemModelConfigState(cfg, preferredChannelId) {
    const source = cfg && typeof cfg === 'object' ? cfg : {};
    const sourceAI = source.ai && typeof source.ai === 'object' ? source.ai : {};
    const channels = sourceAI.channels && typeof sourceAI.channels === 'object'
        ? { ...sourceAI.channels }
        : {};
    const resolveFromChannels = function (value) {
        const raw = String(value || '').trim();
        if (raw && channels[raw]) return raw;
        const normalized = normalizeChatAIChannelId(raw);
        return normalized
            ? Object.keys(channels).find(function (id) {
                return normalizeChatAIChannelId(id) === normalized;
            }) || ''
            : '';
    };
    let defaultChannelId = resolveFromChannels(sourceAI.default_channel);
    let channelId = resolveFromChannels(preferredChannelId) || defaultChannelId;
    if (!channels[channelId]) {
        channelId = Object.keys(channels)[0] || 'default';
    }
    if (!channels[channelId]) {
        const legacy = source.openai && typeof source.openai === 'object' ? source.openai : {};
        channels[channelId] = {
            name: channelId === 'default' ? 'Default' : channelId,
            provider: legacy.provider || 'openai',
            api_key: legacy.api_key || '',
            base_url: legacy.base_url || '',
            model: legacy.model || ''
        };
    }
    if (!defaultChannelId) defaultChannelId = channelId;
    return {
        ai: { ...sourceAI, default_channel: defaultChannelId, channels: channels },
        channelId: channelId,
        channel: channels[channelId]
    };
}

function chatSystemModelElements() {
    return {
        wrap: document.getElementById('chat-model-shortcut-wrap'),
        button: document.getElementById('chat-model-shortcut'),
        menu: document.getElementById('chat-system-model-menu'),
        main: document.getElementById('chat-system-model-main'),
        subview: document.getElementById('chat-system-model-subview'),
        subviewTitle: document.getElementById('chat-system-model-subview-title'),
        list: document.getElementById('chat-system-model-list'),
        status: document.getElementById('chat-system-model-status'),
        subviewStatus: document.getElementById('chat-system-model-subview-status'),
        channelValue: document.getElementById('chat-system-model-channel-value'),
        currentValue: document.getElementById('chat-system-model-current-value'),
        modeValue: document.getElementById('chat-system-model-mode-value'),
        effortValue: document.getElementById('chat-system-model-effort-value')
    };
}

function setChatSystemModelStatus(message, tone) {
    const ui = chatSystemModelElements();
    [ui.status, ui.subviewStatus].forEach(function (status) {
        if (!status) return;
        status.textContent = message || '';
        status.dataset.tone = tone || '';
    });
}

function chatReasoningEffortLabel(value) {
    switch (String(value || '').trim()) {
        case 'low': return 'low';
        case 'medium': return 'medium';
        case 'high': return 'high';
        case 'xhigh': return 'xhigh';
        case 'max': return 'max';
        default: return chatTranslate('chat.reasoningEffortUnset', '不指定');
    }
}

function currentChatReasoningEffort() {
    const effort = document.getElementById('chat-reasoning-effort');
    return effort ? String(effort.value || '').trim() : '';
}

function currentChatReasoningMode() {
    const mode = document.getElementById('chat-reasoning-mode');
    const value = mode ? String(mode.value || 'default').trim() : 'default';
    return ['default', 'off', 'on', 'auto'].includes(value) ? value : 'default';
}

function currentChatReasoningMenuLabel() {
    const modeValue = currentChatReasoningMode();
    if (modeValue === 'off') return chatTranslate('chat.reasoningModeOff', '关闭');
    const effort = currentChatReasoningEffort();
    return effort ? chatReasoningEffortLabel(effort) : reasoningSummaryModeLabel(modeValue);
}

function updateChatSystemModelPickerValues() {
    const ui = chatSystemModelElements();
    const channel = currentChatAIChannelLabel();
    const model = currentChatModelLabel();
    const mode = reasoningSummaryModeLabel(currentChatReasoningMode());
    const effort = currentChatReasoningMenuLabel();
    if (ui.channelValue) ui.channelValue.textContent = channel;
    if (ui.currentValue) ui.currentValue.textContent = model;
    if (ui.modeValue) ui.modeValue.textContent = mode;
    if (ui.effortValue) ui.effortValue.textContent = effort;
    const composerEffort = document.getElementById('chat-model-shortcut-effort');
    if (composerEffort) composerEffort.textContent = effort;
}

function closeChatSystemModelPicker(force) {
    if (chatSystemModelSaving && !force) return;
    const ui = chatSystemModelElements();
    if (ui.menu) ui.menu.hidden = true;
    if (ui.button) {
        ui.button.classList.remove('active');
        ui.button.setAttribute('aria-expanded', 'false');
    }
    if (ui.main) ui.main.hidden = false;
    if (ui.subview) ui.subview.hidden = true;
    chatSystemModelRequestSeq += 1;
}

async function readChatSystemModelError(response, fallback) {
    try {
        const body = await response.json();
        return body.error || body.message || fallback;
    } catch (_) {
        return fallback;
    }
}

function renderChatSystemModelOptions(models, currentModel) {
    const ui = chatSystemModelElements();
    if (!ui.list) return 0;
    ui.list.innerHTML = '';
    const unique = [];
    const seen = new Set();
    [currentModel].concat(Array.isArray(models) ? models : []).forEach(function (value) {
        const model = String(value || '').trim();
        if (!model || seen.has(model)) return;
        seen.add(model);
        unique.push(model);
    });
    unique.forEach(function (model) {
        const option = document.createElement('button');
        option.type = 'button';
        option.className = 'chat-system-model-option';
        option.setAttribute('role', 'option');
        option.setAttribute('aria-selected', model === currentModel ? 'true' : 'false');
        option.dataset.model = model;

        const label = document.createElement('span');
        label.className = 'chat-system-model-option-label';
        label.textContent = model;
        option.appendChild(label);

        if (model === currentModel) {
            option.classList.add('is-selected');
            const current = document.createElement('span');
            current.className = 'chat-system-model-current';
            current.textContent = chatTranslate('chat.systemModelCurrent', '当前');
            option.appendChild(current);
        }
        option.addEventListener('click', function (event) {
            event.preventDefault();
            event.stopPropagation();
            selectChatSystemModel(model);
        });
        ui.list.appendChild(option);
    });
    return unique.length;
}

function renderChatReasoningEffortOptions() {
    const ui = chatSystemModelElements();
    if (!ui.list) return;
    ui.list.innerHTML = '';
    const currentEffort = currentChatReasoningEffort();
    ['', 'low', 'medium', 'high', 'xhigh', 'max'].forEach(function (effort) {
        const option = document.createElement('button');
        option.type = 'button';
        option.className = 'chat-system-model-option';
        option.setAttribute('role', 'option');
        option.setAttribute('aria-selected', effort === currentEffort ? 'true' : 'false');
        if (effort === currentEffort) option.classList.add('is-selected');

        const label = document.createElement('span');
        label.className = 'chat-system-model-option-label chat-system-effort-option-label';
        label.textContent = chatReasoningEffortLabel(effort);
        option.appendChild(label);

        if (effort === currentEffort) {
            const current = document.createElement('span');
            current.className = 'chat-system-model-current';
            current.textContent = chatTranslate('chat.systemModelCurrent', '当前');
            option.appendChild(current);
        }
        option.addEventListener('click', function (event) {
            event.preventDefault();
            event.stopPropagation();
            selectChatReasoningEffort(effort);
        });
        ui.list.appendChild(option);
    });
}

function renderChatReasoningModeOptions() {
    const ui = chatSystemModelElements();
    if (!ui.list) return;
    ui.list.innerHTML = '';
    const currentMode = currentChatReasoningMode();
    ['default', 'off', 'on', 'auto'].forEach(function (mode) {
        const option = document.createElement('button');
        option.type = 'button';
        option.className = 'chat-system-model-option';
        option.setAttribute('role', 'option');
        option.setAttribute('aria-selected', mode === currentMode ? 'true' : 'false');
        if (mode === currentMode) option.classList.add('is-selected');
        const label = document.createElement('span');
        label.className = 'chat-system-model-option-label chat-system-effort-option-label';
        label.textContent = reasoningSummaryModeLabel(mode);
        option.appendChild(label);
        if (mode === currentMode) {
            const current = document.createElement('span');
            current.className = 'chat-system-model-current';
            current.textContent = chatTranslate('chat.systemModelCurrent', '当前');
            option.appendChild(current);
        }
        option.addEventListener('click', function (event) {
            event.preventDefault();
            event.stopPropagation();
            selectChatReasoningMode(mode);
        });
        ui.list.appendChild(option);
    });
}

function finishChatReasoningPickerUpdate() {
    persistChatReasoningPrefs();
    setChatSystemModelStatus(chatTranslate('chat.reasoningSessionUpdated', '会话推理设置已更新'), 'success');
    if (chatSystemModelCloseTimer) window.clearTimeout(chatSystemModelCloseTimer);
    chatSystemModelCloseTimer = window.setTimeout(function () {
        chatSystemModelSaving = false;
        closeChatSystemModelPicker(true);
    }, 450);
}

function selectChatReasoningMode(mode) {
    if (chatSystemModelSaving) return;
    const chosen = ['default', 'off', 'on', 'auto'].includes(String(mode || '').trim())
        ? String(mode || '').trim()
        : 'default';
    const modeControl = document.getElementById('chat-reasoning-mode');
    if (!modeControl) return;
    chatSystemModelSaving = true;
    modeControl.value = chosen;
    finishChatReasoningPickerUpdate();
}

function selectChatReasoningEffort(effort) {
    if (chatSystemModelSaving) return;
    const raw = String(effort || '').trim();
    const chosen = ['', 'low', 'medium', 'high', 'xhigh', 'max'].includes(raw) ? raw : '';
    const effortControl = document.getElementById('chat-reasoning-effort');
    if (!effortControl) return;
    chatSystemModelSaving = true;
    effortControl.value = chosen;
    finishChatReasoningPickerUpdate();
}

function renderChatSystemModelRetry() {
    const ui = chatSystemModelElements();
    if (!ui.list) return;
    ui.list.innerHTML = '';
    const retry = document.createElement('button');
    retry.type = 'button';
    retry.className = 'chat-system-model-retry';
    retry.textContent = chatTranslate('chat.systemModelRetry', '重新获取');
    retry.addEventListener('click', function (retryEvent) {
        retryEvent.preventDefault();
        retryEvent.stopPropagation();
        fetchChatSystemModelsForChannel(resolveChatPickerChannelId(), { force: true });
    });
    ui.list.appendChild(retry);
}

function renderChatAIChannelOptions() {
    const ui = chatSystemModelElements();
    if (!ui.list) return;
    ui.list.innerHTML = '';
    const selected = selectedChatAIChannelId();
    const choices = [{ id: '', label: chatTranslate('chat.aiChannelDefault', '跟随默认通道') }]
        .concat(Object.keys(chatAIChannels).sort().map(function (id) {
            const channel = chatAIChannels[id] || {};
            return { id: id, label: channel.name || id };
        }));
    choices.forEach(function (choice) {
        const option = document.createElement('button');
        option.type = 'button';
        option.className = 'chat-system-model-option';
        option.setAttribute('role', 'option');
        option.setAttribute('aria-selected', choice.id === selected ? 'true' : 'false');
        if (choice.id === selected) option.classList.add('is-selected');
        const label = document.createElement('span');
        label.className = 'chat-system-model-option-label chat-system-channel-option-label';
        label.textContent = choice.label;
        option.appendChild(label);
        if (choice.id === selected) {
            const current = document.createElement('span');
            current.className = 'chat-system-model-current';
            current.textContent = chatTranslate('chat.systemModelCurrent', '当前');
            option.appendChild(current);
        }
        option.addEventListener('click', function (event) {
            event.preventDefault();
            event.stopPropagation();
            selectChatAIChannel(choice.id);
        });
        ui.list.appendChild(option);
    });
}

async function selectChatAIChannel(channelId) {
    const select = document.getElementById('chat-ai-channel-select');
    if (!select) return;
    const resolved = resolveChatAIChannelId(channelId);
    select.value = resolved || '';
    persistChatAIChannelPref();
    refreshSessionSettingsSelects();
    updateChatSystemModelPickerValues();
    openChatSystemModelView('main');
    setChatSystemModelStatus(chatTranslate('chat.systemModelLoading', '正在获取模型列表…'), 'loading');
    await fetchChatSystemModelsForChannel(resolveChatPickerChannelId(), { force: true });
}

function openChatSystemModelView(view, event) {
    if (event) {
        event.preventDefault();
        event.stopPropagation();
    }
    const ui = chatSystemModelElements();
    if (!ui.main || !ui.subview || !ui.list) return;
    if (view === 'main') {
        ui.main.hidden = false;
        ui.subview.hidden = true;
        updateChatSystemModelPickerValues();
        return;
    }
    ui.main.hidden = true;
    ui.subview.hidden = false;
    ui.subview.dataset.view = view;
    if (view === 'channel') {
        if (ui.subviewTitle) ui.subviewTitle.textContent = chatTranslate('chat.aiChannelLabel', 'AI 通道');
        setChatSystemModelStatus('', '');
        renderChatAIChannelOptions();
        return;
    }
    if (view === 'mode') {
        if (ui.subviewTitle) ui.subviewTitle.textContent = chatTranslate('chat.reasoningModeLabel', '推理模式');
        setChatSystemModelStatus('', '');
        renderChatReasoningModeOptions();
        return;
    }
    if (view === 'effort') {
        if (ui.subviewTitle) ui.subviewTitle.textContent = chatTranslate('chat.reasoningEffortLabel', '推理强度');
        setChatSystemModelStatus('', '');
        renderChatReasoningEffortOptions();
        return;
    }
    if (ui.subviewTitle) ui.subviewTitle.textContent = chatTranslate('chat.systemModelField', '模型');
    if (chatSystemModelOptions.length) {
        const count = renderChatSystemModelOptions(chatSystemModelOptions, chatSystemModelCurrent);
        setChatSystemModelStatus(
            chatTranslate('chat.systemModelLoaded', '已获取 {count} 个模型').replace('{count}', String(count)),
            'success'
        );
    } else if (chatSystemModelLoadError) {
        renderChatSystemModelRetry();
        setChatSystemModelStatus(chatSystemModelLoadError, 'error');
    } else {
        ui.list.innerHTML = '';
        setChatSystemModelStatus(chatTranslate('chat.systemModelLoading', '正在获取模型列表…'), 'loading');
    }
}

async function selectChatSystemModel(model) {
    if (chatSystemModelSaving) return;
    if (typeof requirePermission === 'function' && !requirePermission('config:write')) return;
    const chosen = String(model || '').trim();
    if (!chosen) return;
    const ui = chatSystemModelElements();
    chatSystemModelSaving = true;
    if (ui.list) {
        ui.list.querySelectorAll('button').forEach(function (button) { button.disabled = true; });
    }
    setChatSystemModelStatus(chatTranslate('chat.systemModelSaving', '正在保存…'), 'loading');
    try {
        const latestResponse = await apiFetch('/api/config');
        if (!latestResponse.ok) {
            throw new Error(await readChatSystemModelError(latestResponse, chatTranslate('chat.systemModelSaveFailed', '保存失败')));
        }
        const latest = await latestResponse.json();
        const state = chatSystemModelConfigState(latest, resolveChatPickerChannelId());
        state.ai.channels[state.channelId] = { ...state.channel, model: chosen };
        const updateResponse = await apiFetch('/api/config', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ai: state.ai })
        });
        if (!updateResponse.ok) {
            throw new Error(await readChatSystemModelError(updateResponse, chatTranslate('chat.systemModelSaveFailed', '保存失败')));
        }
        const applyResponse = await apiFetch('/api/config/apply', { method: 'POST' });
        if (!applyResponse.ok) {
            throw new Error(await readChatSystemModelError(applyResponse, chatTranslate('chat.systemModelApplyFailed', '应用模型失败')));
        }
        chatAIChannels = state.ai.channels;
        chatDefaultAIChannel = resolveChatAIChannelId(state.ai.default_channel) || state.channelId;
        updateChatComposerSessionShortcuts();
        await initChatAgentModeFromConfig();
        chatSystemModelCurrent = chosen;
        chatSystemModelOptions = [chosen].concat(chatSystemModelOptions);
        updateChatSystemModelPickerValues();
        renderChatSystemModelOptions(chatSystemModelOptions, chosen);
        setChatSystemModelStatus(chatTranslate('chat.systemModelSaved', '已自动保存'), 'success');
        if (chatSystemModelCloseTimer) window.clearTimeout(chatSystemModelCloseTimer);
        chatSystemModelCloseTimer = window.setTimeout(function () {
            chatSystemModelSaving = false;
            closeChatSystemModelPicker(true);
        }, 650);
        return;
    } catch (error) {
        logger.error('selectChatSystemModel', error);
        setChatSystemModelStatus(error.message || chatTranslate('chat.systemModelSaveFailed', '保存失败'), 'error');
    }
    chatSystemModelSaving = false;
    if (ui.list) {
        ui.list.querySelectorAll('button').forEach(function (button) { button.disabled = false; });
    }
}

function chatSystemModelCacheKey(channelId, channel) {
    return [
        String(channelId || ''),
        String(channel && channel.provider || 'openai'),
        String(channel && channel.base_url || '').trim()
    ].join('|');
}

async function fetchChatSystemModelsForChannel(channelId, options) {
    const opts = options || {};
    const ui = chatSystemModelElements();
    if (!ui.menu || !ui.list || ui.menu.hidden) return;
    const resolvedChannelId = resolveChatAIChannelId(channelId) || chatDefaultAIChannel;
    const channel = resolvedChannelId ? chatAIChannels[resolvedChannelId] || {} : {};
    const cacheKey = chatSystemModelCacheKey(resolvedChannelId, channel);
    const cached = chatSystemModelCache.get(cacheKey);
    const requestId = ++chatSystemModelRequestSeq;
    chatSystemModelCurrent = String(channel.model || '').trim();
    chatSystemModelOptions = [];
    chatSystemModelLoadError = '';
    if (!opts.force && cached && Date.now() - cached.fetchedAt < CHAT_SYSTEM_MODEL_CACHE_TTL_MS) {
        chatSystemModelOptions = cached.models.slice();
        if (ui.subview && !ui.subview.hidden && ui.subview.dataset.view === 'model') {
            renderChatSystemModelOptions(chatSystemModelOptions, chatSystemModelCurrent);
        }
        const cachedCount = [chatSystemModelCurrent].concat(chatSystemModelOptions)
            .map(function (model) { return String(model || '').trim(); })
            .filter(function (model, index, all) { return model && all.indexOf(model) === index; })
            .length;
        setChatSystemModelStatus(
            chatTranslate('chat.systemModelLoaded', '已获取 {count} 个模型').replace('{count}', String(cachedCount)),
            'success'
        );
        return;
    }
    if (ui.subview && !ui.subview.hidden && ui.subview.dataset.view === 'model') {
        ui.list.innerHTML = '';
    }
    setChatSystemModelStatus(chatTranslate('chat.systemModelLoading', '正在获取模型列表…'), 'loading');
    try {
        if (!String(channel.api_key || '').trim()) {
            throw new Error(chatTranslate('chat.systemModelNeedApiKey', '请先在系统设置中配置 API Key'));
        }
        const listResponse = await apiFetch('/api/config/list-models', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                provider: channel.provider || 'openai',
                base_url: String(channel.base_url || '').trim(),
                api_key: String(channel.api_key || '').trim()
            })
        });
        const result = await listResponse.json().catch(function () { return {}; });
        if (!listResponse.ok || !result.success) {
            throw new Error(result.error || chatTranslate('chat.systemModelLoadFailed', '获取模型失败'));
        }
        if (requestId !== chatSystemModelRequestSeq || ui.menu.hidden) return;
        chatSystemModelOptions = Array.isArray(result.models) ? result.models.slice() : [];
        chatSystemModelCache.set(cacheKey, {
            models: chatSystemModelOptions.slice(),
            fetchedAt: Date.now()
        });
        const count = [chatSystemModelCurrent].concat(chatSystemModelOptions)
            .map(function (model) { return String(model || '').trim(); })
            .filter(function (model, index, all) { return model && all.indexOf(model) === index; })
            .length;
        if (ui.subview && !ui.subview.hidden && ui.subview.dataset.view === 'model') {
            renderChatSystemModelOptions(chatSystemModelOptions, chatSystemModelCurrent);
        }
        setChatSystemModelStatus(
            chatTranslate('chat.systemModelLoaded', '已获取 {count} 个模型').replace('{count}', String(count)),
            'success'
        );
    } catch (error) {
        if (requestId !== chatSystemModelRequestSeq || ui.menu.hidden) return;
        chatSystemModelLoadError = error.message || chatTranslate('chat.systemModelLoadFailed', '获取模型失败');
        if (ui.subview && !ui.subview.hidden && ui.subview.dataset.view === 'model') {
            renderChatSystemModelRetry();
        }
        setChatSystemModelStatus(chatSystemModelLoadError, 'error');
    }
}

async function openChatSystemModelPicker(event) {
    if (event) {
        event.preventDefault();
        event.stopPropagation();
    }
    const ui = chatSystemModelElements();
    if (!ui.menu || !ui.button || !ui.list) return;
    if (!ui.menu.hidden) {
        closeChatSystemModelPicker();
        return;
    }
    if (chatSystemModelCloseTimer) {
        window.clearTimeout(chatSystemModelCloseTimer);
        chatSystemModelCloseTimer = null;
    }
    if (typeof closeChatReasoningPanel === 'function') closeChatReasoningPanel();
    ui.menu.hidden = false;
    ui.button.classList.add('active');
    ui.button.setAttribute('aria-expanded', 'true');
    if (ui.main) ui.main.hidden = false;
    if (ui.subview) ui.subview.hidden = true;
    ui.list.innerHTML = '';
    updateChatSystemModelPickerValues();
    await fetchChatSystemModelsForChannel(resolveChatPickerChannelId());
}

function truncateChatAIChannelSummaryLabel(label) {
    const chars = Array.from(String(label || ''));
    if (chars.length <= CHAT_AI_CHANNEL_SUMMARY_NAME_MAX) return chars.join('');
    return chars.slice(0, CHAT_AI_CHANNEL_SUMMARY_NAME_MAX).join('') + '...';
}

function persistChatAIChannelPref() {
    const id = selectedChatAIChannelId();
    try {
        if (id) localStorage.setItem(AI_CHANNEL_STORAGE_KEY, id);
        else localStorage.removeItem(AI_CHANNEL_STORAGE_KEY);
    } catch (e) {}
    updateChatReasoningSummary();
    updateChatSystemModelPickerValues();
}

function reasoningSummaryModeLabel(mode) {
    const m = (mode || 'default').trim();
    switch (m) {
        case 'off': return chatTranslate('chat.reasoningModeOff', '关闭');
        case 'on': return chatTranslate('chat.reasoningModeOn', '开启');
        case 'auto': return chatTranslate('chat.reasoningModeAuto', '自动');
        default: return chatTranslate('chat.reasoningSummaryFollow', '系统');
    }
}

function updateChatReasoningSummary() {
    const el = document.getElementById('chat-reasoning-summary');
    const modeEl = document.getElementById('chat-reasoning-mode');
    const effEl = document.getElementById('chat-reasoning-effort');
    if (!el || !modeEl) return;
    const mode = (modeEl.value || 'default').trim();
    const effort = effEl && effEl.value ? String(effEl.value).trim() : '';
    const t = (typeof window.t === 'function') ? window.t : function (k) { return k; };
    const modePart = reasoningSummaryModeLabel(mode);
    const reasoningPart = effort || modePart || t('chat.reasoningSummaryDash');
    let hitlPart = '';
    try {
        const hitlCfg = readHitlConfigFromForm();
        hitlPart = getHitlModeLabel(hitlCfg.mode);
    } catch (e) {
        hitlPart = '';
    }
    const channelPart = currentChatAIChannelLabel();
    const modelPart = currentChatModelLabel();
    el.textContent = hitlPart;
    el.title = hitlPart;
    updateChatComposerSessionShortcuts({
        channel: channelPart,
        model: modelPart,
        reasoning: reasoningPart,
        hitl: hitlPart
    });
}

function updateChatComposerSessionShortcuts(summary) {
    const data = summary || {};
    const modelEl = document.getElementById('chat-model-shortcut-text');
    const hitlEl = document.getElementById('chat-hitl-shortcut-text');
    if (modelEl) {
        // 输入框右侧展示当前会话通道的模型；审批模型只出现在 HITL 入口。
        const label = currentChatModelLabel();
        modelEl.textContent = truncateChatAIChannelSummaryLabel(label);
        modelEl.title = label;
        const shortcut = document.getElementById('chat-model-shortcut');
        if (shortcut) {
            const channel = currentChatAIChannelLabel();
            const effort = currentChatReasoningMenuLabel();
            const action = chatTranslate('chat.modelSettingsAria', '选择 AI 通道、模型与推理设置');
            shortcut.setAttribute('aria-label', action + '：' + channel + ' · ' + label + ' · ' + effort);
            shortcut.title = action + '：' + channel + ' · ' + label + ' · ' + effort;
        }
        updateChatSystemModelPickerValues();
    }
    if (hitlEl) {
        const cfg = readHitlConfigFromForm();
        const auditAgent = normalizeHitlReviewer(cfg.reviewer) === 'audit_agent';
        const prefix = auditAgent
            ? chatTranslate('chat.sessionShortcutAuditAgent', 'Agent 审查')
            : chatTranslate('chat.sessionShortcutHuman', '人工审批');
        const modeLabel = data.hitl || getHitlModeLabel(cfg.mode);
        const approvalModel = auditAgent ? currentHitlAuditModelLabel() : '';
        const label = prefix + '：' + modeLabel + (approvalModel ? ' · ' + approvalModel : '');
        hitlEl.textContent = label;
        hitlEl.title = label;
    }
}

function openChatSessionSettings(section, event) {
    if (event && typeof event.stopPropagation === 'function') event.stopPropagation();
    mountChatSessionSettingsPopover();
    const wrap = document.getElementById('chat-reasoning-wrapper');
    const toggle = document.getElementById('conversation-reasoning-toggle');
    if (!wrap || !toggle || wrap.style.display === 'none') return;
    syncChatReasoningBarHeight();
    wrap.classList.remove('conversation-reasoning-collapsed');
    syncChatSessionSettingsLayerState();
    toggle.setAttribute('aria-expanded', 'true');
    if (typeof closeAgentModePanel === 'function') closeAgentModePanel();
    if (typeof closeRoleSelectionPanel === 'function') closeRoleSelectionPanel();
    if (typeof closeChatProjectPanel === 'function') closeChatProjectPanel();
    updateChatReasoningSummary();

    let target = null;
    if (section === 'hitl') target = document.getElementById('hitl-mode-select');
    else if (section === 'reasoning') target = document.getElementById('chat-reasoning-mode');
    else target = document.getElementById('chat-ai-channel-select');
    const group = target && target.closest('.session-settings-group');
    if (group && typeof group.scrollIntoView === 'function') {
        group.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    }
    const customTrigger = target && target.closest('.session-settings-select')
        ? target.closest('.session-settings-select').querySelector('.session-settings-select-trigger')
        : null;
    window.setTimeout(function () {
        if (customTrigger) customTrigger.focus({ preventScroll: true });
        else if (target) target.focus({ preventScroll: true });
    }, 180);
}

function getVisibleChatConversationId() {
    return typeof currentConversationId === 'string' && currentConversationId.trim()
        ? currentConversationId.trim()
        : '';
}

function shouldTreatLiveChatTaskAsCurrent(liveConversationId, visibleConversationId, hasVisibleProgress) {
    const liveId = String(liveConversationId || '').trim();
    const visibleId = String(visibleConversationId || '').trim();
    if (liveId) return !!visibleId && liveId === visibleId;
    return hasVisibleProgress === true;
}

function ownsLiveChatStream(liveStream) {
    return !!liveStream && window.__csAgentLiveStream === liveStream;
}

function shouldIgnoreLiveChatStreamEvent(
    liveStream,
    activeLiveStream = window.__csAgentLiveStream,
    navigationSeq = chatConversationNavigationSeq
) {
    return !liveStream ||
        activeLiveStream !== liveStream ||
        liveStream.active !== true ||
        liveStream.detached === true ||
        liveStream.navigationSeq !== navigationSeq;
}

function clearLiveChatStreamIfOwned(liveStream) {
    if (!ownsLiveChatStream(liveStream)) return false;
    liveStream.active = false;
    window.__csAgentLiveStream = { active: false, conversationId: null, progressId: null };
    updateChatPrimaryActionState();
    return true;
}

/**
 * 离开正在读取主 POST 流的对话时，只断开浏览器侧响应流，不停止后端任务。
 * 后端任务使用 detachedAgentContext，仍会继续运行；重新进入该对话时由
 * task-events 镜像流接管。这样同时运行多个对话也只占用一个前台长连接，
 * 不会耗尽浏览器对同一主机的连接槽位而卡住普通 GET/POST 请求。
 */
function detachLiveChatStreamForNavigation(nextConversationId, force = false) {
    const liveStream = window.__csAgentLiveStream;
    if (!liveStream || !liveStream.active) return false;
    const liveConversationId = String(liveStream.conversationId || '').trim();
    const nextId = String(nextConversationId || '').trim();
    if (!force && liveConversationId && liveConversationId === nextId) return false;
    if (!force && !liveConversationId && !nextId) return false;

    liveStream.detached = true;
    liveStream.active = false;
    const controller = liveStream.abortController;
    if (controller && !controller.signal.aborted) {
        controller.abort();
    }
    if (ownsLiveChatStream(liveStream)) {
        updateChatPrimaryActionState();
    }
    return true;
}

function cancelPendingConversationLoad() {
    if (!loadConversationAbortController) return false;
    if (!loadConversationAbortController.signal.aborted) {
        loadConversationAbortController.abort();
    }
    loadConversationAbortController = null;
    return true;
}

function isLiveChatTaskVisible(live, visibleConversationId) {
    if (!live || !live.active) return false;
    const progress = live.progressId ? document.getElementById(live.progressId) : null;
    const hasVisibleProgress = !!(progress && progress.closest('#chat-messages'));
    return shouldTreatLiveChatTaskAsCurrent(
        live.conversationId,
        visibleConversationId,
        hasVisibleProgress
    );
}

function getCurrentChatTaskConversationId() {
    const visibleConversationId = getVisibleChatConversationId();
    if (visibleConversationId) return visibleConversationId;
    return '';
}

function isCurrentChatTaskActive() {
    const live = window.__csAgentLiveStream;
    const visibleConversationId = getVisibleChatConversationId();
    if (isLiveChatTaskVisible(live, visibleConversationId)) return true;
    return !!visibleConversationId &&
        typeof isConversationTaskRunning === 'function' &&
        isConversationTaskRunning(visibleConversationId);
}

function updateChatPrimaryActionState() {
    const button = document.getElementById('chat-send-btn');
    if (!button) return;
    const running = isCurrentChatTaskActive();
    const label = running
        ? chatTranslate('tasks.stopTask', '停止任务')
        : chatTranslate('chat.send', '发送');
    button.classList.toggle('is-task-running', running);
    button.setAttribute('aria-label', label);
    button.setAttribute('title', label);
    const labelElement = button.querySelector('.send-btn-label');
    if (labelElement) labelElement.textContent = label;
}

function handleChatPrimaryAction(event) {
    if (event) event.preventDefault();
    if (!isCurrentChatTaskActive()) {
        sendMessage();
        return;
    }

    const live = window.__csAgentLiveStream;
    const conversationId = getCurrentChatTaskConversationId();
    if (conversationId && typeof cancelActiveTask === 'function') {
        cancelActiveTask(conversationId);
        return;
    }
    if (live && live.progressId && typeof cancelProgressTask === 'function') {
        cancelProgressTask(live.progressId);
    }
}

function initChatPrimaryActionButton() {
    const button = document.getElementById('chat-send-btn');
    if (!button) return;
    if (!button.querySelector('.send-btn-stop-icon')) {
        const stopIcon = document.createElement('span');
        stopIcon.className = 'send-btn-stop-icon';
        stopIcon.setAttribute('aria-hidden', 'true');
        button.appendChild(stopIcon);
    }
    button.onclick = handleChatPrimaryAction;
    updateChatPrimaryActionState();
}

function closeChatReasoningPanel() {
    const wrap = document.getElementById('chat-reasoning-wrapper');
    const toggle = document.getElementById('conversation-reasoning-toggle');
    if (wrap) wrap.classList.add('conversation-reasoning-collapsed');
    syncChatSessionSettingsLayerState();
    if (toggle) toggle.setAttribute('aria-expanded', 'false');
}

function toggleConversationReasoningCard() {
    const wrap = document.getElementById('chat-reasoning-wrapper');
    const toggle = document.getElementById('conversation-reasoning-toggle');
    if (!wrap || !toggle) return;
    syncChatReasoningBarHeight();
    wrap.classList.toggle('conversation-reasoning-collapsed');
    syncChatSessionSettingsLayerState();
    const collapsed = wrap.classList.contains('conversation-reasoning-collapsed');
    toggle.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
    if (!collapsed) {
        if (typeof closeAgentModePanel === 'function') {
            closeAgentModePanel();
        }
        if (typeof closeRoleSelectionPanel === 'function') {
            closeRoleSelectionPanel();
        }
        updateChatReasoningSummary();
    }
}

function toggleChatReasoningPanel() {
    toggleConversationReasoningCard();
}

function restoreChatReasoningControlsFromStorage() {
    try {
        const m = document.getElementById('chat-reasoning-mode');
        const e = document.getElementById('chat-reasoning-effort');
        if (m) {
            const v = localStorage.getItem(REASONING_MODE_LS);
            if (v && ['default', 'off', 'on', 'auto'].indexOf(v) !== -1) {
                m.value = v;
            }
        }
        if (e) {
            const v = localStorage.getItem(REASONING_EFFORT_LS);
            if (v !== null && ['', 'low', 'medium', 'high', 'max', 'xhigh'].indexOf(v) !== -1) {
                e.value = v;
            }
        }
        refreshSessionSettingsSelects();
        updateChatReasoningSummary();
    } catch (err) { /* ignore */ }
}

function persistChatReasoningPrefs() {
    try {
        const m = document.getElementById('chat-reasoning-mode');
        const elEff = document.getElementById('chat-reasoning-effort');
        if (m) localStorage.setItem(REASONING_MODE_LS, m.value || 'default');
        if (elEff) localStorage.setItem(REASONING_EFFORT_LS, elEff.value || '');
        refreshSessionSettingsSelects();
        updateChatReasoningSummary();
    } catch (err) { /* ignore */ }
}

/** 供 WebShell 等复用：在 Eino 路径下返回 reasoning 请求片段或 undefined */
function buildReasoningRequestPayload() {
    const wrap = document.getElementById('chat-reasoning-wrapper');
    if (!wrap || wrap.style.display === 'none') {
        return undefined;
    }
    const modeEl = document.getElementById('chat-reasoning-mode');
    const effEl = document.getElementById('chat-reasoning-effort');
    if (!modeEl) return undefined;
    const mode = (modeEl.value || 'default').trim();
    const effort = effEl && effEl.value ? String(effEl.value).trim() : '';
    if (mode === 'default' && !effort) {
        return undefined;
    }
    const o = {};
    if (mode !== 'default') o.mode = mode;
    if (effort) o.effort = effort;
    return Object.keys(o).length ? o : undefined;
}

if (typeof window !== 'undefined') {
    window.persistChatAIChannelPref = persistChatAIChannelPref;
    window.populateChatAIChannelSelect = populateChatAIChannelSelect;
    window.persistChatReasoningPrefs = persistChatReasoningPrefs;
    window.buildReasoningRequestPayload = buildReasoningRequestPayload;
    window.closeChatReasoningPanel = closeChatReasoningPanel;
    window.toggleChatReasoningPanel = toggleChatReasoningPanel;
    window.toggleConversationReasoningCard = toggleConversationReasoningCard;
    window.updateChatReasoningSummary = updateChatReasoningSummary;
    window.updateChatComposerSessionShortcuts = updateChatComposerSessionShortcuts;
    window.openChatSessionSettings = openChatSessionSettings;
    window.openChatSystemModelPicker = openChatSystemModelPicker;
    window.openChatSystemModelView = openChatSystemModelView;
    window.closeChatSystemModelPicker = closeChatSystemModelPicker;
    window.refreshSessionSettingsSelects = refreshSessionSettingsSelects;
    window.updateChatPrimaryActionState = updateChatPrimaryActionState;
}

function closeAgentModePanel() {
    const panel = document.getElementById('agent-mode-panel');
    const btn = document.getElementById('agent-mode-btn');
    if (panel) panel.style.display = 'none';
    if (btn) {
        btn.classList.remove('active');
        btn.setAttribute('aria-expanded', 'false');
    }
}

function toggleAgentModePanel() {
    const panel = document.getElementById('agent-mode-panel');
    const btn = document.getElementById('agent-mode-btn');
    if (!panel || !btn) return;
    const isOpen = panel.style.display === 'flex';
    if (isOpen) {
        closeAgentModePanel();
        return;
    }
    if (typeof closeChatReasoningPanel === 'function') {
        closeChatReasoningPanel();
    }
    if (typeof closeRoleSelectionPanel === 'function') {
        closeRoleSelectionPanel();
    }
    if (typeof closeChatProjectPanel === 'function') {
        closeChatProjectPanel();
    }
    panel.style.display = 'flex';
    btn.classList.add('active');
    btn.setAttribute('aria-expanded', 'true');
}

function selectAgentMode(mode) {
    const ok = chatAgentModeIsEinoSingle(mode) || chatAgentModeIsEino(mode);
    if (!ok) return;
    saveConversationAgentModePreference(currentConversationId, mode);
    try {
        localStorage.setItem(AGENT_MODE_STORAGE_KEY, mode);
    } catch (e) { /* ignore */ }
    syncAgentModeFromValue(mode);
    closeAgentModePanel();
}

async function initChatAgentModeFromConfig() {
    const wrap = document.getElementById('agent-mode-wrapper');
    const sel = document.getElementById('agent-mode-select');
    if (!wrap || !sel) return;

    // 先展示基础模式，避免首次登录时配置接口短暂失败导致入口被隐藏。
    wrap.style.display = '';
    let stored = localStorage.getItem(AGENT_MODE_STORAGE_KEY);
    if (!(chatAgentModeIsEinoSingle(stored) || chatAgentModeIsEino(stored))) {
        stored = CHAT_AGENT_MODE_EINO_SINGLE;
    }
    sel.value = stored;
    syncAgentModeFromValue(stored);
    document.querySelectorAll('.agent-mode-option').forEach(function (el) {
        const v = el.getAttribute('data-value');
        if (v === 'deep' || v === 'plan_execute' || v === 'supervisor') {
            el.style.display = 'none';
        } else {
            el.style.display = '';
        }
    });
    restoreChatReasoningControlsFromStorage();
    syncReasoningRowVisibility(stored);

    try {
        const r = await apiFetch('/api/config');
        if (!r.ok) return;
        const cfg = await r.json();
        multiAgentAPIEnabled = !!(cfg.multi_agent && cfg.multi_agent.enabled);
        populateChatAIChannelSelect(cfg.ai || {});
        const hitlAuditModel = cfg.hitl && cfg.hitl.audit_model;
        chatHitlAuditModelName = hitlAuditModel && typeof hitlAuditModel.model === 'string'
            ? hitlAuditModel.model.trim()
            : '';
        updateChatReasoningSummary();
        if (typeof window !== 'undefined') {
            window.__csaiMultiAgentPublic = cfg.multi_agent || null;
            const tw = cfg.hitl && cfg.hitl.tool_whitelist;
            if (Array.isArray(tw)) {
                window.csaiHitlGlobalToolWhitelist = tw.slice();
            }
        }
        if (typeof window.refreshHitlPageWhitelist === 'function') {
            window.refreshHitlPageWhitelist();
        }
        document.querySelectorAll('.agent-mode-option').forEach(function (el) {
            const v = el.getAttribute('data-value');
            if (v === 'deep' || v === 'plan_execute' || v === 'supervisor') {
                el.style.display = multiAgentAPIEnabled ? '' : 'none';
            } else {
                el.style.display = '';
            }
        });
        stored = chatAgentModeNormalizeStored(stored, cfg);
        try {
            localStorage.setItem(AGENT_MODE_STORAGE_KEY, stored);
        } catch (e) { /* ignore */ }
        sel.value = stored;
        syncAgentModeFromValue(stored);
        restoreChatReasoningControlsFromStorage();
        syncReasoningRowVisibility(stored);
    } catch (e) {
        logger.warn('initChatAgentModeFromConfig', e);
    }
}