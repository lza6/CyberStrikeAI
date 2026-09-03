
// 消息计数器，确保ID唯一
let messageCounter = 0;

// 为消息气泡中的表格添加独立的滚动容器
function wrapTablesInBubble(bubble) {
    const tables = bubble.querySelectorAll('table');
    tables.forEach(table => {
        // 检查表格是否已经有包装容器
        if (table.parentElement && table.parentElement.classList.contains('table-wrapper')) {
            return;
        }

        // 创建表格包装容器
        const wrapper = document.createElement('div');
        wrapper.className = 'table-wrapper';

        // 将表格移动到包装容器中
        table.parentNode.insertBefore(wrapper, table);
        wrapper.appendChild(table);
    });
}

const PROJECT_NAME_DISPLAY_MAX_CHARACTERS = 12;

/** 仅限制项目名的界面展示，不修改实际保存的名称。 */
function formatProjectNameForDisplay(value) {
    const fullName = String(value == null ? '' : value);
    const characters = Array.from(fullName);
    if (characters.length <= PROJECT_NAME_DISPLAY_MAX_CHARACTERS) return fullName;
    return `${characters.slice(0, PROJECT_NAME_DISPLAY_MAX_CHARACTERS).join('')}…`;
}

function applyProjectNameDisplay(element, value, fallback = '') {
    if (!element) return '';
    const fullName = String(value || fallback || '');
    element.textContent = formatProjectNameForDisplay(fullName);
    element.dataset.fullName = fullName;
    element.title = fullName;
    return fullName;
}

window.formatProjectNameForDisplay = formatProjectNameForDisplay;
window.applyProjectNameDisplay = applyProjectNameDisplay;

function getChatWelcomeProjectName() {
    const projectElement = document.getElementById('chat-project-text');
    const projectText = (projectElement?.dataset?.fullName || projectElement?.textContent || '').trim();
    return projectText || (typeof window.t === 'function' ? window.t('projects.noProject') : '无项目');
}

function getChatWelcomeText() {
    const project = getChatWelcomeProjectName();
    const noProject = typeof window.t === 'function' ? window.t('projects.noProject') : '无项目';
    if (!project || project === noProject) {
        return typeof window.t === 'function'
            ? window.t('chat.noProjectWelcomeMessage')
            : '当前无项目，请输入您的测试需求，系统将自动执行相应的安全测试。';
    }
    return typeof window.t === 'function'
        ? window.t('chat.projectWelcomeMessage', { project })
        : `当前${project}项目，请输入您的测试需求，系统将自动执行相应的安全测试。`;
}

function updateChatWelcomeTitle(title) {
    if (!title) return;
    const project = getChatWelcomeProjectName();
    const noProject = typeof window.t === 'function' ? window.t('projects.noProject') : '无项目';
    const subtitle = title.parentElement?.querySelector('.chat-welcome-empty-state-subtitle');

    if (project === noProject) {
        title.textContent = typeof window.t === 'function'
            ? window.t('chat.noProjectWelcomeTitle')
            : '要测试什么？';
    } else {
        const prefix = typeof window.t === 'function'
            ? window.t('chat.projectWelcomeTitlePrefix')
            : '要在 ';
        const suffix = typeof window.t === 'function'
            ? window.t('chat.projectWelcomeTitleSuffix')
            : ' 项目中测试什么？';
        const projectName = document.createElement('span');
        projectName.className = 'chat-welcome-project-name';
        applyProjectNameDisplay(projectName, project);
        title.replaceChildren(document.createTextNode(prefix), projectName, document.createTextNode(suffix));
    }

    if (subtitle) {
        subtitle.textContent = typeof window.t === 'function'
            ? window.t('chat.welcomeSubtitle')
            : '请输入您的测试需求，系统将自动执行相应的安全测试。';
    }
}

function renderChatWelcomeEmptyState() {
    const messagesDiv = document.getElementById('chat-messages');
    if (!messagesDiv) return null;
    messagesDiv.querySelectorAll('.chat-welcome-empty-state').forEach((node) => node.remove());
    const state = document.createElement('div');
    state.className = 'chat-welcome-empty-state';
    state.setAttribute('role', 'status');
    state.setAttribute('aria-live', 'polite');
    state.innerHTML = '<p class="chat-welcome-empty-state-title"></p><p class="chat-welcome-empty-state-subtitle"></p>';
    updateChatWelcomeTitle(state.querySelector('.chat-welcome-empty-state-title'));
    messagesDiv.appendChild(state);
    return state;
}

/** 更新新对话欢迎空状态，并兼容刷新旧版本遗留的系统就绪消息。 */
function refreshSystemReadyMessageBubbles() {
    const text = getChatWelcomeText();
    const welcome = document.querySelector('.chat-welcome-empty-state-title');
    if (welcome) updateChatWelcomeTitle(welcome);
    const escapeHtmlLocal = (s) => {
        if (!s) return '';
        const div = document.createElement('div');
        div.textContent = s;
        return div.innerHTML;
    };
    let formattedContent;
    if (typeof window.csMarkdownSanitize !== 'undefined') {
        formattedContent = window.csMarkdownSanitize.formatMarkdownToHtml(text, { profile: 'chat' });
    } else {
        formattedContent = escapeHtmlLocal(text).replace(/\n/g, '<br>');
    }

    document.querySelectorAll('.message.assistant[data-system-ready-message]').forEach(function (messageDiv) {
        const bubble = messageDiv.querySelector('.message-bubble');
        if (!bubble) return;
        const copyBtn = bubble.querySelector('.message-copy-btn');
        if (copyBtn) copyBtn.remove();
        bubble.innerHTML = formattedContent;
        if (typeof wrapTablesInBubble === 'function') wrapTablesInBubble(bubble);
        messageDiv.dataset.originalContent = text;
        appendMessageCopyButton(messageDiv);
    });
}

function ensureMessageMetaFooter(content) {
    if (!content) return null;
    let footer = content.querySelector('.message-meta-footer');
    if (footer) return footer;
    const timeDiv = content.querySelector('.message-time');
    footer = document.createElement('div');
    footer.className = 'message-meta-footer';
    if (timeDiv && timeDiv.parentNode === content) {
        timeDiv.parentNode.insertBefore(footer, timeDiv);
        footer.appendChild(timeDiv);
    } else {
        content.appendChild(footer);
    }
    return footer;
}

function appendMessageCopyButton(messageDiv) {
    if (!messageDiv) return null;
    if (!messageDiv.classList || (!messageDiv.classList.contains('assistant') && !messageDiv.classList.contains('user'))) {
        return null;
    }
    const content = messageDiv.querySelector('.message-content');
    const footer = ensureMessageMetaFooter(content);
    if (!footer) return null;

    messageDiv.querySelectorAll('.message-bubble .message-copy-btn').forEach((btn) => btn.remove());
    let copyBtn = footer.querySelector('.message-copy-btn');
    if (copyBtn) return copyBtn;

    copyBtn = document.createElement('button');
    copyBtn.type = 'button';
    copyBtn.className = 'message-copy-btn';
    copyBtn.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><rect x="9" y="9" width="13" height="13" rx="2" ry="2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg><span>' + (typeof window.t === 'function' ? window.t('common.copy') : '复制') + '</span>';
    copyBtn.title = typeof window.t === 'function' ? window.t('chat.copyMessageTitle') : '复制消息内容';
    copyBtn.setAttribute('aria-label', copyBtn.title);
    copyBtn.onclick = function(e) {
        e.stopPropagation();
        copyMessageToClipboard(messageDiv, this);
    };
    const deleteBtn = footer.querySelector('.message-delete-turn-btn');
    if (deleteBtn) {
        footer.insertBefore(copyBtn, deleteBtn);
    } else {
        footer.appendChild(copyBtn);
    }
    return copyBtn;
}
window.appendMessageCopyButton = appendMessageCopyButton;
window.ensureMessageMetaFooter = ensureMessageMetaFooter;

