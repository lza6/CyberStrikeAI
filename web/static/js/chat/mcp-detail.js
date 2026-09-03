
function normalizeToolExecutionSummary(raw) {
    if (typeof raw === 'string') {
        return { toolName: raw, status: '' };
    }
    if (raw && typeof raw === 'object') {
        return {
            toolName: raw.toolName || raw.name || '',
            status: raw.status || ''
        };
    }
    return { toolName: '', status: '' };
}

function getToolExecutionStatusLabel(status) {
    const normalized = String(status || '').toLowerCase();
    if (typeof window.t === 'function') {
        const keyMap = {
            completed: 'mcpMonitor.statusSuccess',
            failed: 'mcpMonitor.statusFailed',
            running: 'mcpMonitor.statusRunning',
            cancelled: 'mcpMonitor.statusCancelled',
            pending: 'mcpMonitor.statusPending',
            result_missing: 'timeline.resultMissing'
        };
        const key = keyMap[normalized];
        if (key) {
            const translated = window.t(key);
            if (translated && translated !== key) return translated;
        }
    }
    const fallback = {
        completed: '成功',
        failed: '失败',
        running: '运行中',
        cancelled: '已取消',
        pending: '等待中',
        result_missing: '结果记录缺失'
    };
    return fallback[normalized] || '';
}

function renderToolExecutionButtonContent(btn, displayToolName, index, status) {
    const safeToolName = escapeHtml(displayToolName || (typeof window.t === 'function' ? window.t('chat.unknownTool') : '未知工具'));
    const safeIndex = escapeHtml(index || '');
    const statusText = getToolExecutionStatusLabel(status);
    const normalizedStatus = String(status || '').toLowerCase();
    const label = safeIndex ? `${safeToolName} #${safeIndex}` : safeToolName;
    btn.innerHTML = '<span class="mcp-tool-name">' + label + '</span>';
    if (!statusText) {
        btn.removeAttribute('data-status');
        btn.removeAttribute('title');
        return;
    }
    btn.dataset.status = normalizedStatus;
    btn.title = statusText;
}

// 批量获取工具摘要并更新按钮（消除 N 次单独 API 请求，合并为 1 次）
async function batchUpdateButtonToolNames(buttonsContainer, executionIds, renderVersion) {
    if (!executionIds || executionIds.length === 0) return;
    try {
        const response = await apiFetch('/api/monitor/executions/names', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ ids: executionIds }),
        });
        if (!response.ok) return;
        const nameMap = await response.json(); // { execId: toolName } 或 { execId: { toolName, status } }
        // 等待请求期间如果摘要触发了新一轮渲染，旧响应不得覆盖新状态。
        if (renderVersion && buttonsContainer.dataset.renderVersion !== renderVersion) return;
        // 更新对应按钮的文本
        const buttons = buttonsContainer.querySelectorAll('.mcp-detail-btn[data-exec-id]');
        buttons.forEach(btn => {
            const execId = btn.dataset.execId;
            const index = btn.dataset.execIndex;
            const summary = normalizeToolExecutionSummary(nameMap[execId]);
            const toolName = summary.toolName;
            if (toolName) {
                const displayToolName = toolName.includes('::') ? toolName.split('::')[1] : toolName;
                renderToolExecutionButtonContent(btn, displayToolName, index, summary.status);
            }
        });
    } catch (error) {
        logger.error('批量获取工具名称失败:', error);
    }
}

function extractMCPResultText(result) {
    if (!result) return '';
    const content = result.content;
    if (typeof content === 'string') return content;
    if (Array.isArray(content)) {
        return content
            .map(item => (item && typeof item === 'object' && typeof item.text === 'string') ? item.text : '')
            .filter(Boolean)
            .join('\n\n');
    }
    if (content && typeof content === 'object' && typeof content.text === 'string') {
        return content.text;
    }
    return '';
}

function formatMCPDetailText(text) {
    if (text == null) return '';
    return String(text);
}

function formatMCPResultJsonForDisplay(result) {
    if (!result) return '{}';
    const payload = {
        content: result.content,
        isError: !!result.isError
    };
    return JSON.stringify(payload, null, 2);
}

