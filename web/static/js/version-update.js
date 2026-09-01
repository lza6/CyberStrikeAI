// 版本更新检测（GET /api/update/check）前端逻辑
// 设置页「版本与更新」区块：当前版本徽章 + 检查更新按钮 + 更新提示。
(function () {
    'use strict';

    function vuT(key, fallback) {
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

    function escapeJsString(text) {
        return JSON.stringify(String(text == null ? '' : text));
    }

    async function checkForUpdate() {
        const resultEl = document.getElementById('version-update-result');
        const btn = document.querySelector('#version-update-section .btn-secondary');
        if (btn) {
            btn.style.pointerEvents = 'none';
            btn.style.opacity = '0.5';
        }
        if (resultEl) {
            resultEl.innerHTML = '<span class="form-inline-result">' + escapeHtml(vuT('versionUpdate.checking', '正在检查更新...')) + '</span>';
        }
        try {
            if (typeof apiFetch !== 'function') {
                throw new Error('apiFetch not available');
            }
            const response = await apiFetch('/api/update/check');
            const data = await response.json().catch(() => ({ error: String(response.status) }));
            if (!response.ok) {
                throw new Error(data.error || ('HTTP ' + response.status));
            }
            renderUpdateResult(data);
        } catch (error) {
            if (resultEl) {
                resultEl.innerHTML = '<span class="form-inline-result" style="color: var(--error-color, #e53e3e);">' + escapeHtml(vuT('versionUpdate.checkFailed', '检查更新失败')) + ': ' + escapeHtml(error.message || String(error)) + '</span>';
            }
            showNotification(vuT('versionUpdate.checkFailed', '检查更新失败') + ': ' + (error.message || error), 'error');
        } finally {
            if (btn) {
                btn.style.pointerEvents = '';
                btn.style.opacity = '';
            }
        }
    }

    function renderUpdateResult(data) {
        const resultEl = document.getElementById('version-update-result');
        if (!resultEl || !data) return;
        if (data.has_update) {
            const url = data.release_url || '';
            // 只允许 http/https 链接，防止注入 javascript: 等危险 scheme。
            const safeUrl = /^https?:\/\//i.test(url) ? url : '';
            const notes = data.release_notes || '';
            const link = safeUrl
                ? '<a href="' + escapeHtml(safeUrl) + '" target="_blank" rel="noopener noreferrer" class="version-update-link">' + escapeHtml(vuT('versionUpdate.viewRelease', '查看发布说明')) + '</a>'
                : '';
            const notesBlock = notes
                ? '<details class="version-update-notes"><summary>' + escapeHtml(vuT('versionUpdate.releaseNotes', '发布说明')) + '</summary><pre>' + escapeHtml(notes) + '</pre></details>'
                : '';
            resultEl.innerHTML = [
                '<div class="version-update-has-update">',
                '<strong>' + escapeHtml(vuT('versionUpdate.hasUpdate', '发现新版本')) + ': ' + escapeHtml(data.latest_version || '-') + '</strong> ',
                '<span class="ai-channel-badge default">' + escapeHtml(vuT('versionUpdate.currentLabel', '当前')) + ': ' + escapeHtml(data.current_version || '-') + '</span>',
                link,
                notesBlock,
                '</div>'
            ].join('');
        } else {
            resultEl.innerHTML = '<span class="form-inline-result" style="color: var(--success-color, #38a169);">' + escapeHtml(vuT('versionUpdate.upToDate', '已是最新版本')) + ' (' + escapeHtml(data.current_version || '-') + ')</span>';
        }
    }

    window.checkForUpdate = checkForUpdate;
})();
