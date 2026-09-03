
document.addEventListener('languagechange', function () {
    const hid = document.getElementById('agent-mode-select');
    if (!hid) return;
    const v = hid.value;
    if (chatAgentModeIsEinoSingle(v) || chatAgentModeIsEino(v)) {
        syncAgentModeFromValue(v);
    }
    if (typeof updateChatReasoningSummary === 'function') {
        updateChatReasoningSummary();
    }
});

// 保存输入框草稿到localStorage（防抖版本）
function saveChatDraftDebounced(content) {
    // 清除之前的定时器
    if (draftSaveTimer) {
        clearTimeout(draftSaveTimer);
    }

    // 设置新的定时器
    draftSaveTimer = setTimeout(() => {
        saveChatDraft(content);
    }, DRAFT_SAVE_DELAY);
}

// 保存输入框草稿到localStorage
function saveChatDraft(content) {
    try {
        const chatInput = document.getElementById('chat-input');
        const placeholderText = chatInput ? (chatInput.getAttribute('placeholder') || '').trim() : '';
        const trimmed = (content || '').trim();

        // 不要把占位提示本身当作草稿保存
        if (trimmed && (!placeholderText || trimmed !== placeholderText)) {
            localStorage.setItem(DRAFT_STORAGE_KEY, content);
        } else {
            // 如果内容为空或等于占位提示，清除保存的草稿
            localStorage.removeItem(DRAFT_STORAGE_KEY);
        }
    } catch (error) {
        // localStorage可能已满或不可用，静默失败
        logger.warn('保存草稿失败:', error);
    }
}

// 从localStorage恢复输入框草稿
function restoreChatDraft() {
    try {
        const chatInput = document.getElementById('chat-input');
        if (!chatInput) {
            return;
        }
        const placeholderText = (chatInput.getAttribute('placeholder') || '').trim();
        // 若当前 value 与 placeholder 相同，说明提示被误当作内容，清空以便正确显示占位符
        if (placeholderText && chatInput.value.trim() === placeholderText) {
            chatInput.value = '';
        }
        // 如果输入框已有内容，不恢复草稿（避免覆盖用户输入）
        if (chatInput.value && chatInput.value.trim().length > 0) {
            return;
        }

        const draft = localStorage.getItem(DRAFT_STORAGE_KEY);
        const trimmedDraft = draft ? draft.trim() : '';

        // 如果草稿内容和占位提示一样，则认为是无效草稿，不恢复
        if (trimmedDraft && (!placeholderText || trimmedDraft !== placeholderText)) {
            chatInput.value = draft;
            // 调整输入框高度以适应内容
            adjustTextareaHeight(chatInput);
        } else if (trimmedDraft && placeholderText && trimmedDraft === placeholderText) {
            // 清理掉无效草稿，避免之后继续干扰
            localStorage.removeItem(DRAFT_STORAGE_KEY);
        }
    } catch (error) {
        logger.warn('恢复草稿失败:', error);
    }
}

// 清除保存的草稿
function clearChatDraft() {
    try {
        // 同步清除，确保立即生效
        localStorage.removeItem(DRAFT_STORAGE_KEY);
    } catch (error) {
        logger.warn('清除草稿失败:', error);
    }
}

// 调整textarea高度以适应内容
function adjustTextareaHeight(textarea) {
    if (!textarea) return;

    // 先重置高度为auto，然后立即设置为固定值，确保能准确获取scrollHeight
    textarea.style.height = 'auto';
    // 强制浏览器重新计算布局
    void textarea.offsetHeight;

    // 计算新高度（最小40px，最大不超过300px）
    const scrollHeight = textarea.scrollHeight;
    const newHeight = Math.min(Math.max(scrollHeight, 40), 300);
    textarea.style.height = newHeight + 'px';

    // 如果内容为空或只有很少内容，立即重置到最小高度
    if (!textarea.value || textarea.value.trim().length === 0) {
        textarea.style.height = '40px';
    }
}