function switchMCPResultDetailTab(tabName) {
    const normalized = tabName === 'raw' ? 'raw' : 'success';
    const tabs = {
        success: document.getElementById('detail-result-tab-success'),
        raw: document.getElementById('detail-result-tab-raw')
    };
    const panels = {
        success: document.getElementById('detail-result-panel-success'),
        raw: document.getElementById('detail-result-panel-raw')
    };
    Object.keys(tabs).forEach(function (key) {
        const isActive = key === normalized;
        if (tabs[key]) {
            tabs[key].classList.toggle('active', isActive);
            tabs[key].setAttribute('aria-selected', isActive ? 'true' : 'false');
        }
        if (panels[key]) {
            panels[key].classList.toggle('active', isActive);
            panels[key].hidden = !isActive;
        }
    });
}

function setMCPResultDetailTabs(defaultTab, hasSuccessContent) {
    const successTab = document.getElementById('detail-result-tab-success');
    if (successTab) {
        successTab.disabled = !hasSuccessContent;
        successTab.classList.toggle('disabled', !hasSuccessContent);
    }
    switchMCPResultDetailTab(hasSuccessContent && defaultTab !== 'raw' ? 'success' : 'raw');
}

function copyActiveMCPResultDetail(triggerBtn = null) {
    const activePanel = document.querySelector('#mcp-detail-modal .detail-result-panel.active');
    const activeBlock = activePanel ? activePanel.querySelector('.code-block') : null;
    copyDetailBlock(activeBlock ? activeBlock.id : 'detail-response', triggerBtn);
}

function renderMCPDetailModal(exec) {
    exec = exec || {};
    document.getElementById('detail-tool-name').textContent = exec.toolName || (typeof window.t === 'function' ? window.t('mcpDetailModal.unknown') : 'Unknown');
    document.getElementById('detail-execution-id').textContent = exec.id || 'N/A';
    const statusEl = document.getElementById('detail-status');
    const normalizedStatus = (exec.status || 'unknown').toLowerCase();
    statusEl.textContent = getStatusText(exec.status);
    const statusClass = normalizedStatus === 'background_running' ? 'running' : normalizedStatus;
    statusEl.className = `status-chip status-${statusClass}`;
    try {
        statusEl.dataset.detailStatus = (exec.status || '') + '';
    } catch (e) { /* ignore */ }
    const detailTimeLocale = (typeof window.__locale === 'string' && window.__locale.startsWith('zh')) ? 'zh-CN' : 'en-US';
    const detailTimeEl = document.getElementById('detail-time');
    if (detailTimeEl) {
        detailTimeEl.textContent = exec.startTime
            ? new Date(exec.startTime).toLocaleString(detailTimeLocale)
            : '—';
        try {
            detailTimeEl.dataset.detailTimeIso = exec.startTime ? new Date(exec.startTime).toISOString() : '';
        } catch (e) { /* ignore */ }
    }

    const requestData = {
        tool: exec.toolName,
        arguments: exec.arguments
    };
    document.getElementById('detail-request').textContent = JSON.stringify(requestData, null, 2);

    const responseElement = document.getElementById('detail-response');
    const successElement = document.getElementById('detail-success');
    const errorSection = document.getElementById('detail-error-section');
    const errorElement = document.getElementById('detail-error');

    responseElement.className = 'code-block';
    responseElement.textContent = '';
    if (successElement) {
        successElement.className = 'code-block';
        successElement.textContent = '';
    }
    if (errorSection && errorElement) {
        errorSection.style.display = 'none';
        errorElement.textContent = '';
    }
    setMCPResultDetailTabs('raw', false);

    if (exec.result) {
        const agentVisibleText = formatMCPDetailText(extractMCPResultText(exec.result));
        const emptyText = typeof window.t === 'function' ? window.t('mcpDetailModal.execSuccessNoContent') : '执行成功，未返回可展示的文本内容。';

        if (exec.result.isError) {
            responseElement.className = 'code-block error';
            responseElement.textContent = formatMCPResultJsonForDisplay(exec.result);
            if (successElement) {
                successElement.textContent = '';
            }
            setMCPResultDetailTabs('raw', false);
            if (exec.error && errorSection && errorElement) {
                errorSection.style.display = 'block';
                errorElement.textContent = exec.error;
            }
        } else {
            responseElement.className = 'code-block';
            responseElement.textContent = formatMCPResultJsonForDisplay(exec.result);
            if (successElement) {
                successElement.textContent = agentVisibleText || emptyText;
            }
            setMCPResultDetailTabs('success', true);
        }
    } else {
        if (normalizedStatus === 'running' || normalizedStatus === 'background_running') {
            responseElement.textContent = typeof window.t === 'function' ? window.t('mcpDetailModal.runningNoResponseYet') : '尚无返回，工具可能仍在执行。若长时间无响应，可在下方终止本次调用。';
        } else {
            responseElement.textContent = typeof window.t === 'function' ? window.t('chat.noResponseData') : '暂无响应数据';
        }
        setMCPResultDetailTabs('raw', false);
    }

    const abortSection = document.getElementById('detail-abort-section');
    const abortBtn = document.getElementById('detail-abort-btn');
    if (abortSection && abortBtn) {
        if ((normalizedStatus === 'running' || normalizedStatus === 'background_running') && exec.id) {
            abortSection.style.display = 'block';
            abortBtn.dataset.execId = exec.id || '';
            abortBtn.textContent = typeof window.t === 'function' ? window.t('mcpDetailModal.abortBtn') : '终止工具';
        } else {
            abortSection.style.display = 'none';
            delete abortBtn.dataset.execId;
        }
    }
}

