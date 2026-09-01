// 系统提示词管理（prompts/ 目录 YAML 增删改查 + 激活）
// 后端：GET/POST/PUT/DELETE /api/system-prompts[/:filename]、
// POST /api/system-prompts/:filename/activate、GET /api/system-prompts/current。
// 风格对齐 playbooks.js：IIFE + pbT 回退文案。
(function () {
    'use strict';

    let systemPromptsList = [];
    let systemPromptsLoadedOnce = false;
    let editingFilename = '';

    function spT(key, fallback) {
        if (typeof window.t === 'function') {
            try {
                var translated = window.t(key);
                if (typeof translated === 'string' && translated && translated !== key) {
                    return translated;
                }
            } catch (e) { /* ignore */ }
        }
        return fallback || key;
    }

    function escapeHtml(text) {
        if (typeof window.escapeHtml === 'function') {
            return window.escapeHtml(text);
        }
        var div = document.createElement('div');
        div.textContent = text == null ? '' : String(text);
        return div.innerHTML;
    }

    function showNotification(message, type) {
        if (typeof window.showNotification === 'function') {
            window.showNotification(message, type);
        }
    }

    function isBuiltin(entry) {
        return entry && (entry.is_builtin || entry.filename === '__builtin__');
    }

    function renderEntryCard(entry) {
        if (!entry) return '';
        var filename = entry.filename || '';
        var name = entry.name || filename;
        var desc = entry.description || '';
        var active = !!entry.is_active;
        var builtin = isBuiltin(entry);

        var badgeCls = active ? 'ai-channel-badge default' : 'ai-channel-badge';
        var activeBadge = active
            ? '<span class="' + badgeCls + '">' + escapeHtml(spT('systemPrompts.activeBadge', '当前生效')) + '</span>'
            : '';
        var builtinBadge = builtin
            ? '<span class="ai-channel-badge">' + escapeHtml(spT('systemPrompts.builtinBadge', '内置')) + '</span>'
            : '';
        var actions = builtin
            ? (active
                ? ''
                : '<button type="button" class="btn-secondary" onclick="activateSystemPrompt(\'' + escapeHtml(filename) + '\')" data-require-permission="config:write">' + escapeHtml(spT('systemPrompts.activate', '激活')) + '</button>')
            : [
                '<button type="button" class="btn-secondary" onclick="editSystemPrompt(\'' + escapeHtml(filename) + '\')" data-require-permission="config:write">' + escapeHtml(spT('common.edit', '编辑')) + '</button>',
                '<button type="button" class="btn-secondary" onclick="activateSystemPrompt(\'' + escapeHtml(filename) + '\')" data-require-permission="config:write">' + escapeHtml(spT('systemPrompts.activate', '激活')) + '</button>',
                '<button type="button" class="btn-secondary danger" onclick="deleteSystemPrompt(\'' + escapeHtml(filename) + '\')" data-require-permission="config:write">' + escapeHtml(spT('common.delete', '删除')) + '</button>'
            ].join(' ');

        return [
            '<div class="system-prompt-card" data-filename="' + escapeHtml(filename) + '">',
            '<div class="system-prompt-card-head">',
            '<div>',
            '<strong>' + escapeHtml(name) + '</strong>',
            activeBadge,
            builtinBadge,
            '<span class="system-prompt-card-filename">' + escapeHtml(filename) + '</span>',
            '</div>',
            '<div class="system-prompt-card-actions">' + actions + '</div>',
            '</div>',
            '<div class="system-prompt-card-desc">' + (desc ? escapeHtml(desc) : '<span class="system-prompt-card-empty">' + escapeHtml(spT('systemPrompts.noDescription', '暂无描述')) + '</span>') + '</div>',
            '</div>'
        ].join('');
    }

    function renderSystemPromptsList() {
        var container = document.getElementById('system-prompts-list');
        if (!container) return;
        if (!systemPromptsList || systemPromptsList.length === 0) {
            container.innerHTML = '<div class="empty-state">' + escapeHtml(spT('systemPrompts.empty', '暂无提示词')) + '</div>';
            return;
        }
        var html = systemPromptsList.map(renderEntryCard).join('');
        container.innerHTML = html;
        if (typeof window.applyRBACToUI === 'function') {
            window.applyRBACToUI(container);
        }
    }

    async function loadSystemPrompts() {
        var container = document.getElementById('system-prompts-list');
        if (container && !systemPromptsLoadedOnce) {
            container.innerHTML = '<div class="loading-spinner">' + escapeHtml(spT('common.loading', '加载中...')) + '</div>';
        }
        try {
            if (typeof apiFetch !== 'function') {
                throw new Error('apiFetch not available');
            }
            var response = await apiFetch('/api/system-prompts');
            if (!response.ok) {
                throw new Error('HTTP ' + response.status);
            }
            var data = await response.json();
            systemPromptsList = (data && data.prompts) ? data.prompts : [];
            systemPromptsLoadedOnce = true;
            renderSystemPromptsList();
        } catch (error) {
            systemPromptsLoadedOnce = true;
            var msg = (error && error.message) ? error.message : String(error);
            showNotification(spT('systemPrompts.loadFailed', '加载系统提示词失败') + ': ' + msg, 'error');
            if (container) {
                container.innerHTML = '<div class="empty-state">' + escapeHtml(spT('systemPrompts.loadFailed', '加载系统提示词失败')) + '</div>';
            }
        }
    }

    async function loadCurrentSystemPrompt() {
        try {
            if (typeof apiFetch !== 'function') return null;
            var response = await apiFetch('/api/system-prompts/current');
            if (!response.ok) return null;
            return await response.json();
        } catch (e) {
            return null;
        }
    }

    function openModal(filename, entry) {
        var modal = document.getElementById('system-prompt-modal');
        if (!modal) return;
        editingFilename = filename || '';
        document.getElementById('system-prompt-filename-current').value = filename || '';
        var filenameInput = document.getElementById('system-prompt-filename');
        var filenameRow = document.getElementById('system-prompt-filename-row');
        if (filename) {
            filenameInput.value = filename;
            filenameInput.disabled = true;
            if (filenameRow) filenameRow.style.display = 'none';
            document.getElementById('system-prompt-modal-title').textContent = spT('systemPrompts.editTitle', '编辑系统提示词');
        } else {
            filenameInput.value = '';
            filenameInput.disabled = false;
            if (filenameRow) filenameRow.style.display = '';
            document.getElementById('system-prompt-modal-title').textContent = spT('systemPrompts.newTitle', '新建系统提示词');
        }
        document.getElementById('system-prompt-name').value = (entry && entry.name) || '';
        document.getElementById('system-prompt-description').value = (entry && entry.description) || '';
        document.getElementById('system-prompt-content').value = (entry && entry.content) || '';
        modal.style.display = 'block';
    }

    async function editSystemPrompt(filename) {
        if (!filename) return;
        try {
            var response = await apiFetch('/api/system-prompts/' + encodeURIComponent(filename));
            if (!response.ok) {
                throw new Error('HTTP ' + response.status);
            }
            var entry = await response.json();
            openModal(filename, entry);
        } catch (error) {
            showNotification(spT('systemPrompts.loadEntryFailed', '读取提示词失败') + ': ' + (error.message || error), 'error');
        }
    }

    function openSystemPromptModal() {
        openModal('', null);
    }

    function closeSystemPromptModal() {
        var modal = document.getElementById('system-prompt-modal');
        if (modal) modal.style.display = 'none';
    }

    async function saveSystemPromptFromModal() {
        var filename = document.getElementById('system-prompt-filename-current').value || document.getElementById('system-prompt-filename').value.trim();
        var name = document.getElementById('system-prompt-name').value.trim();
        var description = document.getElementById('system-prompt-description').value.trim();
        var content = document.getElementById('system-prompt-content').value;
        if (!name) {
            showNotification(spT('systemPrompts.nameRequired', '请填写名称'), 'error');
            return;
        }
        if (!content.trim()) {
            showNotification(spT('systemPrompts.contentRequired', '请填写提示内容'), 'error');
            return;
        }
        var body = { name: name, description: description, content: content };
        var isUpdate = !!editingFilename;
        if (!isUpdate) {
            if (!filename) {
                showNotification(spT('systemPrompts.filenameRequired', '请填写文件名'), 'error');
                return;
            }
            body.filename = filename;
        }
        try {
            var url = isUpdate
                ? '/api/system-prompts/' + encodeURIComponent(editingFilename)
                : '/api/system-prompts';
            var method = isUpdate ? 'PUT' : 'POST';
            var response = await apiFetch(url, {
                method: method,
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(body)
            });
            if (!response.ok) {
                var err = await response.json().catch(function () { return {}; });
                throw new Error(err.error || ('HTTP ' + response.status));
            }
            showNotification(spT('systemPrompts.saved', '已保存'), 'success');
            closeSystemPromptModal();
            await loadSystemPrompts();
        } catch (error) {
            showNotification(spT('systemPrompts.saveFailed', '保存失败') + ': ' + (error.message || error), 'error');
        }
    }

    async function activateSystemPrompt(filename) {
        if (!filename) return;
        try {
            var response = await apiFetch('/api/system-prompts/' + encodeURIComponent(filename) + '/activate', { method: 'POST' });
            if (!response.ok) {
                var err = await response.json().catch(function () { return {}; });
                throw new Error(err.error || ('HTTP ' + response.status));
            }
            showNotification(spT('systemPrompts.activated', '已激活，新对话立即生效'), 'success');
            await loadSystemPrompts();
        } catch (error) {
            showNotification(spT('systemPrompts.activateFailed', '激活失败') + ': ' + (error.message || error), 'error');
        }
    }

    async function deleteSystemPrompt(filename) {
        if (!filename) return;
        var msg = spT('systemPrompts.deleteConfirm', '确定删除该系统提示词？');
        if (!confirm(msg)) return;
        try {
            var response = await apiFetch('/api/system-prompts/' + encodeURIComponent(filename), { method: 'DELETE' });
            if (!response.ok) {
                var err = await response.json().catch(function () { return {}; });
                throw new Error(err.error || ('HTTP ' + response.status));
            }
            showNotification(spT('systemPrompts.deleted', '已删除'), 'success');
            await loadSystemPrompts();
        } catch (error) {
            showNotification(spT('systemPrompts.deleteFailed', '删除失败') + ': ' + (error.message || error), 'error');
        }
    }

    // 暴露到全局（与 playbooks.js / roles.js 风格一致）
    window.loadSystemPrompts = loadSystemPrompts;
    window.openSystemPromptModal = openSystemPromptModal;
    window.closeSystemPromptModal = closeSystemPromptModal;
    window.editSystemPrompt = editSystemPrompt;
    window.saveSystemPromptFromModal = saveSystemPromptFromModal;
    window.activateSystemPrompt = activateSystemPrompt;
    window.deleteSystemPrompt = deleteSystemPrompt;
    window.loadCurrentSystemPrompt = loadCurrentSystemPrompt;

    // 设置页首次进入「基本设置」分区时加载一次（与 loadConfig 并行，避免侵入）
    document.addEventListener('DOMContentLoaded', function () {
        // 设置页是按需加载；这里监听 switchSettingsSection 调用，切换到 basic 时触发
        var orig = window.switchSettingsSection;
        if (typeof orig === 'function') {
            window.switchSettingsSection = function (section) {
                try { orig(section); } catch (e) { /* ignore */ }
                if (section === 'basic') {
                    if (!systemPromptsLoadedOnce) {
                        loadSystemPrompts();
                    }
                    if (typeof window.refreshVersionBadge === 'function') {
                        window.refreshVersionBadge();
                    }
                }
            };
        }
    });
})();