// 发送消息
async function sendMessage() {
    const input = document.getElementById('chat-input');
    let message = input.value.trim();
    const hasAttachments = chatAttachments && chatAttachments.length > 0;
    const requestConversationId = currentConversationId;
    const requestNavigationSeq = chatConversationNavigationSeq;

    if (!message && !hasAttachments) {
        return;
    }

    // A restored conversation renders from the local cache first, while its
    // authoritative HITL config is fetched separately. Do not let a fast send
    // reuse the temporary/default reviewer (historically "human") before that
    // fetch completes, otherwise refreshing could turn Audit Agent review into
    // a human approval for the next tool call.
    const hitlConversationAtSendStart = String(currentConversationId || '').trim();
    await waitForHitlConfigReady(hitlConversationAtSendStart);
    if (String(currentConversationId || '').trim() !== hitlConversationAtSendStart) return;

    // Enter 会直接调用 sendMessage；同一会话在其他标签页已启动任务时，
    // 必须在渲染用户气泡和发起 POST 前做一次权威状态同步，避免生成一轮“已有任务执行中”伪对话。
    if (currentConversationId && typeof loadActiveTasks === 'function') {
        await loadActiveTasks();
    }
    if (isCurrentChatTaskActive()) {
        updateChatPrimaryActionState();
        showChatToast(chatTranslate('chat.taskAlreadyRunning', '当前会话已有任务正在执行，请先等待完成或停止任务。'), 'info');
        return;
    }

    if (hasAttachments) {
        const needWait = chatAttachments.some((a) => a.uploading);
        if (needWait) {
            const waitLabel = (typeof window.t === 'function')
                ? window.t('chat.waitingAttachmentsUpload')
                : '正在等待附件上传完成…';
            chatAttachmentProgressSet(true, 0, waitLabel);
        }
        try {
            await Promise.all(chatAttachments.map((a) => (a.uploadPromise ? a.uploadPromise : Promise.resolve())));
        } finally {
            refreshChatAttachmentUploadProgress();
        }
        const bad = chatAttachments.filter((a) => !a.serverPath);
        if (bad.length) {
            const hint = (typeof window.t === 'function')
                ? window.t('chat.attachmentsUploadIncomplete')
                : '部分附件未上传成功，请移除失败项或重新选择文件后再发送。';
            alert(hint);
            return;
        }
    }

    // 有附件且用户未输入时，发一句简短默认提示即可（后端会拼接路径和文件内容给大模型）
    if (hasAttachments && !message) {
        message = CHAT_FILE_DEFAULT_PROMPT;
    }

    // 发送前的任务状态/附件检查可能包含异步等待。若用户已主动切换会话，
    // 保留当前页面，不再把这次尚未发出的请求写入新的可见对话。
    if (requestNavigationSeq !== chatConversationNavigationSeq) {
        return;
    }

    // 显示用户消息（含附件名，便于用户确认）
    const displayMessage = hasAttachments
        ? message + '\n' + chatAttachments.map(a => '📎 ' + a.fileName).join('\n')
        : message;
    if (window.CyberStrikeChatScroll) {
        window.CyberStrikeChatScroll.onUserSendMessage();
    }
    addMessage('user', displayMessage, null, null, null, { scroll: 'none' });
    if (currentConversationId) {
        invalidateConversationLiteCache(currentConversationId);
    }

    // 清除防抖定时器，防止在清空输入框后重新保存草稿
    if (draftSaveTimer) {
        clearTimeout(draftSaveTimer);
        draftSaveTimer = null;
    }

    // 立即清除草稿，防止页面刷新时恢复
    clearChatDraft();
    // 使用同步方式确保草稿被清除
    try {
        localStorage.removeItem(DRAFT_STORAGE_KEY);
    } catch (e) {
        // 忽略错误
    }

    // 立即清空输入框并清除草稿（在发送请求之前）
    input.value = '';
    // 强制重置输入框高度为初始高度（40px）
    input.style.height = '40px';

    // 构建请求体（含附件）
    const body = {
        message: message,
        conversationId: requestConversationId,
        role: typeof getCurrentRole === 'function' ? getCurrentRole() : ''
    };
    if (window.__csNextChatFinalizationPolicy && typeof window.__csNextChatFinalizationPolicy === 'object') {
        body.finalization = window.__csNextChatFinalizationPolicy;
        window.__csNextChatFinalizationPolicy = null;
    }
    let streamConversationId = body.conversationId ? String(body.conversationId) : null;
    const isStreamStillVisibleForRequest = function () {
        if (!document.getElementById(progressId)) return false;
        if (!streamConversationId) return currentConversationId === body.conversationId;
        return currentConversationId === streamConversationId;
    };
    if (!currentConversationId && typeof getActiveProjectId === 'function') {
        const pid = getActiveProjectId();
        if (pid) body.projectId = pid;
    }
    const aiChannelId = selectedChatAIChannelId();
    if (aiChannelId) {
        body.aiChannelId = aiChannelId;
    }
    const hitlCfg = readHitlConfigFromForm();
    if (normalizeHitlMode(hitlCfg.mode) !== HITL_MODE_OFF) {
        const sensitiveTools = hitlToolsSplitToArray(hitlCfg.sensitiveTools || '');
        body.hitl = {
            enabled: true,
            mode: normalizeHitlMode(hitlCfg.mode),
            reviewer: normalizeHitlReviewer(hitlCfg.reviewer),
            sensitiveTools: sensitiveTools,
            timeoutSeconds: normalizeHitlTimeoutForChat(hitlCfg.timeoutSeconds, DEFAULT_HITL_TIMEOUT_SECONDS)
        };
    }
    if (hasAttachments) {
        body.attachments = chatAttachments.map((a) => ({
            fileName: a.fileName,
            mimeType: a.mimeType || '',
            serverPath: a.serverPath
        }));
    }
    const reasoningPayload = buildReasoningRequestPayload();
    if (reasoningPayload) {
        body.reasoning = reasoningPayload;
    }
    // 发送后清空附件列表
    chatAttachments = [];
    renderChatFileChips();

    // 创建进度消息容器（使用详细的进度展示）
    const progressId = addProgressMessage();
    if (window.CyberStrikeChatScroll) {
        window.CyberStrikeChatScroll.markProgressStreaming(true, progressId);
        window.CyberStrikeChatScroll.onUserSendMessage();
    }
    const progressElement = document.getElementById(progressId);
    registerProgressTask(progressId, streamConversationId);
    const requestAbortController = new AbortController();
    const liveStreamState = {
        active: true,
        conversationId: streamConversationId || null,
        progressId: progressId,
        abortController: requestAbortController,
        detached: false,
        navigationSeq: requestNavigationSeq
    };
    window.__csAgentLiveStream = liveStreamState;
    if (streamConversationId && typeof window.notifyConversationTaskStarted === 'function') {
        window.notifyConversationTaskStarted(streamConversationId);
    }
    updateChatPrimaryActionState();
    loadActiveTasks();
    let assistantMessageId = null;
    let mcpExecutionIds = [];

    try {
        const modeSel = document.getElementById('agent-mode-select');
        let modeVal = modeSel ? modeSel.value : CHAT_AGENT_MODE_EINO_SINGLE;
        saveConversationAgentModePreference(streamConversationId || currentConversationId, modeVal);
        const useMulti = multiAgentAPIEnabled && chatAgentModeIsEino(modeVal);
        const streamPath = useMulti ? '/api/multi-agent/stream' : '/api/eino-agent/stream';
        if (useMulti && modeVal) {
            body.orchestration = modeVal;
        }
        const response = await apiFetch(streamPath, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify(body),
            signal: requestAbortController.signal,
        });

        if (!response.ok) {
            throw new Error('请求失败: ' + response.status);
        }

        liveStreamState.conversationId = streamConversationId || null;
        try {
            const reader = response.body.getReader();
            const decoder = new TextDecoder();
            let buffer = '';
            let streamSawDone = false;
            const dispatchStreamEvent = function (eventData) {
                if (eventData && eventData.type === 'done') {
                    streamSawDone = true;
                }
                const eventConvId = eventData && eventData.data && eventData.data.conversationId
                    ? String(eventData.data.conversationId)
                    : '';
                let justBoundConversation = false;
                if (eventConvId) {
                    if (streamConversationId && streamConversationId !== eventConvId) {
                        return;
                    }
                    if (!streamConversationId) {
                        streamConversationId = eventConvId;
                        liveStreamState.conversationId = eventConvId;
                        justBoundConversation = true;
                    }
                }
                // 切换对话后仍可能收到旧响应流中已缓冲的 conversation、response_start
                // 或 response 事件。它们只能补齐后台任务归属，不能重新抢占当前对话。
                if (shouldIgnoreLiveChatStreamEvent(liveStreamState)) {
                    if (eventConvId) updateProgressConversation(progressId, eventConvId);
                    return;
                }
                if (!justBoundConversation && !isStreamStillVisibleForRequest()) {
                    return;
                }
                handleStreamEvent(eventData, progressElement, progressId,
                    () => assistantMessageId, (id) => { assistantMessageId = id; },
                    () => mcpExecutionIds, (ids) => { mcpExecutionIds = ids; },
                    { conversationId: streamConversationId });
            };
            const processSseLines = typeof processSseDataLinesYielding === 'function'
                ? processSseDataLinesYielding
                : async function (lines, onEvent) {
                    for (const line of lines) {
                        if (line.startsWith('data: ')) {
                            try {
                                onEvent(JSON.parse(line.slice(6)));
                            } catch (e) {
                                logger.error('解析事件数据失败:', e, line);
                            }
                        }
                    }
                };

            while (true) {
                const { done, value } = await reader.read();
                if (done) break;

                buffer += decoder.decode(value, { stream: true });
                const lines = buffer.split('\n');
                buffer = lines.pop(); // 保留最后一个不完整的行

                await processSseLines(lines, dispatchStreamEvent);
            }
            // Flush decoder internal buffer to avoid losing the final partial UTF-8 code point.
            buffer += decoder.decode();

            // 处理剩余的buffer
            if (buffer.trim()) {
                const lines = buffer.split('\n');
                await processSseLines(lines, dispatchStreamEvent);
            }
            if (!streamSawDone) {
                if (typeof loadActiveTasks === 'function') {
                    loadActiveTasks();
                }
                const convId = streamConversationId || (body && body.conversationId) || null;
                let attached = false;
                if (
                    convId &&
                    ownsLiveChatStream(liveStreamState) &&
                    !liveStreamState.detached &&
                    isStreamStillVisibleForRequest() &&
                    typeof window.attachRunningTaskEventStream === 'function'
                ) {
                    clearLiveChatStreamIfOwned(liveStreamState);
                    attached = await window.attachRunningTaskEventStream(convId).catch(() => false);
                }
                if (!attached && isStreamStillVisibleForRequest()) {
                    const hint = typeof window.t === 'function'
                        ? window.t('chat.streamEndedWithoutDone')
                        : '连接提前结束，未收到任务完成信号。任务可能仍在后端执行，请查看顶部运行中任务或刷新当前对话。';
                    addMessage('system', hint);
                }
            }
        } finally {
            const clearedOwnedStream = clearLiveChatStreamIfOwned(liveStreamState);
            if (clearedOwnedStream && !liveStreamState.detached && window.CyberStrikeChatScroll) {
                window.CyberStrikeChatScroll.onStreamEnd();
            }
        }

        // 消息发送成功后，再次确保草稿被清除
        clearChatDraft();
        try {
            localStorage.removeItem(DRAFT_STORAGE_KEY);
        } catch (e) {
            // 忽略错误
        }

    } catch (error) {
        clearLiveChatStreamIfOwned(liveStreamState);
        if (liveStreamState.detached || !isStreamStillVisibleForRequest()) {
            if (typeof loadActiveTasks === 'function') {
                loadActiveTasks();
            }
            return;
        }
        removeMessage(progressId);
        const msg = error && error.message != null ? String(error.message) : String(error);
        const isNetwork = /network|fetch|Failed to fetch|aborted|AbortError|load failed|NetworkError/i.test(msg);
        if (isNetwork && typeof window.t === 'function') {
            addMessage('system', window.t('chat.streamNetworkErrorHint', { detail: msg }));
        } else if (isNetwork) {
            addMessage('system', '连接已中断（' + msg + '）。长时间任务可能仍在后端执行，请查看顶部运行中任务或稍后刷新对话。');
        } else {
            addMessage('system', '错误: ' + msg);
        }
        if (typeof loadActiveTasks === 'function') {
            loadActiveTasks();
        }
        // 发送失败时，不恢复草稿，因为消息已经显示在对话框中了
    }
}