async function showMCPDetail(executionId) {
    try {
        openAppModal('mcp-detail-modal', { focus: false });
        const response = await apiFetch(`/api/monitor/execution/${executionId}`);
        const exec = await response.json();

        if (!response.ok) {
            closeMCPDetail();
            alert((typeof window.t === 'function' ? window.t('mcpDetailModal.getDetailFailed') : '获取详情失败') + ': ' + (exec.error || (typeof window.t === 'function' ? window.t('mcpDetailModal.unknown') : '未知错误')));
            return;
        }

        deferModalContent(function () {
            renderMCPDetailModal(exec);
        });
    } catch (error) {
        closeMCPDetail();
        alert((typeof window.t === 'function' ? window.t('mcpDetailModal.getDetailFailed') : '获取详情失败') + ': ' + error.message);
    }
}

// 关闭MCP详情模态框
function closeMCPDetail() {
    closeAppModal('mcp-detail-modal');
}

/** 从详情模态框触发：取消当前进行中的 MCP 工具调用 */
async function abortMCPToolExecutionFromDetail() {
    const btn = document.getElementById('detail-abort-btn');
    const id = btn && btn.dataset.execId;
    if (!id) {
        return;
    }
    await cancelMCPToolExecution(id, { refreshDetail: true });
}

/**
 * 打开 MCP 工具终止弹窗（说明会经服务端加上「用户终止说明」标题块后与工具输出合并给模型）
 * @param {string} executionId
 * @param {{ refreshDetail?: boolean }} [options]
 */
function openMcpToolAbortModal(executionId, options = {}) {
    window.__mcpToolAbortContext = { executionId: executionId, options: options || {} };
    const ta = document.getElementById('mcp-tool-abort-note');
    if (ta) {
        ta.value = '';
    }
    openAppModal('mcp-tool-abort-modal');
}

function closeMcpToolAbortModal() {
    window.__mcpToolAbortContext = null;
    closeAppModal('mcp-tool-abort-modal');
}

async function submitMcpToolAbortModal() {
    const ctx = window.__mcpToolAbortContext;
    if (!ctx || !ctx.executionId) {
        closeMcpToolAbortModal();
        return;
    }
    const note = (document.getElementById('mcp-tool-abort-note') && document.getElementById('mcp-tool-abort-note').value || '').trim();
    const executionId = ctx.executionId;
    const options = ctx.options || {};
    closeMcpToolAbortModal();
    await cancelMCPToolExecutionSubmit(executionId, note, options);
}

/**
 * 提交终止请求（body: { note }）
 * @param {string} executionId
 * @param {string} userNote
 * @param {{ refreshDetail?: boolean }} [options]
 */
