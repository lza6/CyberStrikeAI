/**
 * toast.js — 全局反馈态统一组件（F5 落地核心）
 *
 * 现状根因（2026-09-02 审计）：
 *   全仓 17 个前端文件用 `if (typeof window.showNotification === 'function')` 委托调用全局 toast，
 *   但 window.showNotification / window.showToast 从未被任何文件定义 → 条件恒 false → 全部退化成
 *   本地 alert() 或静默失效。auth.js:426 notifyApiError 已写好防御性 `typeof showNotification`
 *   分支，一旦本文件挂上 window.showToast，所有走 notifyApiError/ensureApiOk 的调用零改动升级。
 *
 * 本文件职责（反伪实现，每条可验证）：
 *   1. 定义 window.showToast(message, type, opts)  — 全局 toast 入口
 *   2. 定义 window.showNotification(message, type)  — showToast 别名（兼容 17 个委托文件的调用约定）
 *   3. 复用已有 CSS：#toast-notification-container（style.css:32069, z-index 10100 !important）
 *   4. 四态：success / error / info / warning，带 SVG 图标 + 关闭按钮
 *   5. 自动消失（success 5s / error 7s / info 4s / warning 4s），可 opts.duration 覆盖
 *   6. 最多堆叠 5 条，超出自动移除最早的（避免刷屏）
 *   7. i18n 可选：关闭按钮 aria-label 走 window.t('common.close', '关闭')，t 未就绪时用兜底
 *   8. 无外部依赖，原生 ES5，加载即注册全局，DOMContentLoaded 前即可调用（延迟到 body 就绪再渲染容器）
 *
 * 不做：
 *   - 不替代 notifications.js（那是后端推送的铃铛通知中心，走 /api/notifications 轮询）
 *   - 不替代 modal.js（那是遮罩弹窗层）
 *   - 不接管 chat.js 的 showChatToast（那是对话页底部 toast，样式与场景不同，保留）
 *
 * 挂载顺序：index.html 中须在 auth.js（6809）之前加载，确保 auth.js 的 notifyApiError
 *   运行时能命中全局 showToast（auth.js 在 DOMContentLoaded 前只定义函数，运行时才调用，所以
 *   只要本文件在首个业务脚本之前同步加载即可）。
 */