// ---------- 对话文件上传 ----------
function renderChatFileChips() {
    const list = document.getElementById('chat-file-list');
    if (!list) return;
    list.innerHTML = '';
    if (!chatAttachments.length) return;
    chatAttachments.forEach((a, i) => {
        const chip = document.createElement('div');
        chip.className = 'chat-file-chip';
        if (a.uploading) chip.classList.add('chat-file-chip--uploading');
        if (a.uploadError) chip.classList.add('chat-file-chip--error');
        chip.setAttribute('role', 'listitem');
        const name = document.createElement('span');
        name.className = 'chat-file-chip-name';
        name.title = a.fileName;
        let label = a.fileName;
        if (a.uploading) {
            label += ' · ' + ((typeof window.t === 'function') ? window.t('chat.attachmentUploading') : '上传中…');
        } else if (a.uploadError) {
            label += ' · ' + ((typeof window.t === 'function') ? window.t('chat.attachmentUploadFailed') : '失败');
        }
        name.textContent = label;
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'chat-file-chip-remove';
        remove.title = typeof window.t === 'function' ? window.t('common.remove') : '移除';
        remove.innerHTML = '×';
        remove.setAttribute('aria-label', '移除 ' + a.fileName);
        remove.addEventListener('click', () => removeChatAttachment(i));
        chip.appendChild(name);
        chip.appendChild(remove);
        list.appendChild(chip);
    });
}

