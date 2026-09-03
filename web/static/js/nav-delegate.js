/**
 * nav-delegate.js — 全局 data-action 事件委托（F4 CSP nonce 化配套）
 *
 * 目的：index.html 已 100% 移除 inline onclick（F4 收紧，CSP script-src 走 nonce-only）。
 *       原先 onclick="fn(a,b)" 全部迁移为 data-action="fn" data-arg0="a" data-arg1="b"，
 *       由本文件的 document 级 click 委托统一分发到对应全局函数。
 *
 * 委托契约（属性语义）：
 *   data-action="fnName"       必填：要调用的全局函数名（支持点分路径如 C2.closeModal）
 *   data-arg0..data-argN       可选：字面量参数（字符串/数字/JSON），按序传入
 *   data-pass-event="1"        可选：把 event 对象追加为最后一个参数（原 onclick="fn(event)"）
 *   data-pass-this="1"         可选：把被点节点追加为最后一个参数（原 onclick="fn(this)"）
 *   data-if-self="1"           可选：仅当 event.target === 被点节点才触发
 *                                （原 onclick="if(event.target===this)fn()"，
 *                                 用于模态遮罩点击关闭——点击遮罩内子元素不关闭）
 *   data-optional="1"          可选：函数不存在时静默跳过（原 onclick="fn && fn()" / typeof 守卫）
 *   data-action-chain="a|b"    可选：顺序调用多个函数（原 onclick="a();b()"）；
 *                                与 data-argN 搭配，argN 属于最后一个函数
 *   data-pre-reset-playbooks   可选：调用前置 action（playbooks 缓存重置特例）
 *   data-action="stopPropagation" 特例：仅阻止冒泡（原 onclick="event.stopPropagation()"）
 *
 * 回退与鲁棒：
 *   - 函数未定义：data-optional 时静默；否则 console.warn 一次（logger 兜底）不抛错。
 *   - data-action 为空：忽略。
 *   - 委托在 DOMContentLoaded 后绑定；重复绑定由 __navDelegateBound 守卫。
 *   - 未声明 data-action 的节点不受影响（无副作用）。
 */
(function (globalScope) {
    'use strict';

    // 按点分路径解析全局函数：'C2.closeModal' → window.C2.closeModal
    function resolveFn(name) {
        if (!name) { return null; }
        // 内置特例：clickById(data-arg0=id) — 原 onclick="document.getElementById('x').click()"
        if (name === 'clickById') {
            return function (id) {
                var el = document.getElementById(id);
                if (el && typeof el.click === 'function') { el.click(); }
            };
        }
        if (!name) { return null; }
        var parts = String(name).split('.');
        var cur = globalScope;
        for (var i = 0; i < parts.length; i++) {
            if (cur == null || typeof cur !== 'object' && typeof cur !== 'function') { return null; }
            cur = cur[parts[i]];
        }
        return typeof cur === 'function' ? cur : null;
    }

    // 从节点收集参数：优先 data-arg0..N（新迁移字面量参数），
    // 无 argN 时回退 data-page（F4 第一步导航迁移的既有属性，如 switchPage/toggleSubmenu）
    function collectLiteralArgs(node) {
        var args = [];
        var hasArg = false;
        for (var i = 0; i < 16; i++) {
            var v = node.getAttribute('data-arg' + i);
            if (v === null) { continue; }
            hasArg = true;
            args[i] = parseArgValue(v);
        }
        if (hasArg) {
            // 压缩空洞（data-arg0/arg2 无 arg1 的情况）
            var out = [];
            for (var j = 0; j < args.length; j++) {
                if (args[j] !== undefined) { out.push(args[j]); }
            }
            return out;
        }
        // F4 第一步迁移的导航属性兼容：data-page 作为单一参数
        var page = node.getAttribute('data-page');
        if (page) { return [page]; }
        return [];
    }

    // 字面量解析：JSON 兼容（数字/布尔/null/对象数组），失败回退字符串
    function parseArgValue(raw) {
        var t = String(raw).trim();
        if (t === '') { return ''; }
        try {
            return JSON.parse(t);
        } catch (e) { /* 非合法 JSON → 字符串 */ }
        return raw;
    }

    function invoke(node, fnName, event) {
        var fn = resolveFn(fnName);
        if (!fn) {
            if (node.getAttribute('data-optional') === '1') { return true; }
            // 未定义函数：warn 一次（delegate 缺失映射），不抛错不阻断其他 handler
            if (typeof globalScope.logger === 'object' && globalScope.logger && typeof globalScope.logger.warn === 'function') {
                globalScope.logger.warn('[nav-delegate] action 函数未定义: ' + fnName);
            } else if (typeof console !== 'undefined' && console.warn) {
                console.warn('[nav-delegate] action 函数未定义: ' + fnName);
            }
            return true;
        }
        var args = collectLiteralArgs(node);
        if (node.getAttribute('data-pass-this') !== null) { args.push(node); }
        if (node.getAttribute('data-pass-event') !== null) { args.push(event); }
        try {
            fn.apply(null, args);
            return true;
        } catch (e) {
            if (typeof globalScope.logger === 'object' && globalScope.logger && typeof globalScope.logger.error === 'function') {
                globalScope.logger.error('[nav-delegate] action 执行失败: ' + fnName, e);
            } else if (typeof console !== 'undefined' && console.error) {
                console.error('[nav-delegate] action 执行失败: ' + fnName, e);
            }
            return true;
        }
    }

    function handleDelegatedClick(event) {
        var target = event.target;
        if (!target || target.nodeType !== 1) { return; }

        // 沿父链向上寻找声明了 data-action / data-action-chain 的节点（最多 6 层，覆盖 svg/span 包裹）
        var node = target;
        var depth = 0;
        while (node && node.getAttribute && depth < 7) {
            var chain = node.getAttribute('data-action-chain');
            var action = node.getAttribute('data-action');

            if (chain || action) {
                // data-if-self：模态遮罩语义，只有点到遮罩自身才触发（点内容不关）
                if (node.getAttribute('data-if-self') === '1' && event.target !== node) {
                    return; // 点在遮罩内容上，不触发，也不冒泡到更上层 action
                }

                // 阻止重复触发：只在最内层 action 节点处理一次
                event.stopPropagation();

                // 特例：仅阻止冒泡
                if (action === 'stopPropagation' && !chain) {
                    return;
                }

                // 前置重置（playbooks 缓存特例）
                if (node.getAttribute('data-pre-reset-playbooks') !== null
                    && typeof globalScope.playbooksCache !== 'undefined') {
                    globalScope.playbooksCache = null;
                }

                if (chain) {
                    // 链式：顺序调用；argN/pass-* 属于最后一个函数
                    var fns = chain.split('|');
                    for (var i = 0; i < fns.length - 1; i++) {
                        invoke(node, fns[i], event);
                    }
                    invoke(node, fns[fns.length - 1], event);
                    return;
                }

                invoke(node, action, event);
                return;
            }
            node = node.parentNode;
            depth++;
        }
    }

    function bind() {
        if (globalScope.__navDelegateBound) { return; }
        document.addEventListener('click', handleDelegatedClick);
        globalScope.__navDelegateBound = true;
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', bind);
    } else {
        bind();
    }
})(typeof window !== 'undefined' ? window : this);
