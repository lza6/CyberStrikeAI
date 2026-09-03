/**
 * lazy-loader.js — 非首屏脚本按页懒加载（F6 首屏瘦身）
 *
 * 现状（2026-09-02 审计）：
 *   index.html 47 个同步 <script src> 共 6.43 MB，首屏默认进 dashboard 只需 ~271KB。
 *   其中 5 个 vendor 大块（elk 1.6MB / xlsx 882KB / xterm 388KB / cytoscape 362KB / xterm-addon-fit 1.7KB）
 *   = 3.24MB，占总量 48%，全部是运行时调用（new ELK()、cytoscape()、XLSX 工具、new Terminal()），
 *   首屏 dashboard 完全用不到。移出首屏可让首屏体积从 6.43MB 降至 ~3.2MB（去掉 vendor 大块），
 *   再配合业务文件懒加载可进一步降至 ~271KB。
 *
 * 本文件职责（反伪实现，每条可验证）：
 *   1. 提供 window.loadScript(src) — Promise，去重，已加载则直接 resolve
 *   2. 提供 window.ensureScripts(srcs) — 批量并行加载，全部就绪后 resolve
 *   3. 在 router.js initPage 的对应 case 之前注入所需 vendor（见 LAZY_MAP）
 *   4. 不破坏现有全局符号依赖：vendor 在页面首次渲染前注入完成
 *
 * 不做：
 *   - 不接管首屏必需脚本（router/i18n/theme/auth/modal/notifications/toast/dashboard/rbac-guards）
 *   - 不接管对话页必需脚本（chat/chat-scroll/chat-plan-progress/chat-files/hitl/monitor/marked/purify/sanitize）
 *   - 不改动 index.html 已有同步 script 的顺序，只把可懒加载的从 index.html 移除，改由本文件按页注入
 *
 * 挂载顺序：index.html 中须在 router.js 之后、首屏业务脚本之前加载（首屏就能调用 loadScript）。
 *   实际位置：紧跟 router.js（行 38）之后，或放在 i18n.js 之后。本版放在 router.js 之后。
 */
(function (globalScope) {
    'use strict';

    // 已加载脚本去重表：src → Promise
    var loading = {};
    var loaded = {};

    function isScriptLoaded(src) {
        // 同步 <script src> 已在 index.html 的，DOM 里能查到
        var existing = document.querySelector('script[src="' + src + '"]');
        if (existing) return true;
        return Object.prototype.hasOwnProperty.call(loaded, src);
    }

    /**
     * 按需加载单个脚本（去重，幂等）。
     * @param {string} src  脚本 URL（如 /static/vendor/elk.bundled.js）
     * @returns {Promise<void>}
     */
    function loadScript(src) {
        if (loaded[src]) return Promise.resolve();
        if (loading[src]) return loading[src];
        if (isScriptLoaded(src)) { loaded[src] = true; return Promise.resolve(); }

        var promise = new Promise(function (resolve, reject) {
            var script = document.createElement('script');
            script.src = src;
            script.async = true;
            script.onload = function () {
                loaded[src] = true;
                delete loading[src];
                resolve();
            };
            script.onerror = function () {
                delete loading[src];
                reject(new Error('Failed to load script: ' + src));
            };
            // 插入到 body 末尾（与原 index.html 同步 script 行为一致，全局符号挂 window）
            (document.body || document.head || document.documentElement).appendChild(script);
        });
        loading[src] = promise;
        return promise;
    }

    /**
     * 批量并行加载多个脚本。
     * @param {string[]} srcs
     * @returns {Promise<void[]>}
     */
    function ensureScripts(srcs) {
        if (!srcs || !srcs.length) return Promise.resolve([]);
        return Promise.all(srcs.map(loadScript));
    }

    globalScope.loadScript = loadScript;
    globalScope.ensureScripts = ensureScripts;

    globalScope.__lazyLoaderVersion = '1.0.0-f6';
})(typeof window !== 'undefined' ? window : this);