function removeChatAttachment(index) {
    chatAttachments.splice(index, 1);
    renderChatFileChips();
    refreshChatAttachmentUploadProgress();
}

// 有附件且输入框为空时，填入一句默认提示（可编辑）；后端会单独拼接路径与内容给大模型
function appendChatFilePrompt() {
    const input = document.getElementById('chat-input');
    if (!input || !chatAttachments.length) return;
    if (!input.value.trim()) {
        input.value = CHAT_FILE_DEFAULT_PROMPT;
        adjustTextareaHeight(input);
    }
}

function chatAttachmentProgressSet(visible, percent, detailText) {
    const wrap = document.getElementById('chat-attachment-progress');
    const fill = document.getElementById('chat-attachment-progress-fill');
    const label = document.getElementById('chat-attachment-progress-label');
    if (!wrap || !fill || !label) return;
    if (!visible) {
        wrap.hidden = true;
        fill.style.width = '0%';
        label.textContent = '';
        return;
    }
    wrap.hidden = false;
    const p = Math.min(100, Math.max(0, Math.round(percent)));
    fill.style.width = p + '%';
    label.textContent = detailText || '';
}

function refreshChatAttachmentUploadProgress() {
    if (!chatAttachments.length) {
        chatAttachmentProgressSet(false);
        return;
    }
    const uploading = chatAttachments.filter((a) => a.uploading);
    if (!uploading.length) {
        chatAttachmentProgressSet(false);
        return;
    }
    let sum = 0;
    chatAttachments.forEach((a) => {
        sum += a.uploading ? (a.uploadPercent || 0) : 100;
    });
    const overall = Math.round(sum / chatAttachments.length);
    const line = (typeof window.t === 'function')
        ? window.t('chat.uploadingAttachmentsDetail', {
            done: chatAttachments.length - uploading.length,
            total: chatAttachments.length,
            percent: overall
        })
        : ('上传附件 ' + (chatAttachments.length - uploading.length) + '/' + chatAttachments.length + ' · ' + overall + '%');
    chatAttachmentProgressSet(true, overall, line);
}

async function uploadOneChatAttachment(entry, file) {
    const form = new FormData();
    form.append('file', file);
    const conv = currentConversationId;
    if (conv && String(conv).trim()) {
        form.append('conversationId', String(conv).trim());
    }
    const entryId = entry.id;
    try {
        const res = typeof apiUploadWithProgress === 'function'
            ? await apiUploadWithProgress('/api/chat-uploads', form, {
                onProgress: function (p) {
                    const cur = chatAttachments.find((x) => x.id === entryId);
                    if (cur) {
                        cur.uploadPercent = p.percent;
                        refreshChatAttachmentUploadProgress();
                    }
                }
            })
            : await apiFetch('/api/chat-uploads', { method: 'POST', body: form });
        if (!res.ok) {
            throw new Error(await res.text());
        }
        const data = await res.json().catch(() => ({}));
        const abs = data.absolutePath ? String(data.absolutePath).trim() : '';
        if (!abs) {
            throw new Error('no absolutePath in response');
        }
        const cur = chatAttachments.find((x) => x.id === entryId);
        if (cur) {
            cur.serverPath = abs;
            cur.uploading = false;
            cur.uploadPercent = 100;
            cur.uploadError = null;
        }
    } catch (e) {
        const msg = (e && e.message) ? e.message : String(e);
        const cur = chatAttachments.find((x) => x.id === entryId);
        if (cur) {
            cur.uploading = false;
            cur.uploadError = msg;
            cur.serverPath = null;
        }
        // F5：附件上传失败 toast 替代 alert
        const uploadFailMsg = ((typeof window.t === 'function') ? window.t('chat.attachmentUploadAlert', { name: file.name }) : ('上传失败：' + file.name)) + '\n' + msg;
        if (typeof window.showChatToast === 'function') window.showChatToast(uploadFailMsg, 'error');
        else if (typeof window.showToast === 'function') window.showToast(uploadFailMsg, 'error');
        else alert(uploadFailMsg);
    }
    renderChatFileChips();
    refreshChatAttachmentUploadProgress();
}

async function addFilesToChat(files) {
    if (!files || !files.length) return;
    const next = Array.from(files);
    if (chatAttachments.length + next.length > MAX_CHAT_FILES) {
        // F5：超限 toast 替代 alert
        const limitMsg = '最多同时上传 ' + MAX_CHAT_FILES + ' 个文件，当前已选 ' + chatAttachments.length + ' 个。';
        if (typeof window.showChatToast === 'function') window.showChatToast(limitMsg, 'warning');
        else if (typeof window.showToast === 'function') window.showToast(limitMsg, 'warning');
        else alert(limitMsg);
        return;
    }
    next.forEach((file) => {
        const id = ++chatAttachmentSeq;
        const entry = {
            id: id,
            fileName: file.name,
            mimeType: file.type || '',
            serverPath: null,
            uploading: true,
            uploadPercent: 0,
            uploadPromise: null,
            uploadError: null
        };
        entry.uploadPromise = uploadOneChatAttachment(entry, file);
        chatAttachments.push(entry);
    });
    renderChatFileChips();
    refreshChatAttachmentUploadProgress();
    appendChatFilePrompt();
}