// 添加消息（options.systemReadyMessage 为 true 时，语言切换会刷新该条文案）
function addMessage(role, content, mcpExecutionIds = null, progressId = null, createdAt = null, options = null) {
    const messagesDiv = document.getElementById('chat-messages');
    const messageDiv = document.createElement('div');
    messageCounter++;
    const id = 'msg-' + Date.now() + '-' + messageCounter + '-' + Math.random().toString(36).substr(2, 9);
    messageDiv.id = id;
    messageDiv.className = 'message ' + role;

    messagesDiv.querySelector('.chat-welcome-empty-state')?.remove();

    // 创建消息内容容器
    const contentWrapper = document.createElement('div');
    contentWrapper.className = 'message-content';

    // 创建消息气泡
    const bubble = document.createElement('div');
    bubble.className = 'message-bubble';

    // 解析 Markdown 或 HTML 格式
    let formattedContent;
    const escapeHtml = (text) => {
        if (!text) return '';
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    };

    // 助手消息中的已知中文错误前缀做国际化替换（后端固定返回中文）
    let displayContent = content;
    if (role === 'assistant' && typeof displayContent === 'string' && typeof window.t === 'function') {
        if (displayContent.indexOf('执行失败: ') === 0) {
            displayContent = window.t('chat.executeFailed') + ': ' + displayContent.slice('执行失败: '.length);
        }
        if (displayContent.indexOf('调用OpenAI失败:') !== -1) {
            displayContent = displayContent.replace(/调用OpenAI失败:/g, window.t('chat.callOpenAIFailed') + ':');
        }
    }

    // 对于用户消息，直接转义HTML，不进行Markdown解析，以保留所有特殊字符
    if (role === 'user') {
        formattedContent = escapeHtml(content).replace(/\n/g, '<br>');
    } else if (typeof window.csMarkdownSanitize !== 'undefined') {
        formattedContent = window.csMarkdownSanitize.formatMarkdownToHtml(
            role === 'assistant' ? displayContent : content,
            { profile: 'chat' }
        );
    } else {
        const rawForEscape = role === 'assistant' ? displayContent : content;
        formattedContent = escapeHtml(rawForEscape).replace(/\n/g, '<br>');
    }

    bubble.innerHTML = formattedContent;

    // 刷新恢复运行中会话时，后端正文可能仍是持久化占位值“处理中...”。
    // 保留消息节点供迭代详情和最终回复复用，但不要把占位值显示成助手正文。
    if (role === 'assistant' && options && options.hideAssistantPlaceholder) {
        messageDiv.classList.add('assistant-placeholder-content');
        bubble.hidden = true;
    }

    if (typeof window.csMarkdownSanitize !== 'undefined') {
        window.csMarkdownSanitize.stripSuspiciousImages(bubble);
    }

    // 为每个表格添加独立的滚动容器
    wrapTablesInBubble(bubble);

    contentWrapper.appendChild(bubble);

    // 保存原始内容到消息元素，用于复制功能
    if (role === 'assistant' || role === 'user') {
        messageDiv.dataset.originalContent = content;
    }

    // 添加时间戳
    const timeDiv = document.createElement('div');
    timeDiv.className = 'message-time';
    // 如果有传入的创建时间，使用它；否则使用当前时间
    let messageTime;
    if (createdAt) {
        // 处理字符串或Date对象
        if (typeof createdAt === 'string') {
            messageTime = new Date(createdAt);
        } else if (createdAt instanceof Date) {
            messageTime = createdAt;
        } else {
            messageTime = new Date(createdAt);
        }
        // 如果解析失败，使用当前时间
        if (isNaN(messageTime.getTime())) {
            messageTime = new Date();
        }
    } else {
        messageTime = new Date();
    }
    const msgTimeLocale = (typeof window.__locale === 'string' && window.__locale.startsWith('zh')) ? 'zh-CN' : 'en-US';
    const msgTimeOpts = { hour: '2-digit', minute: '2-digit' };
    if (msgTimeLocale === 'zh-CN') msgTimeOpts.hour12 = false;
    timeDiv.textContent = messageTime.toLocaleTimeString(msgTimeLocale, msgTimeOpts);
    try {
        timeDiv.dataset.messageTime = messageTime.toISOString();
    } catch (e) { /* ignore */ }
    const metaFooter = document.createElement('div');
    metaFooter.className = 'message-meta-footer';
    metaFooter.appendChild(timeDiv);
    contentWrapper.appendChild(metaFooter);
    messageDiv.appendChild(contentWrapper);

    // 为用户和助手消息添加复制按钮（复制整条消息内容）
    if (role === 'assistant' || role === 'user') {
        appendMessageCopyButton(messageDiv);
    }

    // 有 MCP 执行记录且非流式占位消息时展示调用按钮；带 progressId 的流式占位不挂此条（与进度卡片一致，结束时 integrate 再创建）
    if (role === 'assistant' && (mcpExecutionIds && Array.isArray(mcpExecutionIds) && mcpExecutionIds.length > 0) && !progressId) {
        if (options && options.deferMcpButtons) {
            try {
                const ids = cacheMcpExecutionIds(messageDiv, mcpExecutionIds);
                messageDiv.dataset.pendingMcpExecutionIds = JSON.stringify(ids);
            } catch (e) { /* ignore */ }
        } else {
            setMcpCallExecutionIds(messageDiv, mcpExecutionIds);
        }
    }

    // 标记「系统就绪」占位消息，便于切换语言后刷新文案
    if (options && options.systemReadyMessage) {
        messageDiv.setAttribute('data-system-ready-message', '1');
    }
    messagesDiv.appendChild(messageDiv);
    if (window.CyberStrikeChatScroll) {
        window.CyberStrikeChatScroll.applyMessageScroll(options);
    } else {
        messagesDiv.scrollTop = messagesDiv.scrollHeight;
    }
    return id;
}

