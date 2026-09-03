/**
 * nav-delegate.js — 导航/子菜单事件委托（F4 渐进迁移第一步）
 *
 * 目的：把 index.html 中模式统一的导航类 onclick 迁移到 data-action + 事件委托，
 *       降低 inline onclick 依赖，为后续 CSP nonce 收紧做准备。
 *
 * 迁移范围（本轮，高价值低风险）：
 *   - switchPage('xxx')           ← 24 处（header logo + nav-item-content + nav-submenu-item）
 *   - window.toggleSubmenu('xxx') ← 7 处（nav-item-has-submenu 的 content）
 *
 * 迁移方式：
 *   onclick="switchPage('dashboard')"            → data-action="switchPage" data-page="dashboard"
 *   onclick="window.toggleSubmenu('assets')"     → data-action="toggleSubmenu" data-page="assets"
 *
 * 委托契约：
 *   document 上注册单一 click 监听，命中 [data-action] 节点后读取 data-page 调用对应全局函数。
 *   未声明 data-action 的节点保持原 inline onclick 不变（渐进，不破坏）。
 *
 * 回退与鲁棒：
 *   - 委托 handler 内若对应全局函数未定义，静默 return（不抛，不阻断其他 handler）。
 *   - data-page 为空时不触发。
 *   - 事件委托发生在 DOMContentLoaded 后，避免节点未就绪。
 *
 * 不在本轮迁移：545 - 31 = 514 处其他 onclick（含动态/复杂多语句调用）保留 inline，
 *   等后续批次逐页迁；CSP 本轮保持 'unsafe-inline' 不动，确保未迁的 onclick 不失效。
 */
(function (globalScope) {
    'use strict';

    function handleNavDelegatedClick(event) {
        var target = event.target;
        if (!target || target.nodeType !== 1) { return; }

        // 沿父链向上寻找声明了 data-action 的节点（最多向上 3 层，覆盖 svg/span 包裹）
        var node = target;
        var depth = 0;
        while (node && depth < 4) {
            var action = node.getAttribute && node.getAttribute('data-action');
            if (action) {
                var page = node.getAttribute('data-page') || '';
                if (!page) { return; }
                // 阻止重复触发：事件委托只在最内层 action 节点处理一次
                event.stopPropagation();
                switch (action) {
                    case 'switchPage':
                        if (typeof globalScope.switchPage === 'function') {
                            globalScope.switchPage(page);
                        }
                        break;
                    case 'toggleSubmenu':
                        if (typeof globalScope.toggleSubmenu === 'function') {
                            globalScope.toggleSubmenu(page);
                        }
                        break;
                    default:
                        // 未知 action 不处理（保留原 inline 兜底）
                        break;
                }
                return;
            }
            node = node.parentNode;
            depth++;
        }
    }

    function bind() {
        if (globalScope.__navDelegateBound) { return; }
        document.addEventListener('click', handleNavDelegatedClick);
        globalScope.__navDelegateBound = true;
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', bind);
    } else {
        bind();
    }
})(typeof window !== 'undefined' ? window : this);