function setupChatFileUpload() {
    const inputEl = document.getElementById('chat-file-input');
    const container = document.getElementById('chat-input-container') || document.querySelector('.chat-input-container');
    if (!inputEl || !container) return;

    inputEl.addEventListener('change', function () {
        const files = this.files;
        if (files && files.length) {
            addFilesToChat(files).catch(function () { /* addFilesToChat 已提示 */ });
        }
        this.value = '';
    });

    container.addEventListener('dragover', function (e) {
        e.preventDefault();
        e.stopPropagation();
        this.classList.add('drag-over');
    });
    container.addEventListener('dragleave', function (e) {
        e.preventDefault();
        e.stopPropagation();
        if (!this.contains(e.relatedTarget)) {
            this.classList.remove('drag-over');
        }
    });
    container.addEventListener('drop', function (e) {
        e.preventDefault();
        e.stopPropagation();
        this.classList.remove('drag-over');
        const files = e.dataTransfer && e.dataTransfer.files;
        if (files && files.length) addFilesToChat(files).catch(function () { /* addFilesToChat 已提示 */ });
    });
}

// 确保 chat-input-container 有 id（若模板未写）
function ensureChatInputContainerId() {
    const c = document.querySelector('.chat-input-container');
    if (c && !c.id) c.id = 'chat-input-container';
}

function setupMentionSupport() {
    mentionSuggestionsEl = document.getElementById('mention-suggestions');
    if (mentionSuggestionsEl) {
        mentionSuggestionsEl.style.display = 'none';
        mentionSuggestionsEl.addEventListener('mousedown', (event) => {
            // 防止点击候选项时输入框失焦
            event.preventDefault();
        });
    }
    ensureMentionToolsLoaded().catch(() => {
        // 忽略加载错误，稍后可重试
    });
}

// 刷新工具列表（重置已加载状态，强制重新加载）
function refreshMentionTools() {
    mentionToolsLoaded = false;
    mentionTools = [];
    externalMcpNames = [];
    mentionToolsLoadingPromise = null;
    // 如果当前正在使用@功能，立即触发重新加载
    if (mentionState.active) {
        ensureMentionToolsLoaded().catch(() => {
            // 忽略加载错误
        });
    }
}

// 将刷新函数暴露到window对象，供其他模块调用
if (typeof window !== 'undefined') {
    window.refreshMentionTools = refreshMentionTools;
}

function ensureMentionToolsLoaded() {
    // 检查角色是否改变，如果改变则强制重新加载
    if (typeof window !== 'undefined' && window._mentionToolsRoleChanged) {
        mentionToolsLoaded = false;
        mentionTools = [];
        delete window._mentionToolsRoleChanged;
    }

    if (mentionToolsLoaded) {
        return Promise.resolve(mentionTools);
    }
    if (mentionToolsLoadingPromise) {
        return mentionToolsLoadingPromise;
    }
    mentionToolsLoadingPromise = fetchMentionTools().finally(() => {
        mentionToolsLoadingPromise = null;
    });
    return mentionToolsLoadingPromise;
}

// 生成工具的唯一标识符，用于区分同名但来源不同的工具
function getToolKeyForMention(tool) {
    // 如果是外部工具，使用 external_mcp::tool.name 作为唯一标识
    // 如果是内部工具，使用 tool.name 作为标识
    if (tool.is_external && tool.external_mcp) {
        return `${tool.external_mcp}::${tool.name}`;
    }
    return tool.name;
}

async function fetchMentionTools() {
    const pageSize = 100;
    let page = 1;
    let totalPages = 1;
    const seen = new Set();
    const collected = [];

    try {
        // 获取当前选中的角色（从 roles.js 的函数获取）
        const roleName = typeof getCurrentRole === 'function' ? getCurrentRole() : '';

        // 同时获取外部MCP列表
        try {
            const mcpResponse = await apiFetch('/api/external-mcp');
            if (mcpResponse.ok) {
                const mcpData = await mcpResponse.json();
                externalMcpNames = Object.keys(mcpData.servers || {}).filter(name => {
                    const server = mcpData.servers[name];
                    // 只包含已连接且已启用的MCP
                    return server.status === 'connected' &&
                           (server.config.external_mcp_enable || (server.config.enabled && !server.config.disabled));
                });
            }
        } catch (mcpError) {
            logger.warn('加载外部MCP列表失败:', mcpError);
            externalMcpNames = [];
        }

        while (page <= totalPages && page <= 20) {
            // 构建API URL，如果指定了角色，添加role查询参数
            let url = `/api/config/tools?page=${page}&page_size=${pageSize}`;
            if (roleName && roleName !== '默认') {
                url += `&role=${encodeURIComponent(roleName)}`;
            }

            const response = await apiFetch(url);
            if (!response.ok) {
                break;
            }
            const result = await response.json();
            const tools = Array.isArray(result.tools) ? result.tools : [];
            tools.forEach(tool => {
                if (!tool || !tool.name) {
                    return;
                }
                // 使用唯一标识符来去重，而不是只使用工具名称
                const toolKey = getToolKeyForMention(tool);
                if (seen.has(toolKey)) {
                    return;
                }
                seen.add(toolKey);

                // 确定工具在当前角色中的启用状态
                // 如果有 role_enabled 字段，使用它（表示指定了角色）
                // 否则使用 enabled 字段（表示未指定角色或使用所有工具）
                let roleEnabled = tool.enabled !== false;
                if (tool.role_enabled !== undefined && tool.role_enabled !== null) {
                    roleEnabled = tool.role_enabled;
                }

                collected.push({
                    name: tool.name,
                    description: tool.description || '',
                    enabled: tool.enabled !== false, // 工具本身的启用状态
                    roleEnabled: roleEnabled, // 在当前角色中的启用状态
                    isExternal: !!tool.is_external,
                    externalMcp: tool.external_mcp || '',
                    toolKey: toolKey, // 保存唯一标识符
                });
            });
            totalPages = result.total_pages || 1;
            page += 1;
            if (page > totalPages) {
                break;
            }
        }
        mentionTools = collected;
        mentionToolsLoaded = true;
    } catch (error) {
        logger.warn('加载工具列表失败，@提及功能可能不可用:', error);
    }
    return mentionTools;
}