// 复制消息内容到剪贴板（使用原始Markdown格式）
function copyMessageToClipboard(messageDiv, button) {
    try {
        // 获取保存的原始Markdown内容
        const originalContent = messageDiv.dataset.originalContent;

        // 统一的复制处理函数
        const doCopy = (text) => {
            // 优先使用现代 Clipboard API（需要 HTTPS 或 localhost）
            if (navigator.clipboard && navigator.clipboard.writeText) {
                return navigator.clipboard.writeText(text).then(() => {
                    showCopySuccess(button);
                }).catch(err => {
                    logger.error('Clipboard API 复制失败:', err);
                    fallbackCopy(text);
                });
            } else {
                // 降级方案：使用传统的 execCommand 方法（适用于 HTTP 环境）
                return fallbackCopy(text);
            }
        };

        // 降级复制函数（使用 document.execCommand）
        const fallbackCopy = (text) => {
            try {
                const textArea = document.createElement('textarea');
                textArea.value = text;
                textArea.style.position = 'fixed';
                textArea.style.left = '-999999px';
                textArea.style.top = '-999999px';
                textArea.style.opacity = '0';
                document.body.appendChild(textArea);
                textArea.focus();
                textArea.select();

                const successful = document.execCommand('copy');
                document.body.removeChild(textArea);

                if (successful) {
                    showCopySuccess(button);
                } else {
                    throw new Error('execCommand copy failed');
                }
            } catch (execErr) {
                logger.error('降级复制失败:', execErr);
                alert(typeof window.t === 'function' ? window.t('chat.copyFailedManual') : '复制失败，请手动选择内容复制');
            }
        };

        if (!originalContent) {
            // 如果没有保存原始内容，尝试从渲染后的HTML提取（降级方案）
            const bubble = messageDiv.querySelector('.message-bubble');
            if (bubble) {
                const tempDiv = document.createElement('div');
                tempDiv.innerHTML = bubble.innerHTML;

                // 移除复制按钮本身（避免复制按钮文本）
                const copyBtnInTemp = tempDiv.querySelector('.message-copy-btn');
                if (copyBtnInTemp) {
                    copyBtnInTemp.remove();
                }

                // 提取纯文本内容
                let textContent = tempDiv.textContent || tempDiv.innerText || '';
                textContent = textContent.replace(/\n{3,}/g, '\n\n').trim();

                doCopy(textContent);
            }
            return;
        }

        // 使用原始Markdown内容
        doCopy(originalContent);
    } catch (error) {
        logger.error('复制消息时出错:', error);
        alert(typeof window.t === 'function' ? window.t('chat.copyFailedManual') : '复制失败，请手动选择内容复制');
    }
}

// 显示复制成功提示
function showCopySuccess(button) {
    if (button) {
        const originalText = button.innerHTML;
        button.dataset.copySuccessActive = '1';
        button.innerHTML = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg"><path d="M20 6L9 17l-5-5" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/></svg><span>' + (typeof window.t === 'function' ? window.t('common.copied') : '已复制') + '</span>';
        button.style.color = '#10b981';
        button.style.background = 'rgba(16, 185, 129, 0.1)';
        button.style.borderColor = 'rgba(16, 185, 129, 0.3)';
        setTimeout(() => {
            delete button.dataset.copySuccessActive;
            button.innerHTML = originalText;
            button.style.color = '';
            button.style.background = '';
            button.style.borderColor = '';
        }, 2000);
    }
}

/** Claude extended thinking 内部尾缀（与后端 DisplayReasoningContent 一致，UI 不展示） */
const CLAUDE_REASONING_UI_SUFFIX = '\n---CSAI_CLAUDE_THINKING_BLOCKS---\n';

function normalizeReasoningContentForDisplay(text) {
    if (text == null) return '';
    let s = String(text).trim();
    if (!s) return '';
    const idx = s.lastIndexOf(CLAUDE_REASONING_UI_SUFFIX);
    if (idx >= 0) {
        s = s.slice(0, idx).trim();
    }
    return s;
}

function setMessageReasoningContent(messageIdOrEl, reasoningContent) {
    const el = typeof messageIdOrEl === 'string' ? document.getElementById(messageIdOrEl) : messageIdOrEl;
    if (!el || !el.dataset) return;
    const rc = normalizeReasoningContentForDisplay(reasoningContent);
    if (rc) {
        el.dataset.reasoningContent = rc;
    } else {
        delete el.dataset.reasoningContent;
    }
}

function getMessageReasoningContent(messageIdOrEl) {
    const el = typeof messageIdOrEl === 'string' ? document.getElementById(messageIdOrEl) : messageIdOrEl;
    if (!el || !el.dataset) return '';
    return normalizeReasoningContentForDisplay(el.dataset.reasoningContent || '');
}

function reasoningTextAlreadyInProcessDetails(processDetails, rc) {
    if (!rc) return true;
    const list = Array.isArray(processDetails) ? processDetails : [];
    for (let i = 0; i < list.length; i++) {
        const d = list[i];
        if (!d) continue;
        const et = d.eventType || '';
        if (et !== 'reasoning_chain' && et !== 'thinking') continue;
        const msg = normalizeReasoningContentForDisplay(d.message || '');
        if (!msg) continue;
        if (msg === rc || msg.includes(rc) || rc.includes(msg)) {
            return true;
        }
    }
    return false;
}

/** 合并 messages.reasoningContent 与 process_details 中的 reasoning_chain，两者都读、都展示（去重后） */
function mergeMessageReasoningContentIntoProcessDetails(processDetails, reasoningContent) {
    const rc = normalizeReasoningContentForDisplay(reasoningContent);
    const details = Array.isArray(processDetails) ? processDetails.slice() : [];
    if (!rc || reasoningTextAlreadyInProcessDetails(details, rc)) {
        return details;
    }
    details.push({
        eventType: 'reasoning_chain',
        message: rc,
        data: { source: 'message.reasoningContent' }
    });
    return details;
}

async function syncAssistantReasoningContentFromServer(backendMessageId, domAssistantId) {
    if (!backendMessageId || !domAssistantId || !currentConversationId || typeof apiFetch !== 'function') {
        return;
    }
    try {
        const convRes = await apiFetch(`/api/conversations/${encodeURIComponent(currentConversationId)}?include_process_details=0`);
        const conv = await convRes.json().catch(() => ({}));
        if (!convRes.ok || !Array.isArray(conv.messages)) return;
        const msg = conv.messages.find((m) => m && String(m.id) === String(backendMessageId));
        if (!msg || !msg.reasoningContent) return;
        setMessageReasoningContent(domAssistantId, msg.reasoningContent);
        // 最终回复到达后同样必须完整恢复过程详情；无参数接口默认仅返回前 50 条，
        // 否则这里会把 task-events 恢复出的完整时间线再次覆盖成第一页。
        if (typeof window.loadProcessDetailsPaginated === 'function') {
            await window.loadProcessDetailsPaginated(domAssistantId, String(backendMessageId));
        } else {
            const pdRes = await apiFetch(
                `/api/messages/${encodeURIComponent(String(backendMessageId))}/process-details?full=1`
            );
            const pdJson = await pdRes.json().catch(() => ({}));
            const details = pdRes.ok && Array.isArray(pdJson.processDetails) ? pdJson.processDetails : [];
            if (typeof renderProcessDetails === 'function') {
                renderProcessDetails(domAssistantId, details);
            }
        }
    } catch (e) {
        logger.warn('syncAssistantReasoningContentFromServer failed', e);
    }
}

window.normalizeReasoningContentForDisplay = normalizeReasoningContentForDisplay;
window.setMessageReasoningContent = setMessageReasoningContent;
window.getMessageReasoningContent = getMessageReasoningContent;
window.filterNoiseProcessDetails = filterNoiseProcessDetails;
window.mergeMessageReasoningContentIntoProcessDetails = mergeMessageReasoningContentIntoProcessDetails;
window.syncAssistantReasoningContentFromServer = syncAssistantReasoningContentFromServer;

/** 相邻且类型/正文/data 完全一致的过程详情只保留一条（与后端去重一致，避免时间线叠多条相同块） */
function isEinoAgentHeartbeatProgress(detail) {
    if (!detail || detail.eventType !== 'progress') return false;
    const msg = String(detail.message != null ? detail.message : '').trim();
    return /^\[Eino\]\s+\S/.test(msg);
}

