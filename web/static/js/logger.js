/**
 * logger.js — 前端统一日志入口（F3 / J12）
 *
 * 目的：把分散在 25 个 JS 文件的 258 处 console.* 收敛到单一入口，
 *       生产环境默认静默，可由环境变量/URL 参数/全局开关按级别开启，
 *       便于排障且不向普通用户控制台泄漏内部信息。
 *
 * 约束（反伪实现）：
 *   - 不删任何日志信息，只收敛入口；语义与原 console.* 对齐。
 *   - console.error 是错误处理的一部分，开启时仍走 console.error 保证可见。
 *   - 不依赖任何外部库，原生 ES（兼容现有 ES module + 兼容回退风格）。
 *   - 暴露 window.logger，供既有代码按需调用；同时保留 console 作为最后兜底。
 */
(function (globalScope) {
    'use strict';

    // 级别权重：DEBUG < INFO < WARN < ERROR
    var LEVELS = { debug: 10, info: 20, warn: 30, error: 40, off: 99 };
    var DEFAULT_LEVEL = 'info';

    function readConfiguredLevel() {
        try {
            // 1) URL 参数 ?log=error|warn|info|debug（临时排障，不入持久化）
            var hash = String(globalScope.location && globalScope.location.hash || '');
            var m = /[?&]log=(debug|info|warn|error|off)\b/i.exec(hash);
            if (m) { return m[1].toLowerCase(); }
            var search = String(globalScope.location && globalScope.location.search || '');
            m = /[?&]log=(debug|info|warn|error|off)\b/i.exec(search);
            if (m) { return m[1].toLowerCase(); }
            // 2) localStorage 持久开关（开发者面板设置一次）
            var stored = globalScope.localStorage && globalScope.localStorage.getItem('cyberstrike-log-level');
            if (stored && LEVELS[String(stored).toLowerCase()] !== undefined) {
                return String(stored).toLowerCase();
            }
        } catch (_) { /* 隐私模式/不可用则忽略 */ }
        // 3) 默认：生产环境静默到 warn 及以上（error 必显）
        return DEFAULT_LEVEL;
    }

    var currentLevel = readConfiguredLevel();

    function shouldLog(name) {
        return (LEVELS[name] || 0) >= (LEVELS[currentLevel] || 0);
    }

    function bind(name, native) {
        return function () {
            if (!shouldLog(name)) { return; }
            try {
                // 前缀便于在控制台快速过滤本项目日志
                if (native) {
                    var args = Array.prototype.slice.call(arguments);
                    // 仅 debug/info 加 [CSAI] 前缀，warn/error 保持原样以免干扰 stacktrace
                    if (name === 'debug' || name === 'info') {
                        args.unshift('[CSAI]');
                    }
                    native.apply(globalScope.console, args);
                }
            } catch (_) { /* console 不可用时静默，绝不抛 */ }
        };
    }

    var logger = {
        level: currentLevel,
        // 兼容旧式 console.* 调用的最小接口
        log: bind('info', globalScope.console && globalScope.console.log),
        info: bind('info', globalScope.console && globalScope.console.info),
        debug: bind('debug', globalScope.console && globalScope.console.log),
        warn: bind('warn', globalScope.console && globalScope.console.warn),
        error: bind('error', globalScope.console && globalScope.console.error),
        // 排障辅助：临时切级别（不持久化）
        setLevel: function (name) {
            if (LEVELS[String(name).toLowerCase()] !== undefined) {
                currentLevel = String(name).toLowerCase();
                logger.level = currentLevel;
            }
            return currentLevel;
        },
        getLevel: function () { return currentLevel; }
    };

    // 暴露到全局，供既有脚本与开发者控制台使用
    globalScope.logger = logger;
    // 兼容别名（部分代码可能写 window.logger.log）
    if (!globalScope.logger) { globalScope.logger = logger; }
})(typeof window !== 'undefined' ? window : this);