function handleChatInputInput(event) {
    const textarea = event.target;
    updateMentionStateFromInput(textarea);
    // 自动调整输入框高度
    // 使用requestAnimationFrame确保在DOM更新后立即调整，特别是在删除内容时
    requestAnimationFrame(() => {
        adjustTextareaHeight(textarea);
    });
    // 保存输入内容到localStorage（防抖）
    saveChatDraftDebounced(textarea.value);
}

function handleChatInputClick(event) {
    updateMentionStateFromInput(event.target);
}

function handleChatInputKeydown(event) {
    // 如果正在使用输入法输入（IME），回车键应该用于确认候选词，而不是发送消息
    // Safari 可能在确认候选词时先触发 compositionend，再触发 Enter keydown，
    // 因此这里同时使用全局状态和 keyCode 229 兜底。
    if (event.isComposing || isComposing || event.keyCode === 229) {
        return;
    }

    if (mentionState.active && mentionSuggestionsEl && mentionSuggestionsEl.style.display !== 'none') {
        if (event.key === 'ArrowDown') {
            event.preventDefault();
            moveMentionSelection(1);
            return;
        }
        if (event.key === 'ArrowUp') {
            event.preventDefault();
            moveMentionSelection(-1);
            return;
        }
        if (event.key === 'Enter' || event.key === 'Tab') {
            event.preventDefault();
            applyMentionSelection();
            return;
        }
        if (event.key === 'Escape') {
            event.preventDefault();
            deactivateMentionState();
            return;
        }
    }

    // Enter 直接发送；Shift+Enter 保留 textarea 原生换行行为。
    if (event.key === 'Enter' && !event.shiftKey) {
        event.preventDefault();
        void sendMessage();
    }
}

function updateMentionStateFromInput(textarea) {
    if (!textarea) {
        deactivateMentionState();
        return;
    }
    const caret = textarea.selectionStart || 0;
    const textBefore = textarea.value.slice(0, caret);
    const atIndex = textBefore.lastIndexOf('@');

    if (atIndex === -1) {
        deactivateMentionState();
        return;
    }

    // 限制触发字符之前必须是空白或起始位置
    if (atIndex > 0) {
        const boundaryChar = textBefore[atIndex - 1];
        if (boundaryChar && !/\s/.test(boundaryChar) && !'([{，。,.;:!?'.includes(boundaryChar)) {
            deactivateMentionState();
            return;
        }
    }

    const querySegment = textBefore.slice(atIndex + 1);

    if (querySegment.includes(' ') || querySegment.includes('\n') || querySegment.includes('\t') || querySegment.includes('@')) {
        deactivateMentionState();
        return;
    }

    if (querySegment.length > 60) {
        deactivateMentionState();
        return;
    }

    mentionState.active = true;
    mentionState.startIndex = atIndex;
    mentionState.query = querySegment.toLowerCase();
    mentionState.selectedIndex = 0;

    if (!mentionToolsLoaded) {
        renderMentionSuggestions({ showLoading: true });
    } else {
        updateMentionCandidates();
        renderMentionSuggestions();
    }

    ensureMentionToolsLoaded().then(() => {
        if (mentionState.active) {
            updateMentionCandidates();
            renderMentionSuggestions();
        }
    });
}