function hasModelOutputRecoveryMarker(value) {
    if (!value) return false;
    let obj = value;
    if (typeof obj === 'string') {
        const text = obj.trim();
        if (!text || text.indexOf('_cyberstrike_model_output_recovery') === -1) return false;
        try {
            obj = JSON.parse(text);
        } catch (e) {
            return false;
        }
    }
    return !!(obj && typeof obj === 'object' && obj._cyberstrike_model_output_recovery);
}

function isModelOutputRecoveryToolCallDetail(detail) {
    if (!detail || detail.eventType !== 'tool_call') return false;
    const data = detail.data && typeof detail.data === 'object' ? detail.data : {};
    return hasModelOutputRecoveryMarker(data.argumentsObj) || hasModelOutputRecoveryMarker(data.arguments);
}

function isAnonymousTaskFragmentToolCallDetail(detail) {
    if (!detail || detail.eventType !== 'tool_call') return false;
    const data = detail.data && typeof detail.data === 'object' ? detail.data : {};
    const toolName = String(data.toolName || '').trim().toLowerCase();
    if (toolName && toolName !== 'task' && toolName !== 'unknown') return false;

    let raw = null;
    const argsObj = data.argumentsObj && typeof data.argumentsObj === 'object' ? data.argumentsObj : null;
    if (argsObj) {
        const keys = Object.keys(argsObj);
        if (keys.length === 1 && keys[0] === '_raw') {
            raw = String(argsObj._raw != null ? argsObj._raw : '').trim();
        }
    }
    if (raw == null && typeof data.arguments === 'string') {
        raw = data.arguments.trim();
    }
    if (raw == null) return false;
    if (!raw) return true;
    return !(raw.startsWith('{') && raw.endsWith('}'));
}

function isInternalEinoDiagnosticDetail(detail) {
    if (!detail) return false;
    if (detail.eventType === 'model_output_rejected') return true;
    if (detail.eventType !== 'progress') return false;
    const msg = String(detail.message != null ? detail.message : '').trim();
    if (msg === 'Eino TurnLoop 常驻多轮 runtime 已接管本轮会话。' ||
        msg === 'Eino TurnLoop 已在安全点切换到用户补充后的下一轮。' ||
        msg === '已将用户补充推入 Eino TurnLoop，正在等待安全点切换…') {
        return true;
    }
    const data = detail.data && typeof detail.data === 'object' ? detail.data : {};
    const kind = String(data.kind || '').trim();
    return kind === 'turn_loop_takeover' || kind === 'turn_loop_preempted';
}

function filterNoiseProcessDetails(details) {
    if (!Array.isArray(details)) return details;
    return details.filter(function (d) {
        if (isEinoAgentHeartbeatProgress(d)) return false;
        if (isModelOutputRecoveryToolCallDetail(d)) return false;
        if (isAnonymousTaskFragmentToolCallDetail(d)) return false;
        if (isInternalEinoDiagnosticDetail(d)) return false;
        return !(d && d.eventType === 'tool_calls_detected');
    });
}

function dedupeConsecutiveProcessDetailRows(details) {
    if (!Array.isArray(details) || details.length < 2) {
        return details;
    }
    const out = [details[0]];
    for (let i = 1; i < details.length; i++) {
        const cur = details[i];
        if (processDetailRowFingerprint(out[out.length - 1]) === processDetailRowFingerprint(cur)) {
            continue;
        }
        out.push(cur);
    }
    return out;
}

function processDetailRowFingerprint(d) {
    if (!d || typeof d !== 'object') {
        return '';
    }
    const et = String(d.eventType || '');
    const msg = String(d.message != null ? d.message : '').trim();
    let dataKey = '';
    try {
        if (d.data != null) {
            dataKey = JSON.stringify(d.data);
        }
    } catch (e) {
        dataKey = String(d.data);
    }
    return et + '\0' + msg + '\0' + dataKey;
}

function compactWorkflowProcessDetails(details) {
    if (!Array.isArray(details) || details.length === 0) return details || [];
    return details.filter((detail) => {
        const eventType = detail && detail.eventType ? String(detail.eventType) : '';
        // workflow_node_start 已经表达了节点进入；这些事件只用于实时状态，落到详情里会让 Agent 节点看起来重复启动。
        return eventType !== 'workflow_agent_start';
    });
}

function isProcessDetailsUserExpanded(messageId) {
    const container = document.getElementById('process-details-' + messageId);
    return !!(container && container.dataset && container.dataset.userExpanded === '1');
}

function messageHasConversationContent(messageElement) {
    if (!messageElement || !messageElement.classList || !messageElement.classList.contains('assistant')) return false;
    if (messageElement.hasAttribute('data-system-ready-message')) return false;
    if (messageElement.dataset && String(messageElement.dataset.backendMessageId || '').trim()) return true;
    const raw = messageElement.dataset ? String(messageElement.dataset.originalContent || '').trim() : '';
    if (raw) return true;
    const bubble = messageElement.querySelector('.message-bubble');
    if (!bubble) return false;
    const clone = bubble.cloneNode(true);
    clone.querySelectorAll('.message-copy-btn').forEach((btn) => btn.remove());
    return String(clone.textContent || '').trim().length > 0;
}

function syncProcessDetailButtonLabels(messageId, expanded) {
    const expandT = typeof window.t === 'function' ? window.t('chat.expandDetail') : '展开详情';
    const collapseT = typeof window.t === 'function' ? window.t('tasks.collapseDetail') : '收起详情';
    const label = expanded ? collapseT : expandT;
    document.querySelectorAll('#' + messageId + ' .process-detail-btn').forEach((btn) => {
        btn.innerHTML = '<span>' + label + '</span>';
    });
    if (typeof window.syncAssistantTurnSummary === 'function') {
        window.syncAssistantTurnSummary(document.getElementById(messageId));
    }
}

/** 懒加载占位提示可点击，与工具栏「展开详情」行为一致 */
function bindProcessDetailsLazyHint(hostEl, messageId) {
    if (!hostEl || !messageId) return;
    const emptyEl = hostEl.classList && hostEl.classList.contains('progress-timeline-empty')
        ? hostEl
        : hostEl.querySelector('.progress-timeline-empty');
    if (!emptyEl || emptyEl.dataset.lazyHintBound === '1') return;
    emptyEl.dataset.lazyHintBound = '1';
    emptyEl.classList.add('progress-timeline-lazy-clickable');
    emptyEl.setAttribute('role', 'button');
    emptyEl.setAttribute('tabindex', '0');
    const activate = () => {
        if (typeof toggleProcessDetails === 'function') {
            toggleProcessDetails(null, messageId);
        }
    };
    emptyEl.addEventListener('click', activate);
    emptyEl.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            activate();
        }
    });
}
window.bindProcessDetailsLazyHint = bindProcessDetailsLazyHint;