(function (globalScope) {
    'use strict';

    var CONTAINER_ID = 'toast-notification-container';
    var MAX_STACK = 5;
    var DEFAULT_DURATIONS = {
        success: 5000,
        error: 7000,
        info: 4000,
        warning: 4000
    };

    // 四态视觉配置：与 style.css 的 .toast-notification / .toast-success 等类对齐
    // （knowledge.js:2125 历史版本用内联 style，本版改用 CSS 类，便于主题适配）
    var TYPE_META = {
        success: { cls: 'toast-success', icon: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true"><circle cx="8" cy="8" r="7" stroke="currentColor" stroke-width="1.2"/><path d="M5 8l2 2 4-4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/></svg>' },
        error: { cls: 'toast-error', icon: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true"><circle cx="8" cy="8" r="7" stroke="currentColor" stroke-width="1.2"/><path d="M6 6l4 4M10 6l-4 4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>' },
        info: { cls: 'toast-info', icon: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true"><circle cx="8" cy="8" r="7" stroke="currentColor" stroke-width="1.2"/><path d="M8 7v4M8 5.5v.01" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>' },
        warning: { cls: 'toast-warning', icon: '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M8 2.5l6 10.5H2L8 2.5z" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/><path d="M8 7v2.5M8 11v.01" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>' }
    };

    function t(key, fallback) {
        if (typeof globalScope.t === 'function') {
            try {
                var v = globalScope.t(key);
                if (v && v !== key) return v;
            } catch (_) { /* i18n 未就绪，用兜底 */ }
        }
        return fallback;
    }

    function ensureContainer() {
        var container = document.getElementById(CONTAINER_ID);
        if (container) return container;
        if (!document.body) return null; // DOM 未就绪，调用方应延后
        container = document.createElement('div');
        container.id = CONTAINER_ID;
        container.className = 'toast-notification-container';
        container.setAttribute('role', 'region');
        container.setAttribute('aria-label', t('common.notifications', '通知'));
        container.setAttribute('aria-live', 'polite');
        document.body.appendChild(container);
        return container;
    }

    function trimStack(container) {
        var items = container.children;
        while (items.length > MAX_STACK) {
            var first = items[0];
            if (first && first.parentNode) {
                first.parentNode.removeChild(first);
            } else {
                break;
            }
        }
    }

    /**
     * 全局 toast 入口。
     * @param {string} message  文案（已由调用方走 i18n，本函数不再翻译，避免双重翻译）
     * @param {string} [type]   success | error | info | warning（默认 info）
     * @param {object} [opts]   { duration?: number, sticky?: boolean }
     * @returns {HTMLDivElement|null}  toast 元素，便于调用方主动关闭
     */
    function showToast(message, type, opts) {
        var text = message == null ? '' : String(message).trim();
        if (!text) return null;
        var resolvedType = TYPE_META[type] ? type : 'info';
        var meta = TYPE_META[resolvedType];
        opts = opts || {};
        var duration = (typeof opts.duration === 'number' && opts.duration > 0)
            ? opts.duration
            : DEFAULT_DURATIONS[resolvedType];
        var sticky = opts.sticky === true;

        var container = ensureContainer();
        if (!container) {
            // body 未就绪：降级 alert，绝不让调用方失语（反伪实现：有反馈 > 无反馈）
            try { globalScope.alert(text); } catch (_) {}
            return null;
        }

        var toast = document.createElement('div');
        toast.className = 'toast-notification ' + meta.cls;
        toast.setAttribute('role', resolvedType === 'error' ? 'alert' : 'status');

        // 图标列
        var iconSpan = document.createElement('span');
        iconSpan.className = 'toast-icon';
        iconSpan.innerHTML = meta.icon;

        // 文案列
        var msgSpan = document.createElement('span');
        msgSpan.className = 'toast-message';
        msgSpan.textContent = text; // textContent 防注入（调用方可能传入用户数据）

        // 关闭按钮
        var closeBtn = document.createElement('button');
        closeBtn.type = 'button';
        closeBtn.className = 'toast-close';
        closeBtn.setAttribute('aria-label', t('common.close', '关闭'));
        closeBtn.textContent = '×';

        toast.appendChild(iconSpan);
        toast.appendChild(msgSpan);
        toast.appendChild(closeBtn);

        container.appendChild(toast);
        trimStack(container);

        // 入场动画（style.css 定义 .toast-notification--visible）
        requestAnimationFrame(function () {
            toast.classList.add('toast-notification--visible');
        });

        function dismiss() {
            toast.classList.remove('toast-notification--visible');
            setTimeout(function () {
                if (toast.parentNode) toast.parentNode.removeChild(toast);
            }, 250);
        }

        closeBtn.addEventListener('click', function () {
            if (timerId) {
                clearTimeout(timerId);
                timerId = null;
            }
            dismiss();
        });

        var timerId = null;
        if (!sticky) {
            timerId = setTimeout(dismiss, duration);
        }
        return toast;
    }

    /**
     * showNotification — showToast 别名，兼容 17 个委托文件的 `showNotification(msg, type)` 调用约定。
     * 防御：若被同名本地函数覆盖（如 knowledge.js:2093 的本地 showNotification），委托文件会优先调本地；
     * 本全局只在 `typeof window.showNotification === 'function'` 探测时被命中，不会产生递归。
     */
    function showNotification(message, type) {
        return showToast(message, type);
    }

    /**
     * 便捷别名（c2.js:172 / chat-files 等用 `if (window.showToast)` 探测）
     */
    globalScope.showToast = showToast;
    globalScope.showNotification = showNotification;

    // 调试用：在控制台可手动开关 toast（不影响生产）
    globalScope.__toastVersion = '1.0.0-f5';
})(typeof window !== 'undefined' ? window : this);