function updateMentionCandidates() {
    if (!mentionState.active) {
        mentionFilteredTools = [];
        return;
    }
    const normalizedQuery = (mentionState.query || '').trim().toLowerCase();
    let filtered = mentionTools;

    if (normalizedQuery) {
        // 检查是否精确匹配外部MCP名称
        const exactMatchedMcp = externalMcpNames.find(mcpName =>
            mcpName.toLowerCase() === normalizedQuery
        );

        if (exactMatchedMcp) {
            // 如果完全匹配MCP名称，只显示该MCP下的所有工具
            filtered = mentionTools.filter(tool => {
                return tool.externalMcp && tool.externalMcp.toLowerCase() === exactMatchedMcp.toLowerCase();
            });
        } else {
            // 检查是否部分匹配MCP名称
            const partialMatchedMcps = externalMcpNames.filter(mcpName =>
                mcpName.toLowerCase().includes(normalizedQuery)
            );

            // 正常匹配：按工具名称和描述过滤，同时也匹配MCP名称
            filtered = mentionTools.filter(tool => {
                const nameMatch = tool.name.toLowerCase().includes(normalizedQuery);
                const descMatch = tool.description && tool.description.toLowerCase().includes(normalizedQuery);
                const mcpMatch = tool.externalMcp && tool.externalMcp.toLowerCase().includes(normalizedQuery);

                // 如果部分匹配到MCP名称，也包含该MCP下的所有工具
                const mcpPartialMatch = partialMatchedMcps.some(mcpName =>
                    tool.externalMcp && tool.externalMcp.toLowerCase() === mcpName.toLowerCase()
                );

                return nameMatch || descMatch || mcpMatch || mcpPartialMatch;
            });
        }
    }

    filtered = filtered.slice().sort((a, b) => {
        // 如果指定了角色，优先显示在当前角色中启用的工具
        if (a.roleEnabled !== undefined || b.roleEnabled !== undefined) {
            const aRoleEnabled = a.roleEnabled !== undefined ? a.roleEnabled : a.enabled;
            const bRoleEnabled = b.roleEnabled !== undefined ? b.roleEnabled : b.enabled;
            if (aRoleEnabled !== bRoleEnabled) {
                return aRoleEnabled ? -1 : 1; // 启用的工具排在前面
            }
        }

        if (normalizedQuery) {
            // 精确匹配MCP名称的工具优先显示
            const aMcpExact = a.externalMcp && a.externalMcp.toLowerCase() === normalizedQuery;
            const bMcpExact = b.externalMcp && b.externalMcp.toLowerCase() === normalizedQuery;
            if (aMcpExact !== bMcpExact) {
                return aMcpExact ? -1 : 1;
            }

            const aStarts = a.name.toLowerCase().startsWith(normalizedQuery);
            const bStarts = b.name.toLowerCase().startsWith(normalizedQuery);
            if (aStarts !== bStarts) {
                return aStarts ? -1 : 1;
            }
        }
        // 如果指定了角色，使用 roleEnabled；否则使用 enabled
        const aEnabled = a.roleEnabled !== undefined ? a.roleEnabled : a.enabled;
        const bEnabled = b.roleEnabled !== undefined ? b.roleEnabled : b.enabled;
        if (aEnabled !== bEnabled) {
            return aEnabled ? -1 : 1;
        }
        return a.name.localeCompare(b.name, 'zh-CN');
    });

    mentionFilteredTools = filtered;
    if (mentionFilteredTools.length === 0) {
        mentionState.selectedIndex = 0;
    } else if (mentionState.selectedIndex >= mentionFilteredTools.length) {
        mentionState.selectedIndex = 0;
    }
}

function renderMentionSuggestions({ showLoading = false } = {}) {
    if (!mentionSuggestionsEl || !mentionState.active) {
        hideMentionSuggestions();
        return;
    }

    const currentQuery = mentionState.query || '';
    const existingList = mentionSuggestionsEl.querySelector('.mention-suggestions-list');
    const canPreserveScroll = !showLoading &&
        existingList &&
        mentionSuggestionsEl.dataset.lastMentionQuery === currentQuery;
    const previousScrollTop = canPreserveScroll ? existingList.scrollTop : 0;

    if (showLoading) {
        mentionSuggestionsEl.innerHTML = '<div class="mention-empty">' + (typeof window.t === 'function' ? window.t('chat.loadingTools') : '正在加载工具...') + '</div>';
        mentionSuggestionsEl.style.display = 'block';
        delete mentionSuggestionsEl.dataset.lastMentionQuery;
        return;
    }

    if (!mentionFilteredTools.length) {
        mentionSuggestionsEl.innerHTML = '<div class="mention-empty">' + (typeof window.t === 'function' ? window.t('chat.noMatchTools') : '没有匹配的工具') + '</div>';
        mentionSuggestionsEl.style.display = 'block';
        mentionSuggestionsEl.dataset.lastMentionQuery = currentQuery;
        return;
    }

    const itemsHtml = mentionFilteredTools.map((tool, index) => {
        const activeClass = index === mentionState.selectedIndex ? 'active' : '';
        // 如果工具有 roleEnabled 字段（指定了角色），使用它；否则使用 enabled
        const toolEnabled = tool.roleEnabled !== undefined ? tool.roleEnabled : tool.enabled;
        const disabledClass = toolEnabled ? '' : 'disabled';
        const badge = tool.isExternal ? '<span class="mention-item-badge">外部</span>' : '<span class="mention-item-badge internal">内置</span>';
        const nameHtml = escapeHtml(tool.name);
        const description = tool.description && tool.description.length > 0 ? escapeHtml(tool.description) : (typeof window.t === 'function' ? window.t('chat.noDescription') : '暂无描述');
        const descHtml = `<div class="mention-item-desc">${description}</div>`;
        // 根据工具在当前角色中的启用状态显示状态标签
        const statusLabel = toolEnabled ? '可用' : (tool.roleEnabled !== undefined ? '已禁用（当前角色）' : '已禁用');
        const statusClass = toolEnabled ? 'enabled' : 'disabled';
        const originLabel = tool.isExternal
            ? (tool.externalMcp ? `来源：${escapeHtml(tool.externalMcp)}` : '来源：外部MCP')
            : '来源：内置工具';

        return `
            <button type="button" class="mention-item ${activeClass} ${disabledClass}" data-index="${index}">
                <div class="mention-item-name">
                    <span class="mention-item-icon">🔧</span>
                    <span class="mention-item-text">@${nameHtml}</span>
                    ${badge}
                </div>
                ${descHtml}
                <div class="mention-item-meta">
                    <span class="mention-status ${statusClass}">${statusLabel}</span>
                    <span class="mention-origin">${originLabel}</span>
                </div>
            </button>
        `;
    }).join('');

    const listWrapper = document.createElement('div');
    listWrapper.className = 'mention-suggestions-list';
    listWrapper.innerHTML = itemsHtml;

    mentionSuggestionsEl.innerHTML = '';
    mentionSuggestionsEl.appendChild(listWrapper);
    mentionSuggestionsEl.style.display = 'block';
    mentionSuggestionsEl.dataset.lastMentionQuery = currentQuery;

    if (canPreserveScroll) {
        listWrapper.scrollTop = previousScrollTop;
    }

    listWrapper.querySelectorAll('.mention-item').forEach(item => {
        item.addEventListener('mousedown', (event) => {
            event.preventDefault();
            const idx = parseInt(item.dataset.index, 10);
            if (!Number.isNaN(idx)) {
                mentionState.selectedIndex = idx;
            }
            applyMentionSelection();
        });
    });

    scrollMentionSelectionIntoView();
}