// 渲染过程详情
// options.append=true 时分页追加；options.markLoaded=false 时保留 lazy 标记（分页加载中）
function renderProcessDetails(messageId, processDetails, options) {
    const renderOpts = options || {};
    const appendMode = !!renderOpts.append;
    const prependMode = !!renderOpts.prepend;
    const markLoaded = renderOpts.markLoaded !== false;
    const toolStatusByProcessDetailId = new Map();
    if (Array.isArray(renderOpts.toolExecutions)) {
        renderOpts.toolExecutions.forEach((execution) => {
            if (!execution || !execution.processDetailId) return;
            toolStatusByProcessDetailId.set(String(execution.processDetailId), String(execution.status || '').toLowerCase());
        });
    }
    const messageElement = document.getElementById(messageId);
    if (!messageElement) {
        return;
    }
    const isLazyRequest = (processDetails === null);
    const reasoningFromMessage = getMessageReasoningContent(messageElement);
    const backendId = messageElement.dataset ? String(messageElement.dataset.backendMessageId || '').trim() : '';
    const hasConversationContent = !!renderOpts.force || messageHasConversationContent(messageElement);
    if (isLazyRequest && !reasoningFromMessage && !backendId && getMcpExecutionCount(messageElement) <= 0 && !hasConversationContent) {
        pruneEmptyMcpCallSection(messageElement);
        return;
    }

    // 查找或创建 MCP 区域（工具栏 + 工具列表 + 迭代时间线 分区）
    const chrome = ensureMcpCallSectionChrome(messageElement, messageId);
    if (!chrome) return;
    const { mcpSection, toolbar: buttonsContainer } = chrome;

    // 添加过程详情按钮（如果还没有）
    let processDetailBtn = buttonsContainer.querySelector('.process-detail-btn');
    if (!processDetailBtn) {
        processDetailBtn = document.createElement('button');
        processDetailBtn.className = 'mcp-detail-btn process-detail-btn';
        processDetailBtn.innerHTML = '<span>' + (typeof window.t === 'function' ? window.t('chat.expandDetail') : '展开详情') + '</span>';
        processDetailBtn.onclick = () => toggleProcessDetails(null, messageId);
        buttonsContainer.appendChild(processDetailBtn);
    }
    syncMcpToolsToggleButton(messageElement);

    // 创建过程详情容器（放在工具列表之后）
    const detailsId = 'process-details-' + messageId;
    let detailsContainer = document.getElementById(detailsId);
    const toolListEl = chrome.toolList;

    if (!detailsContainer) {
        detailsContainer = document.createElement('div');
        detailsContainer.id = detailsId;
        detailsContainer.className = 'process-details-container';
        if (toolListEl) {
            toolListEl.after(detailsContainer);
        } else if (buttonsContainer.nextSibling) {
            mcpSection.insertBefore(detailsContainer, buttonsContainer.nextSibling);
        } else {
            mcpSection.appendChild(detailsContainer);
        }
    }

    // 创建时间线（即使没有processDetails也要创建，以便展开详情按钮能正常工作）
    const timelineId = detailsId + '-timeline';
    let timeline = document.getElementById(timelineId);

    if (!timeline) {
        const contentDiv = document.createElement('div');
        contentDiv.className = 'process-details-content';

        timeline = document.createElement('div');
        timeline.id = timelineId;
        timeline.className = 'progress-timeline';

        contentDiv.appendChild(timeline);
        detailsContainer.appendChild(contentDiv);
    }
    if (typeof window.ensureProcessDetailsReturnLatestControl === 'function') {
        window.ensureProcessDetailsReturnLatestControl(timeline);
    }

    // processDetails === null 表示“尚未加载（懒加载）”；messages.reasoningContent 可先展示
    const isLazyNotLoaded = isLazyRequest;
    if (isLazyNotLoaded && !reasoningFromMessage) {
        detailsContainer.dataset.lazyNotLoaded = '1';
        detailsContainer.dataset.loaded = '0';
        const expandLabel = typeof window.t === 'function' ? window.t('chat.expandDetail') : '展开详情';
        let lazyHint = expandLabel + '（点击后加载迭代详情）';
        timeline.innerHTML = '<div class="progress-timeline-empty">' + lazyHint + '</div>';
        bindProcessDetailsLazyHint(timeline, messageId);
        timeline.classList.remove('expanded');
        if (typeof window.updateProcessDetailsReturnLatestControl === 'function') {
            window.updateProcessDetailsReturnLatestControl(timeline);
        }
        prefetchProcessDetailsSummaryHint(messageId, messageElement);
        return;
    }
    if (isLazyNotLoaded) {
        detailsContainer.dataset.lazyNotLoaded = '1';
        detailsContainer.dataset.loaded = '0';
        processDetails = [];
        if (!appendMode) {
            prefetchProcessDetailsSummaryHint(messageId, messageElement);
        }
    } else if (markLoaded) {
        detailsContainer.dataset.lazyNotLoaded = '0';
        detailsContainer.dataset.loaded = '1';
    }
    const turnUsageFromDetails = extractAssistantTurnTokenUsage(processDetails);
    if (turnUsageFromDetails) {
        setAssistantTurnTokenUsage(messageElement, turnUsageFromDetails);
    }
    processDetails = mergeMessageReasoningContentIntoProcessDetails(processDetails, reasoningFromMessage);
    processDetails = filterNoiseProcessDetails(processDetails);
    processDetails = dedupeConsecutiveProcessDetailRows(processDetails);
    const renderedMcpIds = collectMcpExecutionIdsFromProcessDetails(processDetails);
    if (renderedMcpIds.length > 0) {
        setPendingMcpExecutionIds(messageElement, renderedMcpIds);
        setMcpExecutionSummaryCount(messageElement, renderedMcpIds.length);
    }
    if (typeof window.coalesceProcessDetailsToolPairs === 'function') {
        processDetails = window.coalesceProcessDetailsToolPairs(processDetails);
    }
    processDetails = compactWorkflowProcessDetails(processDetails);
    // 如果没有processDetails或为空，显示空状态
    if (!processDetails || processDetails.length === 0) {
        if (!appendMode && !prependMode) {
            timeline.innerHTML = '<div class="progress-timeline-empty">' + (typeof window.t === 'function' ? window.t('chat.noProcessDetail') : '暂无过程详情（可能执行过快或未触发详细事件）') + '</div>';
            if (!isProcessDetailsUserExpanded(messageId)) {
                timeline.classList.remove('expanded');
            }
            if (typeof window.updateProcessDetailsReturnLatestControl === 'function') {
                window.updateProcessDetailsReturnLatestControl(timeline);
            }
        }
        return;
    }

    const prependAnchor = prependMode ? timeline.firstChild : null;
    const prependScrollBox = prependMode ? document.getElementById('chat-messages') : null;
    const prependScrollHeight = prependScrollBox ? prependScrollBox.scrollHeight : 0;
    const prependScrollTop = prependScrollBox ? prependScrollBox.scrollTop : 0;
    const prependedIds = [];

    if (!appendMode && !prependMode) {
        timeline.innerHTML = '';
    }


    function processDetailAgentPrefix(d) {
        if (!d || d.einoAgent == null) return '';
        const s = String(d.einoAgent).trim();
        return s ? ('[' + s + '] ') : '';
    }

    function formatProcessDetailEinoRunRetryKind(kind) {
        if (typeof window.formatEinoRunRetryKind === 'function') {
            return window.formatEinoRunRetryKind(kind);
        }
        const key = String(kind || '').trim();
        if (!key) return '';
        const labels = {
            rate_limit: '限流 / 请求过多',
            retryable_http: '可重试 HTTP 错误',
            upstream_server: '上游服务错误',
            http_error: 'HTTP 错误',
            upstream_busy: '上游繁忙',
            network: '网络连接异常',
            stream: '流式读取异常',
            transient: '临时异常'
        };
        if (typeof window.t === 'function') {
            const translated = window.t('chat.einoRunRetryKind_' + key);
            if (translated && translated !== 'chat.einoRunRetryKind_' + key) return translated;
        }
        return labels[key] || key;
    }

    function formatProcessDetailEinoRunRetryTitle(data) {
        if (typeof window.formatEinoRunRetryTitle === 'function') {
            return window.formatEinoRunRetryTitle(data);
        }
        const d = data && typeof data === 'object' ? data : {};
        const base = typeof window.t === 'function'
            ? window.t('chat.einoRunRetryTitle')
            : '🔁 临时错误重试';
        const attempt = Number(d.attempt || 0);
        const maxAttempts = Number(d.maxAttempts || 0);
        if (Number.isFinite(attempt) && attempt > 0 && Number.isFinite(maxAttempts) && maxAttempts > 0) {
            return base + '（' + attempt + '/' + maxAttempts + '）';
        }
        return base;
    }

    function formatProcessDetailEinoRunRetryMessage(message, data) {
        if (typeof window.formatEinoRunRetryMessage === 'function') {
            return window.formatEinoRunRetryMessage(message, data);
        }
        const d = data && typeof data === 'object' ? data : {};
        const base = String(message || '').trim();
        const errRaw = d.errorSummary != null && String(d.errorSummary).trim() !== ''
            ? String(d.errorSummary).trim()
            : (d.error != null ? String(d.error).trim() : '');
        const lines = [];
        if (base) lines.push(base);
        const attempt = Number(d.attempt || 0);
        const maxAttempts = Number(d.maxAttempts || 0);
        const backoffSec = Number(d.backoffSec || 0);
        const kind = formatProcessDetailEinoRunRetryKind(d.errorKind);
        if (Number.isFinite(attempt) && attempt > 0 && Number.isFinite(maxAttempts) && maxAttempts > 0) {
            const retryPlan = typeof window.t === 'function'
                ? window.t('chat.einoRunRetryPlan', { attempt: attempt, maxAttempts: maxAttempts, backoffSec: Number.isFinite(backoffSec) && backoffSec > 0 ? backoffSec : '-' })
                : ('重试进度：第 ' + attempt + '/' + maxAttempts + ' 次，等待 ' + (Number.isFinite(backoffSec) && backoffSec > 0 ? backoffSec : '-') + ' 秒');
            if (!base || base.indexOf(String(attempt) + '/' + String(maxAttempts)) === -1) {
                lines.push(retryPlan);
            }
        }
        if (kind) {
            const kindLabel = typeof window.t === 'function'
                ? window.t('chat.einoRunRetryReasonKind')
                : '原因类型';
            lines.push(kindLabel + '：' + kind);
        }
        if (errRaw && (!base || base.indexOf(errRaw) === -1)) {
            const detailLabel = typeof window.t === 'function'
                ? window.t('chat.einoRunRetryErrorDetail')
                : '错误详情';
            lines.push(detailLabel + '：' + errRaw);
        }
        return lines.join('\n');
    }

    function renderOneProcessDetail(detail) {
        const eventType = detail.eventType || '';
        const title = detail.message || '';
        const data = detail.data || {};
        const agPx = processDetailAgentPrefix(data);

        let itemTitle = title;
        if (eventType === 'workflow_start') {
            const name = data.workflowName || data.workflowId || '';
            itemTitle = '🧭 工作流开始' + (name ? (' · ' + name) : '');
        } else if (eventType === 'workflow_done') {
            const name = data.workflowName || data.workflowId || '';
            itemTitle = '✅ 工作流完成' + (name ? (' · ' + name) : '');
        } else if (eventType === 'workflow_node_start') {
            const label = data.label || title || data.nodeId || '';
            itemTitle = '▶ 节点开始' + (label ? (' · ' + label) : '');
        } else if (eventType === 'workflow_node_result') {
            const label = data.label || data.nodeId || '';
            const status = data.status || '';
            const nodeType = data.nodeType != null ? String(data.nodeType).toLowerCase() : '';
            if (nodeType === 'condition') {
                const matched = data.matched === true || data.matched === 'true' || (data.output && (data.output.matched === true || data.output.matched === 'true'));
                itemTitle = (matched ? '✅' : '🔀') + ' 条件判断' + (label ? (' · ' + label) : '') + ' → ' + (matched ? '是' : '否');
            } else {
                const icon = status === 'failed' ? '❌' : (status === 'skipped' ? '⏭️' : '✅');
                itemTitle = icon + ' 节点完成' + (label ? (' · ' + label) : '') + (status ? ('（' + status + '）') : '');
            }
        } else if (eventType === 'workflow_branch_taken' || eventType === 'workflow_branch_skipped') {
            const branch = data.branchLabel || '';
            const target = data.targetLabel || data.targetId || '';
            const taken = eventType === 'workflow_branch_taken';
            itemTitle = (taken ? '➡️' : '⏭️') + (taken ? ' 执行分支' : ' 跳过分支') + (branch ? (' · ' + branch) : '') + (target ? (' → ' + target) : '');
        } else if (eventType === 'workflow_tool_start') {
            const tool = data.tool || data.toolName || '';
            itemTitle = '🔧 工具节点' + (tool ? (' · ' + tool) : '');
        } else if (eventType === 'workflow_agent_output') {
            const label = data.label || data.nodeId || '';
            itemTitle = '🤖 Agent 输出' + (label ? (' · ' + label) : '');
        } else if (eventType === 'workflow_hitl_checkpoint') {
            itemTitle = '🧑‍⚖️ 人工确认检查点';
        } else if (eventType === 'workflow_hitl_waiting') {
            itemTitle = '🧑‍⚖️ 工作流等待审批';
        } else if (eventType === 'workflow_paused') {
            itemTitle = '⏸️ 工作流已暂停';
        } else if (eventType === 'iteration') {
            const n = data.iteration || 1;
            if (data.orchestration === 'plan_execute' && data.einoScope === 'main') {
                const phase = typeof window.translatePlanExecuteAgentName === 'function'
                    ? window.translatePlanExecuteAgentName(data.einoAgent) : (data.einoAgent || '');
                itemTitle = (typeof window.t === 'function'
                    ? window.t('chat.einoPlanExecuteRound', { n: n, phase: phase })
                    : ('Plan-Execute · 第 ' + n + ' 轮 · ' + phase));
            } else if (data.einoScope === 'main') {
                itemTitle = agPx + (typeof window.t === 'function'
                    ? window.t('chat.einoOrchestratorRound', { n: n })
                    : ('主代理 · 第 ' + n + ' 轮'));
            } else if (data.einoScope === 'sub') {
                const agent = data.einoAgent != null ? String(data.einoAgent).trim() : '';
                itemTitle = agPx + (typeof window.t === 'function'
                    ? window.t('chat.einoSubAgentStep', { n: n, agent: agent })
                    : ('子代理 · ' + agent + ' · 第 ' + n + ' 步'));
            } else {
                itemTitle = agPx + (typeof window.t === 'function' ? window.t('chat.iterationRound', { n: n }) : '第 ' + n + ' 轮迭代');
            }
        } else if (eventType === 'thinking') {
            itemTitle = agPx + '🤔 ' + (typeof window.t === 'function' ? window.t('chat.aiThinking') : 'AI思考');
        } else if (eventType === 'reasoning_chain') {
            itemTitle = agPx + '🔗 ' + (typeof window.t === 'function' ? window.t('chat.reasoningChain') : '推理过程');
        } else if (eventType === 'planning') {
            if (typeof window.einoMainStreamPlanningTitle === 'function') {
                itemTitle = window.einoMainStreamPlanningTitle(data);
            } else {
                itemTitle = agPx + '📝 ' + (typeof window.t === 'function' ? window.t('chat.planning') : '规划中');
            }
        } else if (eventType === 'tool_calls_detected') {
            itemTitle = agPx + '🔧 ' + (typeof window.t === 'function' ? window.t('chat.toolCallsDetected', { count: data.count || 0 }) : '检测到 ' + (data.count || 0) + ' 个工具调用');
        } else if (eventType === 'tool_call') {
            const toolName = data.toolName || (typeof window.t === 'function' ? window.t('chat.unknownTool') : '未知工具');
            const index = data.index || 0;
            const total = data.total || 0;
            const callTitle = typeof window.formatToolCallTimelineTitle === 'function'
                ? window.formatToolCallTimelineTitle(toolName, index, total)
                : (typeof window.t === 'function' ? window.t('chat.callTool', { name: escapeHtml(toolName), index: index, total: total }) : '调用工具: ' + escapeHtml(toolName) + ' (' + index + '/' + total + ')');
            itemTitle = agPx + '🔧 ' + callTitle;
        } else if (eventType === 'tool_result') {
            const toolName = data.toolName || (typeof window.t === 'function' ? window.t('chat.unknownTool') : '未知工具');
            const noResultText = typeof window.t === 'function' ? window.t('timeline.noResult') : '无结果';
            const result = data.result != null ? data.result : (data.error != null ? data.error : (data.resultPreview != null ? data.resultPreview : noResultText));
            const resultStr = typeof result === 'string' ? result : JSON.stringify(result);
            const displayState = typeof window.getToolResultDisplayState === 'function'
                ? window.getToolResultDisplayState(data, { rawText: resultStr })
                : { kind: (data.success !== false ? 'success' : 'error'), isError: data.success === false };
            const backgroundRunning = displayState.kind === 'background_running';
            const success = !displayState.isError && !backgroundRunning;
            const statusIcon = backgroundRunning ? '⏳' : (success ? '✅' : '❌');
            const execText = backgroundRunning
                ? ((typeof window.getBackgroundRunningToolLabel === 'function' ? window.getBackgroundRunningToolLabel() : '后台执行中') + ': ' + escapeHtml(toolName))
                : (success ? (typeof window.t === 'function' ? window.t('chat.toolExecComplete', { name: escapeHtml(toolName) }) : '工具 ' + escapeHtml(toolName) + ' 执行完成') : (typeof window.t === 'function' ? window.t('chat.toolExecFailed', { name: escapeHtml(toolName) }) : '工具 ' + escapeHtml(toolName) + ' 执行失败'));
            let execLine = statusIcon + ' ' + execText;
            if (toolName === BuiltinTools.SEARCH_KNOWLEDGE_BASE && success) {
                execLine = '📚 ' + execLine + ' - ' + (typeof window.t === 'function' ? window.t('chat.knowledgeRetrievalTag') : '知识检索');
            }
            itemTitle = agPx + execLine;
        } else if (eventType === 'eino_agent_reply') {
            itemTitle = agPx + '💬 ' + (typeof window.t === 'function' ? window.t('chat.einoAgentReplyTitle') : '子代理回复');
        } else if (eventType === 'eino_empty_response_continue') {
            itemTitle = typeof window.t === 'function'
                ? window.t('chat.einoEmptyResponseContinueTitle')
                : '🔁 自动续跑（无助手正文）';
        } else if (eventType === 'eino_run_retry') {
            itemTitle = formatProcessDetailEinoRunRetryTitle(data);
            detail.message = formatProcessDetailEinoRunRetryMessage(title, data);
        } else if (eventType === 'knowledge_retrieval') {
            itemTitle = '📚 ' + (typeof window.t === 'function' ? window.t('chat.knowledgeRetrieval') : '知识检索');
        } else if (eventType === 'error') {
            itemTitle = '❌ ' + (typeof window.t === 'function' ? window.t('chat.error') : '错误');
        } else if (eventType === 'cancelled') {
            itemTitle = '⛔ ' + (typeof window.t === 'function' ? window.t('chat.taskCancelled') : '任务已取消');
        } else if (eventType === 'hitl_interrupt') {
            const hitlMsg = (detail.message && String(detail.message).trim()) ? String(detail.message).trim() : (typeof window.t === 'function' ? window.t('hitl.pendingTitle') : '待审批');
            itemTitle = agPx + '🧑‍⚖️ HITL · ' + hitlMsg;
        } else if (eventType === 'hitl_audit_agent_started') {
            itemTitle = agPx + '审计 Agent 正在审查';
        } else if (eventType === 'hitl_audit_agent') {
            itemTitle = agPx + '审计 Agent 已完成审查';
        } else if (eventType === 'hitl_resumed') {
            itemTitle = agPx + '审批已通过';
        } else if (eventType === 'hitl_rejected') {
            itemTitle = agPx + '审批已拒绝';
        } else if (eventType === 'progress') {
            itemTitle = typeof window.translateProgressMessage === 'function' ? window.translateProgressMessage(detail.message || '') : (detail.message || '');
        } else if (eventType === 'user_interrupt_continue') {
            itemTitle = typeof window.t === 'function'
                ? window.t('chat.userInterruptContinueTitle')
                : '⏸️ 用户中断并继续';
        }

        if (eventType === 'hitl_interrupt' || eventType === 'hitl_audit_agent_started' ||
            eventType === 'hitl_audit_agent' || eventType === 'hitl_resumed' || eventType === 'hitl_rejected') {
            const hitlTarget = typeof findToolCallItemForHitl === 'function'
                ? findToolCallItemForHitl(timeline, data)
                : null;
            if (hitlTarget && hitlTarget.id) {
                if (eventType === 'hitl_interrupt' || eventType === 'hitl_audit_agent_started') {
                    renderInlineHitlApproval(hitlTarget.id, Object.assign({}, data, {
                        reviewer: eventType === 'hitl_audit_agent_started' ? 'audit_agent' : (data.reviewer || 'human'),
                        status: eventType === 'hitl_audit_agent_started' ? 'audit_running' : (data.status || 'pending')
                    }));
                } else {
                    const decision = eventType === 'hitl_rejected' || data.decision === 'reject' ? 'reject' : 'approve';
                    resolveInlineHitlDecision(timeline, Object.assign({}, data, {
                        reviewer: data.reviewer || data.decidedBy || (eventType === 'hitl_audit_agent' ? 'audit_agent' : 'human'),
                        status: 'decided'
                    }), decision, detail.message || '');
                }
                return;
            }
        }

        const processDetailId = detail.id || data.processDetailId || '';
        const timelineOpts = {
            title: itemTitle,
            message: detail.message || '',
            data: data,
            processDetailId: processDetailId,
            createdAt: detail.createdAt
        };
        if (eventType === 'tool_call' && data._mergedResult) {
            timelineOpts.mergedResult = data._mergedResult;
            if (typeof window.getToolResultDisplayState === 'function') {
                const displayState = window.getToolResultDisplayState(data._mergedResult);
                if (displayState && displayState.kind === 'background_running') {
                    timelineOpts.toolStatus = 'background_running';
                }
            }
        }
        if (eventType === 'tool_call' && detail.id && toolStatusByProcessDetailId.has(String(detail.id))) {
            timelineOpts.toolStatus = toolStatusByProcessDetailId.get(String(detail.id));
        }
        const itemId = addTimelineItem(timeline, eventType, timelineOpts);
        if (itemId && (eventType === 'hitl_interrupt' || eventType === 'hitl_audit_agent_started')) {
            renderInlineHitlApproval(itemId, Object.assign({}, data, {
                reviewer: eventType === 'hitl_audit_agent_started' ? 'audit_agent' : (data.reviewer || 'human'),
                status: eventType === 'hitl_audit_agent_started' ? 'audit_running' : (data.status || 'pending')
            }));
        } else if (itemId && (eventType === 'hitl_audit_agent' || eventType === 'hitl_resumed' || eventType === 'hitl_rejected')) {
            renderInlineHitlApproval(itemId, Object.assign({}, data, {
                resolved: true,
                decision: eventType === 'hitl_rejected' || data.decision === 'reject' ? 'reject' : 'approve',
                reviewer: data.reviewer || data.decidedBy || (eventType === 'hitl_audit_agent' ? 'audit_agent' : 'human'),
                status: 'decided'
            }));
        }
        if (prependMode && itemId) {
            prependedIds.push(itemId);
        }
    }

    function finishPrependRender() {
        if (!prependMode || prependedIds.length === 0) return;
        const fragment = document.createDocumentFragment();
        prependedIds.forEach((id) => {
            const node = document.getElementById(id);
            if (node) fragment.appendChild(node);
        });
        timeline.insertBefore(fragment, prependAnchor || timeline.firstChild);
        if (prependScrollBox) {
            const delta = prependScrollBox.scrollHeight - prependScrollHeight;
            prependScrollBox.scrollTop = prependScrollTop + delta;
        }
    }

    const TIMELINE_RENDER_BATCH = 40;
    const renderTimelineBatch = (startIdx) => {
        const endIdx = Math.min(startIdx + TIMELINE_RENDER_BATCH, processDetails.length);
        for (let i = startIdx; i < endIdx; i++) {
            renderOneProcessDetail(processDetails[i]);
        }
        if (endIdx < processDetails.length) {
            requestAnimationFrame(() => renderTimelineBatch(endIdx));
        } else if (markLoaded) {
            finishPrependRender();
            finishProcessDetailsRender(messageElement, processDetails, isLazyNotLoaded, timeline);
        } else {
            finishPrependRender();
        }
    };
    if (processDetails.length > TIMELINE_RENDER_BATCH) {
        renderTimelineBatch(0);
    } else {
        processDetails.forEach(renderOneProcessDetail);
        finishPrependRender();
        if (markLoaded) {
            finishProcessDetailsRender(messageElement, processDetails, isLazyNotLoaded, timeline);
        }
    }
}

