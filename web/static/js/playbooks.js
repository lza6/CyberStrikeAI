// 攻击剧本（playbooks）页面相关功能
// 从 /api/playbooks 拉取剧本列表，渲染为可折叠的卡片网格。
// 每个卡片显示名称、描述、阶段数；点击展开显示各阶段的工具与步骤说明。
(function () {
    'use strict';

    let playbooksList = [];
    let playbooksLoadedOnce = false;

    /**
     * i18n 包装：i18n 未就绪时回退到默认中文文案，避免把 key 暴露给用户。
     * 与 roles.js 的 _t 风格一致。
     */
    function pbT(key, opts) {
        if (typeof window.t === 'function') {
            try {
                var translated = window.t(key, opts);
                if (typeof translated === 'string' && translated && translated !== key) {
                    return translated;
                }
            } catch (e) { /* ignore */ }
        }
        var fallback = {
            'playbooks.title': '攻击剧本',
            'playbooks.loadFailed': '加载剧本失败',
            'playbooks.empty': '暂无剧本',
            'playbooks.phaseCount': '阶段',
            'playbooks.toolsLabel': '工具',
            'playbooks.stepsLabel': '步骤',
            'playbooks.noTools': '无',
            'playbooks.noDescription': '暂无描述',
            'playbooks.expand': '展开',
            'playbooks.collapse': '收起'
        };
        return fallback[key] || key;
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

    /**
     * 渲染单个阶段（展开后内容）。
     * @param {Object} phase - { name, description, steps: [], tools: [], post_analysis }
     * @returns {string} HTML
     */
    function renderPhase(phase) {
        if (!phase) return '';
        var parts = [];
        parts.push('<div class="playbook-phase">');

        // 阶段标题
        var phaseName = phase.name ? escapeHtml(phase.name) : pbT('playbooks.noDescription');
        parts.push('<div class="playbook-phase-header"><span class="playbook-phase-name">' + phaseName + '</span></div>');

        // 阶段描述（description 或 post_analysis）
        var desc = '';
        if (phase.description && String(phase.description).trim()) {
            desc = String(phase.description).trim();
        } else if (phase.post_analysis && String(phase.post_analysis).trim()) {
            desc = String(phase.post_analysis).trim();
        }
        if (desc) {
            parts.push('<div class="playbook-phase-desc">' + escapeHtml(desc) + '</div>');
        }

        // 工具列表
        var tools = Array.isArray(phase.tools) ? phase.tools : [];
        var toolsHtml = tools.length > 0
            ? tools.map(function (t) { return '<span class="playbook-tool-chip">' + escapeHtml(t) + '</span>'; }).join('')
            : '<span class="playbook-no-tools">' + escapeHtml(pbT('playbooks.noTools')) + '</span>';
        parts.push('<div class="playbook-phase-tools"><span class="playbook-phase-label">' + escapeHtml(pbT('playbooks.toolsLabel')) + ':</span> ' + toolsHtml + '</div>');

        // 步骤列表（可选字段）
        var steps = Array.isArray(phase.steps) ? phase.steps : [];
        if (steps.length > 0) {
            var stepsHtml = steps.map(function (s) {
                return '<li>' + escapeHtml(s) + '</li>';
            }).join('');
            parts.push('<div class="playbook-phase-steps"><span class="playbook-phase-label">' + escapeHtml(pbT('playbooks.stepsLabel')) + ':</span><ol>' + stepsHtml + '</ol></div>');
        }

        parts.push('</div>');
        return parts.join('');
    }

    /**
     * 渲染单个剧本卡片。
     */
    function renderPlaybookCard(pb) {
        if (!pb) return '';
        var name = pb.name ? escapeHtml(pb.name) : '';
        var displayName = pb.display_name ? escapeHtml(pb.display_name) : name;
        var description = pb.description ? escapeHtml(pb.description) : '<span class="playbook-no-description">' + escapeHtml(pbT('playbooks.noDescription')) + '</span>';
        var phases = Array.isArray(pb.phases) ? pb.phases : [];
        var phaseCount = phases.length;

        var phasesHtml = phases.map(renderPhase).join('');
        var phaseCountLabel = phaseCount + ' ' + escapeHtml(pbT('playbooks.phaseCount'));

        return [
            '<div class="playbook-card" data-playbook-name="' + name + '">',
                '<div class="playbook-card-header" onclick="window.togglePlaybookCard(\'' + name.replace(/'/g, "\\'") + '\')">',
                    '<div class="playbook-card-title">',
                        '<span class="playbook-card-name">' + displayName + '</span>',
                        '<span class="playbook-card-filename">' + name + '</span>',
                    '</div>',
                    '<div class="playbook-card-meta">',
                        '<span class="playbook-phase-count">' + phaseCountLabel + '</span>',
                        '<svg class="playbook-card-arrow" width="16" height="16" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M9 18l6-6-6-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>',
                    '</div>',
                '</div>',
                '<div class="playbook-card-desc">' + description + '</div>',
                '<div class="playbook-card-body" style="display: none;">' + phasesHtml + '</div>',
            '</div>'
        ].join('');
    }

    /**
     * 渲染剧本列表到 #playbooks-list 容器。
     */
    function renderPlaybooksList() {
        var container = document.getElementById('playbooks-list');
        if (!container) return;

        if (!playbooksList || playbooksList.length === 0) {
            container.innerHTML = '<div class="empty-state">' + escapeHtml(pbT('playbooks.empty')) + '</div>';
            return;
        }

        var html = playbooksList.map(renderPlaybookCard).join('');
        container.innerHTML = html;
    }

    /**
     * 切换剧本卡片展开/收起状态（暴露到 window 供 onclick 调用）。
     */
    window.togglePlaybookCard = function (name) {
        if (!name) return;
        var cards = document.querySelectorAll('.playbook-card[data-playbook-name="' + CSS.escape(name) + '"]');
        for (var i = 0; i < cards.length; i++) {
            var body = cards[i].querySelector('.playbook-card-body');
            var arrow = cards[i].querySelector('.playbook-card-arrow');
            if (!body) continue;
            var isOpen = body.style.display !== 'none';
            body.style.display = isOpen ? 'none' : 'block';
            if (arrow) {
                arrow.style.transform = isOpen ? '' : 'rotate(90deg)';
            }
        }
    };

    /**
     * 从后端加载剧本列表。GET /api/playbooks
     */
    async function loadPlaybooks() {
        var container = document.getElementById('playbooks-list');
        if (container && !playbooksLoadedOnce) {
            container.innerHTML = '<div class="loading-spinner">' + escapeHtml(pbT('playbooks.title')) + '</div>';
        }
        try {
            if (typeof apiFetch !== 'function') {
                throw new Error('apiFetch not available');
            }
            var response = await apiFetch('/api/playbooks');
            if (!response.ok) {
                throw new Error('HTTP ' + response.status);
            }
            var data = await response.json();
            playbooksList = (data && data.playbooks) ? data.playbooks : [];
            playbooksLoadedOnce = true;
            renderPlaybooksList();
        } catch (error) {
            playbooksLoadedOnce = true;
            var msg = (error && error.message) ? error.message : String(error);
            showNotification(pbT('playbooks.loadFailed') + ': ' + msg, 'error');
            if (container) {
                container.innerHTML = '<div class="empty-state">' + escapeHtml(pbT('playbooks.loadFailed')) + '</div>';
            }
        }
    }

    /**
     * 页面切换时由 router.js 调用：首次进入自动加载。
     */
    function initPlaybooksPage() {
        if (!playbooksLoadedOnce) {
            loadPlaybooks();
        }
    }

    // 暴露到全局（与 roles.js / skills.js 风格一致：顶层函数声明 + 全局引用）
    window.loadPlaybooks = loadPlaybooks;
    window.initPlaybooksPage = initPlaybooksPage;
})();