function hideMentionSuggestions() {
    if (mentionSuggestionsEl) {
        mentionSuggestionsEl.style.display = 'none';
        mentionSuggestionsEl.innerHTML = '';
        delete mentionSuggestionsEl.dataset.lastMentionQuery;
    }
}

function deactivateMentionState() {
    mentionState.active = false;
    mentionState.startIndex = -1;
    mentionState.query = '';
    mentionState.selectedIndex = 0;
    mentionFilteredTools = [];
    hideMentionSuggestions();
}

function moveMentionSelection(direction) {
    if (!mentionFilteredTools.length) {
        return;
    }
    const max = mentionFilteredTools.length - 1;
    let nextIndex = mentionState.selectedIndex + direction;
    if (nextIndex < 0) {
        nextIndex = max;
    } else if (nextIndex > max) {
        nextIndex = 0;
    }
    mentionState.selectedIndex = nextIndex;
    updateMentionActiveHighlight();
}

function updateMentionActiveHighlight() {
    if (!mentionSuggestionsEl) {
        return;
    }
    const items = mentionSuggestionsEl.querySelectorAll('.mention-item');
    if (!items.length) {
        return;
    }
    items.forEach(item => item.classList.remove('active'));

    let targetIndex = mentionState.selectedIndex;
    if (targetIndex < 0) {
        targetIndex = 0;
    }
    if (targetIndex >= items.length) {
        targetIndex = items.length - 1;
        mentionState.selectedIndex = targetIndex;
    }

    const activeItem = items[targetIndex];
    if (activeItem) {
        activeItem.classList.add('active');
        scrollMentionSelectionIntoView(activeItem);
    }
}

function scrollMentionSelectionIntoView(targetItem = null) {
    if (!mentionSuggestionsEl) {
        return;
    }
    const activeItem = targetItem || mentionSuggestionsEl.querySelector('.mention-item.active');
    if (activeItem && typeof activeItem.scrollIntoView === 'function') {
        activeItem.scrollIntoView({
            block: 'nearest',
            inline: 'nearest',
            behavior: 'auto'
        });
    }
}

function applyMentionSelection() {
    const textarea = document.getElementById('chat-input');
    if (!textarea || mentionState.startIndex === -1 || !mentionFilteredTools.length) {
        deactivateMentionState();
        return;
    }

    const selectedTool = mentionFilteredTools[mentionState.selectedIndex] || mentionFilteredTools[0];
    if (!selectedTool) {
        deactivateMentionState();
        return;
    }

    const caret = textarea.selectionStart || 0;
    const before = textarea.value.slice(0, mentionState.startIndex);
    const after = textarea.value.slice(caret);
    const mentionText = `@${selectedTool.name}`;
    const needsSpace = after.length === 0 || !/^\s/.test(after);
    const insertText = mentionText + (needsSpace ? ' ' : '');

    textarea.value = before + insertText + after;
    const newCaret = before.length + insertText.length;
    textarea.focus();
    textarea.setSelectionRange(newCaret, newCaret);

    // 调整输入框高度并保存草稿
    adjustTextareaHeight(textarea);
    saveChatDraftDebounced(textarea.value);

    deactivateMentionState();
}

function initializeChatUI() {
    const chatInputEl = document.getElementById('chat-input');
    if (chatInputEl) {
        // 初始化时设置正确的高度
        adjustTextareaHeight(chatInputEl);
        // 恢复保存的草稿（仅在输入框为空时恢复，避免覆盖用户输入）
        if (!chatInputEl.value || chatInputEl.value.trim() === '') {
            // 检查对话中是否有最近的消息（30秒内），如果有，说明可能是刚刚发送的消息，不恢复草稿
            const messagesDiv = document.getElementById('chat-messages');
            let shouldRestoreDraft = true;
            if (messagesDiv && messagesDiv.children.length > 0) {
                // 检查最后一条消息的时间
                const lastMessage = messagesDiv.lastElementChild;
                if (lastMessage) {
                    const timeDiv = lastMessage.querySelector('.message-time');
                    if (timeDiv && timeDiv.textContent) {
                        // 如果最后一条消息是用户消息，且时间很近，不恢复草稿
                        const isUserMessage = lastMessage.classList.contains('user');
                        if (isUserMessage) {
                            // 检查消息时间，如果是最近30秒内的，不恢复草稿
                            const now = new Date();
                            const messageTimeText = timeDiv.textContent;
                            // 简单检查：如果消息时间显示的是当前时间（格式：HH:MM），且是用户消息，不恢复草稿
                            // 更精确的方法是检查消息的创建时间，但需要从消息元素中获取
                            // 这里采用简单策略：如果最后一条是用户消息，且输入框为空，可能是刚发送的，不恢复草稿
                            shouldRestoreDraft = false;
                        }
                    }
                }
            }
            if (shouldRestoreDraft) {
                restoreChatDraft();
            } else {
                // 即使不恢复草稿，也要清除localStorage中的草稿，避免下次误恢复
                clearChatDraft();
            }
        }
    }

    const messagesDiv = document.getElementById('chat-messages');
    if (messagesDiv && messagesDiv.childElementCount === 0) {
        renderChatWelcomeEmptyState();
    }

    addAttackChainButton(currentConversationId);
    loadActiveTasks(true);
    if (activeTaskInterval) {
        clearInterval(activeTaskInterval);
    }
    activeTaskInterval = setInterval(() => loadActiveTasks(), ACTIVE_TASK_REFRESH_INTERVAL);
    setupMentionSupport();
    ensureChatInputContainerId();
    setupChatFileUpload();
}