function finishProcessDetailsRender(messageElement, processDetails, isLazyNotLoaded, timeline) {
    if (isLazyNotLoaded && getMessageReasoningContent(messageElement)) {
        const lazyHint = document.createElement('div');
        lazyHint.className = 'progress-timeline-empty progress-timeline-lazy-hint';
        lazyHint.textContent = (typeof window.t === 'function' ? window.t('chat.expandDetail') : '展开详情') +
            '（点击后加载完整过程详情）';
        timeline.appendChild(lazyHint);
        bindProcessDetailsLazyHint(lazyHint, messageElement.id);
    }

    const hasPendingHitlInDetails = processDetails.some(d => d && d.eventType === 'hitl_interrupt');
    const hasPendingWorkflowHitl = processDetails.some(d => d && d.eventType === 'workflow_hitl_waiting');
    const hasErrorOrCancelled = processDetails.some(d =>
        d.eventType === 'error' || d.eventType === 'cancelled'
    );
    const userExpanded = isProcessDetailsUserExpanded(messageElement.id);
    if (userExpanded) {
        timeline.classList.add('expanded');
        syncProcessDetailButtonLabels(messageElement.id, true);
    } else if (hasErrorOrCancelled && !hasPendingHitlInDetails && !hasPendingWorkflowHitl) {
        timeline.classList.remove('expanded');
        syncProcessDetailButtonLabels(messageElement.id, false);
    }
    if (hasPendingWorkflowHitl && messageElement && messageElement.id) {
        const convId = typeof window.currentConversationId === 'string' ? window.currentConversationId : '';
        if (convId && typeof window.restoreWorkflowHitlInlineForConversation === 'function') {
            window.restoreWorkflowHitlInlineForConversation(convId);
        }
    }
    if (typeof window.ensureProcessDetailsReturnLatestControl === 'function') {
        window.ensureProcessDetailsReturnLatestControl(timeline);
    }
    if (typeof window.updateProcessDetailsReturnLatestControl === 'function') {
        window.updateProcessDetailsReturnLatestControl(timeline);
    }
}