async function cancelMCPToolExecutionSubmit(executionId, userNote, options = {}) {
    if (!executionId) {
        return;
    }
    let conversationId = '';
    if (typeof monitorState !== 'undefined' && Array.isArray(monitorState.executions)) {
        const exec = monitorState.executions.find(e => e && e.id === executionId);
        if (exec) {
            conversationId = (exec.conversationId || '').trim();
        }
    }
    try {
        if (conversationId && typeof requestCancelWithContinue === 'function') {
            await requestCancelWithContinue(conversationId, userNote || '', { executionId });
        } else {
            const res = await apiFetch(`/api/monitor/execution/${encodeURIComponent(executionId)}/cancel`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ note: userNote || '' }),
            });
            const body = await res.json().catch(() => ({}));
            if (!res.ok) {
                throw new Error(body.error || body.message || res.statusText);
            }
        }
        const okMsg = typeof window.t === 'function' ? window.t('mcpDetailModal.abortSuccess') : '已发送终止请求';
        alert(okMsg);
        if (options.refreshDetail && typeof showMCPDetail === 'function') {
            await showMCPDetail(executionId);
        }
        if (typeof refreshMonitorPanel === 'function') {
            const page = (typeof monitorState !== 'undefined' && monitorState.pagination && monitorState.pagination.page) ? monitorState.pagination.page : 1;
            await refreshMonitorPanel(page);
        }
    } catch (e) {
        const failMsg = typeof window.t === 'function' ? window.t('mcpDetailModal.abortFailed') : '终止失败';
        alert(failMsg + ': ' + (e && e.message ? e.message : String(e)));
    }
}

/**
 * 取消单次 MCP 工具执行（监控页「终止」）。有 conversationId 时复用对话页「中断并继续」弹窗与 API。
 * @param {string} executionId
 * @param {{ refreshDetail?: boolean }} [options]
 */
async function cancelMCPToolExecution(executionId, options = {}) {
    if (!executionId) {
        return;
    }
    let conversationId = '';
    if (typeof monitorState !== 'undefined' && Array.isArray(monitorState.executions)) {
        const exec = monitorState.executions.find(e => e && e.id === executionId);
        if (exec) {
            conversationId = (exec.conversationId || '').trim();
        }
    }
    if (conversationId && typeof openUserInterruptModal === 'function') {
        openUserInterruptModal(null, conversationId);
        window.__monitorInterruptContext = { executionId: executionId, options: options || {} };
        return;
    }
    openMcpToolAbortModal(executionId, options);
}

// 复制详情面板中的内容
function copyDetailBlock(elementId, triggerBtn = null) {
    const target = document.getElementById(elementId);
    if (!target) {
        return;
    }
    const text = target.textContent || '';
    if (!text.trim()) {
        return;
    }

    const originalLabel = triggerBtn ? (triggerBtn.dataset.originalLabel || triggerBtn.textContent.trim()) : '';
    if (triggerBtn && !triggerBtn.dataset.originalLabel) {
        triggerBtn.dataset.originalLabel = originalLabel;
    }

    const showCopiedState = () => {
        if (!triggerBtn) {
            return;
        }
        triggerBtn.textContent = '已复制';
        triggerBtn.disabled = true;
        setTimeout(() => {
            triggerBtn.disabled = false;
            triggerBtn.textContent = triggerBtn.dataset.originalLabel || originalLabel || '复制';
        }, 1200);
    };

    const fallbackCopy = (value) => {
        return new Promise((resolve, reject) => {
            const textarea = document.createElement('textarea');
            textarea.value = value;
            textarea.style.position = 'fixed';
            textarea.style.opacity = '0';
            document.body.appendChild(textarea);
            textarea.focus();
            textarea.select();
            try {
                const successful = document.execCommand('copy');
                document.body.removeChild(textarea);
                if (successful) {
                    resolve();
                } else {
                    reject(new Error('execCommand failed'));
                }
            } catch (err) {
                document.body.removeChild(textarea);
                reject(err);
            }
        });
    };

    const copyPromise = (navigator.clipboard && typeof navigator.clipboard.writeText === 'function')
        ? navigator.clipboard.writeText(text)
        : fallbackCopy(text);

    copyPromise
        .then(() => {
            showCopiedState();
        })
        .catch(() => {
            if (triggerBtn) {
                triggerBtn.disabled = false;
                triggerBtn.textContent = triggerBtn.dataset.originalLabel || originalLabel || '复制';
            }
            alert('复制失败，请手动选择文本复制。');
        });
}


// 开始新对话