/** 懒加载折叠态：后台拉摘要，提示迭代规模而不加载全量详情 */
function prefetchProcessDetailsSummaryHint(messageId, messageElement) {
    if (!messageElement || !messageElement.dataset || !messageElement.dataset.backendMessageId) return;
    const backendId = String(messageElement.dataset.backendMessageId).trim();
    if (!backendId || typeof apiFetch !== 'function') return;
    const detailsContainer = document.getElementById('process-details-' + messageId);
    if (!detailsContainer || detailsContainer.dataset.summaryFetched === '1') return;
    detailsContainer.dataset.summaryFetched = '1';
    apiFetch('/api/messages/' + encodeURIComponent(backendId) + '/process-details?summary=1')
        .then(async (res) => {
            const j = await res.json().catch(() => ({}));
            if (!res.ok || !j.summary) return;
            const s = j.summary;
            if (typeof window.setAssistantTurnTiming === 'function') {
                window.setAssistantTurnTiming(messageElement, {
                    startedAt: s.startedAt,
                    completedAt: s.completedAt,
                    durationMs: s.durationMs,
                    status: s.status || 'completed'
                });
            }
            const summaryMcpIds = Array.isArray(s.mcpExecutionIds) ? s.mcpExecutionIds : [];
            const summaryTools = Array.isArray(s.toolExecutions) ? s.toolExecutions : [];
            if (summaryTools.length > 0) {
                setPendingToolExecutionSummaries(messageElement, summaryTools);
            }
            if (summaryMcpIds.length > 0) {
                setPendingMcpExecutionIds(messageElement, summaryMcpIds);
            }
            const summaryToolExecutionCount = summaryTools
                .map(normalizeToolExecutionSummaryForButton)
                .filter((item) => item.executionId)
                .length;
            const buttonToolCount = summaryToolExecutionCount > 0 ? summaryToolExecutionCount : summaryMcpIds.length;
            if (buttonToolCount > 0) {
                setMcpExecutionSummaryCount(messageElement, buttonToolCount);
            }
            const timeline = detailsContainer.querySelector('.progress-timeline');
            if (!timeline || detailsContainer.dataset.loaded === '1') return;
            const expandLabel = typeof window.t === 'function' ? window.t('chat.expandDetail') : '展开详情';
            let hint = expandLabel + '（点击后加载迭代详情）';
            if (s.maxIteration > 0) {
                hint = expandLabel + '（共 ' + s.maxIteration + ' 轮迭代，' + (s.total || 0) + ' 条详情）';
            } else if (s.total > 0) {
                hint = expandLabel + '（共 ' + (s.total || 0) + ' 条详情）';
            }
            const empty = timeline.querySelector('.progress-timeline-empty');
            if (empty) {
                empty.textContent = hint;
                bindProcessDetailsLazyHint(timeline, messageId);
            }
        })
        .catch(() => {});
}

// 移除消息
function removeMessage(id) {
    const messageDiv = document.getElementById(id);
    if (messageDiv) {
        messageDiv.remove();
    }
}

// 输入框事件绑定（Enter 发送、Shift+Enter 换行 / @提及）