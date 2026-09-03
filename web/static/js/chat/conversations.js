
// 语言切换时刷新攻击链统计文案（动态 textContent 不会随 applyTranslations 更新）
document.addEventListener('languagechange', function () {
    if (window.attackChainOriginalData && typeof updateAttackChainStats === 'function') {
        updateAttackChainStats(window.attackChainOriginalData);
    } else {
        const statsEl = document.getElementById('attack-chain-stats');
        if (statsEl && typeof window.t === 'function') {
            statsEl.textContent = window.t('attackChainModal.nodesEdges', { nodes: 0, edges: 0 });
        }
    }
});

// 关闭节点详情
function closeNodeDetails() {
    const detailsPanel = document.getElementById('attack-chain-details');
    const sidebar = document.querySelector('.attack-chain-sidebar');

    if (detailsPanel) {
        detailsPanel.style.opacity = '0';
        setTimeout(() => {
            detailsPanel.style.display = 'none';
            detailsPanel.style.opacity = '';
            // 移除详情激活态，图例恢复显示
            if (sidebar) sidebar.classList.remove('details-active');
        }, 220);
    } else if (sidebar) {
        sidebar.classList.remove('details-active');
    }

    if (attackChainCytoscape) {
        attackChainCytoscape.elements().unselect();
    }
}

// 关闭攻击链模态框
function closeAttackChainModal() {
    closeAppModal('attack-chain-modal');

    // 关闭节点详情
    closeNodeDetails();

    // 清理Cytoscape实例
    if (attackChainCytoscape) {
        attackChainCytoscape.destroy();
        attackChainCytoscape = null;
    }

    currentAttackChainConversationId = null;
}

// 刷新攻击链（重新加载）
// 注意：此函数允许在加载过程中调用，用于检查生成状态
function refreshAttackChain() {
    if (currentAttackChainConversationId) {
        // 临时允许刷新，即使正在加载中（用于检查生成状态）
        const wasLoading = isAttackChainLoading(currentAttackChainConversationId);
        setAttackChainLoading(currentAttackChainConversationId, false); // 临时重置，允许刷新
        loadAttackChain(currentAttackChainConversationId).finally(() => {
            // 如果之前正在加载（409 情况），恢复加载状态
            // 否则保持 false（正常完成）
            if (wasLoading) {
                // 检查是否仍然需要保持加载状态（如果还是 409，会在 loadAttackChain 中处理）
                // 这里我们假设如果成功加载，则重置状态
                // 如果还是 409，loadAttackChain 会保持加载状态
            }
        });
    }
}

// 重新生成攻击链
async function regenerateAttackChain() {
    if (!currentAttackChainConversationId) {
        return;
    }

    // 防止重复点击（只检查当前对话的加载状态）
    if (isAttackChainLoading(currentAttackChainConversationId)) {
        return;
    }

    // 保存请求时的对话ID，防止串台
    const savedConversationId = currentAttackChainConversationId;
    setAttackChainLoading(savedConversationId, true);

    const container = document.getElementById('attack-chain-container');
    if (container) {
        container.innerHTML = '<div class="loading-spinner">重新生成中...</div>';
    }

    // 禁用重新生成按钮
    const regenerateBtn = document.querySelector('button[onclick="regenerateAttackChain()"]');
    if (regenerateBtn) {
        regenerateBtn.disabled = true;
        regenerateBtn.style.opacity = '0.5';
        regenerateBtn.style.cursor = 'not-allowed';
    }

    try {
        // 调用重新生成接口
        const response = await apiFetch(`/api/attack-chain/${savedConversationId}/regenerate`, {
            method: 'POST'
        });

        if (!response.ok) {
            // 处理 409 Conflict（正在生成中）
            if (response.status === 409) {
                const error = await response.json();
                if (container) {
                    container.innerHTML = `
                        <div class="loading-spinner" style="text-align: center; padding: 40px;">
                            <div style="margin-bottom: 16px;">⏳ 攻击链正在生成中...</div>
                            <div style="color: var(--text-secondary); font-size: 0.875rem;">
                                请稍候，生成完成后将自动显示
                            </div>
                            <button class="btn-secondary" onclick="refreshAttackChain()" style="margin-top: 16px;">
                                刷新查看进度
                            </button>
                        </div>
                    `;
                }
                // 5秒后自动刷新
                // savedConversationId 已在函数开始处定义
                setTimeout(() => {
                    // 检查当前显示的对话ID是否匹配，且仍在加载中
                    if (currentAttackChainConversationId === savedConversationId &&
                        isAttackChainLoading(savedConversationId)) {
                        refreshAttackChain();
                    }
                }, 5000);
                return;
            }

            const error = await response.json();
            throw new Error(error.error || '重新生成攻击链失败');
        }

        const chainData = await response.json();

        // 检查当前显示的对话ID是否匹配，防止串台
        if (currentAttackChainConversationId !== savedConversationId) {
            logger.info('攻击链数据已返回，但当前显示的对话已切换，忽略此次渲染', {
                returned: savedConversationId,
                current: currentAttackChainConversationId
            });
            setAttackChainLoading(savedConversationId, false);
            return;
        }

        // 渲染攻击链
        renderAttackChain(chainData);

        // 更新统计信息
        updateAttackChainStats(chainData);

    } catch (error) {
        logger.error('重新生成攻击链失败:', error);
        if (container) {
            container.innerHTML = `<div class="error-message">重新生成失败: ${escapeHtml(error.message)}</div>`;
        }
    } finally {
        setAttackChainLoading(savedConversationId, false);

        // 恢复重新生成按钮
        if (regenerateBtn) {
            regenerateBtn.disabled = false;
            regenerateBtn.style.opacity = '1';
            regenerateBtn.style.cursor = 'pointer';
        }
    }
}

// ==================== 攻击链导出（精美版） ====================

// XML/HTML 转义
function _acEscapeXml(str) {
    if (str === null || str === undefined) return '';
    return String(str)
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&apos;');
}

// 按字符宽度做软换行（支持中英文混排），返回字符串数组
function _acWrapLabel(label, maxChars, maxLines) {
    if (!label) return [''];
    const text = String(label).replace(/\s+/g, ' ').trim();
    if (!text) return [''];
    // 以"字符宽度"估算：中文算2，其他算1
    const width = (ch) => (/[\u4e00-\u9fa5\uff00-\uffef]/.test(ch) ? 2 : 1);
    const maxW = maxChars * 1.8; // 以单位宽度衡量

    const lines = [];
    let buf = '';
    let bufW = 0;
    let lastSpaceIdx = -1;
    for (let i = 0; i < text.length; i++) {
        const ch = text[i];
        const w = width(ch);
        if (ch === ' ') lastSpaceIdx = buf.length;
        if (bufW + w > maxW) {
            // 在空格处换行（英文更自然）
            let cut = buf;
            let rest = '';
            if (lastSpaceIdx > 0 && lastSpaceIdx >= buf.length - 10) {
                cut = buf.substring(0, lastSpaceIdx);
                rest = buf.substring(lastSpaceIdx + 1);
            }
            lines.push(cut);
            if (lines.length >= maxLines) {
                // 在最后加省略号
                const last = lines[lines.length - 1];
                lines[lines.length - 1] = _acTruncateToWidth(last, maxW - 2) + '…';
                return lines;
            }
            buf = rest + ch;
            bufW = 0;
            for (let j = 0; j < buf.length; j++) bufW += width(buf[j]);
            lastSpaceIdx = -1;
        } else {
            buf += ch;
            bufW += w;
        }
    }
    if (buf) lines.push(buf);
    if (lines.length > maxLines) {
        const kept = lines.slice(0, maxLines);
        kept[kept.length - 1] = _acTruncateToWidth(kept[kept.length - 1], maxW - 2) + '…';
        return kept;
    }
    return lines;
}

function _acTruncateToWidth(str, maxW) {
    const width = (ch) => (/[\u4e00-\u9fa5\uff00-\uffef]/.test(ch) ? 2 : 1);
    let w = 0;
    let out = '';
    for (let i = 0; i < str.length; i++) {
        w += width(str[i]);
        if (w > maxW) break;
        out += str[i];
    }
    return out;
}

// 计算颜色对应的深色主色（辅助 accent 深色）
function _acDarken(hex, amount) {
    try {
        const h = hex.replace('#', '');
        const r = parseInt(h.substring(0, 2), 16);
        const g = parseInt(h.substring(2, 4), 16);
        const b = parseInt(h.substring(4, 6), 16);
        const f = (c) => Math.max(0, Math.min(255, Math.round(c * (1 - amount))));
        return '#' + [f(r), f(g), f(b)].map(x => x.toString(16).padStart(2, '0')).join('');
    } catch (e) {
        return hex;
    }
}

// 从当前 Cytoscape 实例收集导出所需的节点/边信息
function _acCollectExportData() {
    if (!attackChainCytoscape) return null;
    const nodes = [];
    attackChainCytoscape.nodes().forEach(n => {
        // 过滤隐藏节点
        if (n.style('display') === 'none') return;
        const pos = n.position();
        // 读取 Cytoscape 中实际渲染的节点尺寸，保证导出与看板一致
        let w = n.outerWidth ? n.outerWidth() : n.width();
        let h = n.outerHeight ? n.outerHeight() : n.height();
        // 兜底
        if (!w || !isFinite(w) || w < 40) w = 280;
        if (!h || !isFinite(h) || h < 30) h = 96;
        nodes.push({
            id: n.id(),
            x: pos.x,
            y: pos.y,
            w: w,
            h: h,
            type: n.data('type') || '',
            typeLabel: n.data('typeLabel') || '',
            typeBadge: n.data('typeBadge') || '•',
            typeColor: n.data('typeColor') || '#334155',
            accentColor: n.data('accentColor') || '#94a3b8',
            bgGradientStart: n.data('bgGradientStart') || '#FFFFFF',
            bgGradientEnd: n.data('bgGradientEnd') || '#F8FAFC',
            riskScore: n.data('riskScore') || 0,
            label: n.data('originalLabel') || n.data('label') || n.id(),
            metadata: n.data('metadata') || {}
        });
    });

    const edges = [];
    attackChainCytoscape.edges().forEach(e => {
        if (e.style('display') === 'none') return;
        const info = getEdgeNodes(e);
        if (!info.valid) return;
        const s = info.source.position();
        const t = info.target.position();
        edges.push({
            id: e.id(),
            source: info.source.id(),
            target: info.target.id(),
            sx: s.x, sy: s.y,
            tx: t.x, ty: t.y,
            type: e.data('type') || 'leads_to'
        });
    });

    return { nodes, edges };
}

// 节点类型图标（SVG path）—— 真正的矢量图标
function _acGetNodeIconPath(type) {
    // 24×24 视图下的 path（会被缩放到 iconSize）
    if (type === 'target') {
        // 靶子（同心圆 + 十字准星）
        return 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.42 0-8-3.58-8-8s3.58-8 8-8 8 3.58 8 8-3.58 8-8 8zm0-14c-3.31 0-6 2.69-6 6s2.69 6 6 6 6-2.69 6-6-2.69-6-6-6zm0 10c-2.21 0-4-1.79-4-4s1.79-4 4-4 4 1.79 4 4-1.79 4-4 4z';
    }
    if (type === 'action') {
        // 闪电（行动）
        return 'M7 2v11h3v9l7-12h-4l4-8z';
    }
    if (type === 'vulnerability') {
        // 盾牌警告
        return 'M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm-1 6h2v6h-2V7zm0 8h2v2h-2v-2z';
    }
    // 默认点
    return 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z';
}

// 获取节点风险等级标签（漏洞节点用）
function _acGetRiskLabel(score) {
    if (score >= 80) return '严重';
    if (score >= 60) return '高';
    if (score >= 40) return '中';
    if (score > 0) return '低';
    return '';
}

// 生成精美 SVG 字符串（高端商业报告风格）
function _acBuildSvgString() {
    const data = _acCollectExportData();
    if (!data || data.nodes.length === 0) throw new Error('没有可导出的数据');

    const { nodes, edges } = data;

    // --- 关键：重新统一节点尺寸为大卡片设计（SVG 中使用自己的规格） ---
    // SVG 导出时采用更大的卡片，让信息层次清晰
    nodes.forEach(n => {
        // 根据当前在 Cytoscape 中的位置，重新分配 SVG 版本的宽高
        // 统一使用大卡片以便展示完整信息
        n.w = 380;
        n.h = 140;
    });

    // 计算图的包围盒
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    nodes.forEach(n => {
        minX = Math.min(minX, n.x - n.w / 2);
        minY = Math.min(minY, n.y - n.h / 2);
        maxX = Math.max(maxX, n.x + n.w / 2);
        maxY = Math.max(maxY, n.y + n.h / 2);
    });

    // ==================== 版面布局参数 ====================
    const GRAPH_PAD = 100;                   // 图区域内部留白
    const HEADER_H = 128;                    // 顶部标题栏（加大）
    const FOOTER_H = 56;                     // 底部信息栏
    const LEGEND_W = 320;                    // 右侧图例面板
    const OUTER_PAD = 32;                    // 最外层留白

    const rawGraphW = (maxX - minX) + GRAPH_PAD * 2;
    const rawGraphH = (maxY - minY) + GRAPH_PAD * 2;

    const minGraphW = 900;
    const minGraphH = 620;
    const graphW = Math.max(rawGraphW, minGraphW);
    const graphH = Math.max(rawGraphH, minGraphH);

    const contentW = graphW + LEGEND_W + 36;
    const contentH = graphH + HEADER_H + FOOTER_H + 24;

    const totalW = contentW + OUTER_PAD * 2;
    const totalH = contentH + OUTER_PAD * 2;

    // 图区域在卡片坐标系的位置
    const graphAreaX = OUTER_PAD + 20;
    const graphAreaY = OUTER_PAD + HEADER_H;
    const graphAreaW = graphW - 4;
    const graphAreaH = contentH - HEADER_H - FOOTER_H;

    // 节点坐标映射
    const graphCenterOffsetX = (graphAreaW - rawGraphW) / 2;
    const graphCenterOffsetY = (graphAreaH - rawGraphH) / 2;
    const graphOriginX = graphAreaX + GRAPH_PAD + graphCenterOffsetX - minX;
    const graphOriginY = graphAreaY + GRAPH_PAD + graphCenterOffsetY - minY;

    // 图例区坐标
    const legendX = graphAreaX + graphW + 16;

    // 统计信息
    const nodeCount = nodes.length;
    const edgeCount = edges.length;
    const vulnNodes = nodes.filter(n => n.type === 'vulnerability');
    const actionNodes = nodes.filter(n => n.type === 'action');
    const targetNodes = nodes.filter(n => n.type === 'target');
    const criticalCount = vulnNodes.filter(n => n.riskScore >= 80).length;
    const highCount = vulnNodes.filter(n => n.riskScore >= 60 && n.riskScore < 80).length;
    const medCount = vulnNodes.filter(n => n.riskScore >= 40 && n.riskScore < 60).length;
    const lowCount = vulnNodes.filter(n => n.riskScore > 0 && n.riskScore < 40).length;

    const timestamp = new Date();
    const ts = timestamp.getFullYear() + '-' +
        String(timestamp.getMonth() + 1).padStart(2, '0') + '-' +
        String(timestamp.getDate()).padStart(2, '0') + ' ' +
        String(timestamp.getHours()).padStart(2, '0') + ':' +
        String(timestamp.getMinutes()).padStart(2, '0');

    // 类型主题色（高级感配色）
    const typeTheme = {
        'target': { primary: '#4F46E5', light: '#EEF2FF', dark: '#3730A3', text: '#312E81', label: '目标' },
        'action-success': { primary: '#10B981', light: '#ECFDF5', dark: '#047857', text: '#064E3B', label: '行动' },
        'action-neutral': { primary: '#64748B', light: '#F8FAFC', dark: '#475569', text: '#334155', label: '行动' },
        'vuln-critical': { primary: '#E11D48', light: '#FFF1F2', dark: '#BE123C', text: '#881337', label: '漏洞' },
        'vuln-high': { primary: '#EA580C', light: '#FFF7ED', dark: '#C2410C', text: '#7C2D12', label: '漏洞' },
        'vuln-med': { primary: '#CA8A04', light: '#FEFCE8', dark: '#A16207', text: '#713F12', label: '漏洞' },
        'vuln-low': { primary: '#0D9488', light: '#F0FDFA', dark: '#0F766E', text: '#134E4A', label: '漏洞' }
    };

    function themeFor(n) {
        if (n.type === 'target') return typeTheme['target'];
        if (n.type === 'action') {
            const m = n.metadata || {};
            const findings = m.findings || [];
            const hasFindings = Array.isArray(findings) && findings.length > 0;
            const isFailed = m.status === 'failed_insight';
            return (hasFindings && !isFailed) ? typeTheme['action-success'] : typeTheme['action-neutral'];
        }
        if (n.type === 'vulnerability') {
            const s = n.riskScore || 0;
            if (s >= 80) return typeTheme['vuln-critical'];
            if (s >= 60) return typeTheme['vuln-high'];
            if (s >= 40) return typeTheme['vuln-med'];
            return typeTheme['vuln-low'];
        }
        return typeTheme['action-neutral'];
    }

    // 边的起止主题
    const nodesMap = new Map(nodes.map(n => [n.id, n]));

    // ==================== 开始组装 SVG ====================
    const parts = [];
    parts.push(`<?xml version="1.0" encoding="UTF-8"?>`);
    parts.push(`<svg xmlns="http://www.w3.org/2000/svg" width="${totalW}" height="${totalH}" viewBox="0 0 ${totalW} ${totalH}" font-family="-apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', 'Hiragino Sans GB', Roboto, Helvetica, Arial, sans-serif">`);

    // ==================== defs ====================
    parts.push(`<defs>`);

    // 根背景渐变：极淡暖灰
    parts.push(`<linearGradient id="ac-bg" x1="0%" y1="0%" x2="100%" y2="100%">
        <stop offset="0%" stop-color="#FAFBFC"/>
        <stop offset="100%" stop-color="#F1F5F9"/>
    </linearGradient>`);

    // 角落光晕
    parts.push(`<radialGradient id="ac-glow-1" cx="50%" cy="50%" r="50%">
        <stop offset="0%" stop-color="#6366F1" stop-opacity="0.12"/>
        <stop offset="100%" stop-color="#6366F1" stop-opacity="0"/>
    </radialGradient>`);
    parts.push(`<radialGradient id="ac-glow-2" cx="50%" cy="50%" r="50%">
        <stop offset="0%" stop-color="#EC4899" stop-opacity="0.08"/>
        <stop offset="100%" stop-color="#EC4899" stop-opacity="0"/>
    </radialGradient>`);
    parts.push(`<radialGradient id="ac-glow-3" cx="50%" cy="50%" r="50%">
        <stop offset="0%" stop-color="#06B6D4" stop-opacity="0.08"/>
        <stop offset="100%" stop-color="#06B6D4" stop-opacity="0"/>
    </radialGradient>`);

    // 品牌渐变（标题用）
    parts.push(`<linearGradient id="ac-brand" x1="0%" y1="0%" x2="100%" y2="0%">
        <stop offset="0%" stop-color="#4F46E5"/>
        <stop offset="50%" stop-color="#7C3AED"/>
        <stop offset="100%" stop-color="#EC4899"/>
    </linearGradient>`);

    // 网格点阵（非常淡）
    parts.push(`<pattern id="ac-dot" x="0" y="0" width="24" height="24" patternUnits="userSpaceOnUse">
        <circle cx="12" cy="12" r="1" fill="#0F172A" fill-opacity="0.06"/>
    </pattern>`);

    // 节点卡片阴影（多层阴影，更有层次）
    parts.push(`<filter id="ac-shadow-card" x="-20%" y="-20%" width="140%" height="140%">
        <feDropShadow dx="0" dy="1" stdDeviation="1.5" flood-color="#0F172A" flood-opacity="0.06"/>
        <feDropShadow dx="0" dy="6" stdDeviation="12" flood-color="#0F172A" flood-opacity="0.08"/>
    </filter>`);

    // 图标徽章阴影
    parts.push(`<filter id="ac-shadow-icon" x="-30%" y="-30%" width="160%" height="160%">
        <feDropShadow dx="0" dy="2" stdDeviation="3" flood-color="#0F172A" flood-opacity="0.15"/>
    </filter>`);

    // 风险徽章阴影
    parts.push(`<filter id="ac-shadow-badge" x="-30%" y="-30%" width="160%" height="160%">
        <feDropShadow dx="0" dy="1.5" stdDeviation="2.5" flood-color="#0F172A" flood-opacity="0.18"/>
    </filter>`);

    // 为每个节点定义图标渐变（大图标用）
    Object.keys(typeTheme).forEach(key => {
        const t = typeTheme[key];
        parts.push(`<linearGradient id="ac-icon-grad-${key}" x1="0%" y1="0%" x2="100%" y2="100%">
            <stop offset="0%" stop-color="${t.primary}"/>
            <stop offset="100%" stop-color="${t.dark}"/>
        </linearGradient>`);
    });

    // 边使用渐变（源 -> 目标）
    edges.forEach((e, idx) => {
        const sNode = nodesMap.get(e.source);
        const tNode = nodesMap.get(e.target);
        if (!sNode || !tNode) return;
        const sTheme = themeFor(sNode);
        const tTheme = themeFor(tNode);
        parts.push(`<linearGradient id="ac-edge-grad-${idx}" gradientUnits="userSpaceOnUse" x1="${e.sx}" y1="${e.sy}" x2="${e.tx}" y2="${e.ty}">
            <stop offset="0%" stop-color="${sTheme.primary}" stop-opacity="0.7"/>
            <stop offset="100%" stop-color="${tTheme.primary}" stop-opacity="0.9"/>
        </linearGradient>`);
    });

    // 为每种主题色定义箭头标记
    Object.keys(typeTheme).forEach(key => {
        const t = typeTheme[key];
        parts.push(`<marker id="ac-arrow-${key}" viewBox="0 0 12 12" refX="10" refY="6" markerWidth="8" markerHeight="8" orient="auto-start-reverse" markerUnits="strokeWidth">
            <path d="M 0 0 L 12 6 L 0 12 L 3 6 Z" fill="${t.primary}"/>
        </marker>`);
    });

    parts.push(`</defs>`);

    // ==================== 背景 ====================
    parts.push(`<rect x="0" y="0" width="${totalW}" height="${totalH}" fill="url(#ac-bg)"/>`);
    // 角落光晕
    parts.push(`<ellipse cx="${totalW * 0.1}" cy="${totalH * 0.15}" rx="${totalW * 0.4}" ry="${totalH * 0.4}" fill="url(#ac-glow-1)"/>`);
    parts.push(`<ellipse cx="${totalW * 0.9}" cy="${totalH * 0.85}" rx="${totalW * 0.35}" ry="${totalH * 0.35}" fill="url(#ac-glow-2)"/>`);
    parts.push(`<ellipse cx="${totalW * 0.5}" cy="${totalH * 0.1}" rx="${totalW * 0.3}" ry="${totalH * 0.3}" fill="url(#ac-glow-3)"/>`);

    // ==================== 主卡片 ====================
    parts.push(`<rect x="${OUTER_PAD}" y="${OUTER_PAD}" width="${contentW}" height="${contentH}" rx="24" ry="24" fill="#FFFFFF" stroke="rgba(15,23,42,0.06)" stroke-width="1" filter="url(#ac-shadow-card)"/>`);

    // ==================== 顶部标题栏 ====================
    const tX = OUTER_PAD + 40;
    const tY = OUTER_PAD + 28;

    // 左侧 Logo 色块（大，渐变，有设计感）
    parts.push(`<g filter="url(#ac-shadow-icon)">`);
    parts.push(`<rect x="${tX - 4}" y="${tY}" width="48" height="48" rx="12" fill="url(#ac-brand)"/>`);
    // Logo 图标（六边形 + 闪电）
    parts.push(`<g transform="translate(${tX - 4 + 12}, ${tY + 12}) scale(0.9)">
        <path d="M12 2L3 7v10l9 5 9-5V7z" fill="none" stroke="#FFFFFF" stroke-width="1.8" stroke-linejoin="round"/>
        <path d="M10 7l-2 5h3l-1 4 4-5h-3l1-4z" fill="#FFFFFF"/>
    </g>`);
    parts.push(`</g>`);

    // 主标题（超大、粗体）
    parts.push(`<text x="${tX + 56}" y="${tY + 26}" font-size="26" font-weight="800" fill="#0F172A" letter-spacing="-0.6px">攻击链可视化报告</text>`);

    // 副标题（小字、次要色）
    parts.push(`<text x="${tX + 56}" y="${tY + 50}" font-size="13" font-weight="500" fill="#64748B" letter-spacing="0.1px">Attack Chain Analysis · ${_acEscapeXml(ts)}</text>`);

    // 右上角：关键统计胶囊（3 个）
    const kpiY = OUTER_PAD + 28;
    const kpiH = 48;
    const kpiGap = 12;
    const kpiW = 110;
    const kpiItems = [
        { label: '节点', value: nodeCount, color: '#4F46E5' },
        { label: '连线', value: edgeCount, color: '#06B6D4' },
        { label: '严重漏洞', value: criticalCount, color: criticalCount > 0 ? '#E11D48' : '#94A3B8' }
    ];
    let kpiXStart = OUTER_PAD + contentW - 40 - (kpiW * kpiItems.length + kpiGap * (kpiItems.length - 1));
    kpiItems.forEach((kpi, i) => {
        const kx = kpiXStart + i * (kpiW + kpiGap);
        // 卡片背景
        parts.push(`<rect x="${kx}" y="${kpiY}" width="${kpiW}" height="${kpiH}" rx="12" fill="#FFFFFF" stroke="${kpi.color}" stroke-opacity="0.15" stroke-width="1"/>`);
        // 左侧细条
        parts.push(`<rect x="${kx}" y="${kpiY + 10}" width="3" height="${kpiH - 20}" rx="1.5" fill="${kpi.color}"/>`);
        // 数值（大字）
        parts.push(`<text x="${kx + 16}" y="${kpiY + 26}" font-size="20" font-weight="800" fill="#0F172A" letter-spacing="-0.4px">${kpi.value}</text>`);
        // 标签（小字）
        parts.push(`<text x="${kx + 16}" y="${kpiY + 40}" font-size="10.5" font-weight="600" fill="#64748B" letter-spacing="0.4px">${_acEscapeXml(kpi.label)}</text>`);
    });

    // 标题分隔线（渐变淡化）
    parts.push(`<line x1="${OUTER_PAD + 40}" y1="${OUTER_PAD + HEADER_H - 10}" x2="${OUTER_PAD + contentW - 40}" y2="${OUTER_PAD + HEADER_H - 10}" stroke="rgba(15,23,42,0.08)" stroke-width="1"/>`);

    // ==================== 图区域 ====================
    parts.push(`<rect x="${graphAreaX}" y="${graphAreaY}" width="${graphAreaW}" height="${graphAreaH}" rx="18" fill="#FCFCFD" stroke="rgba(15,23,42,0.05)" stroke-width="1"/>`);
    parts.push(`<rect x="${graphAreaX}" y="${graphAreaY}" width="${graphAreaW}" height="${graphAreaH}" rx="18" fill="url(#ac-dot)" opacity="0.7"/>`);

    // ==================== 开始绘制图形 ====================
    parts.push(`<g transform="translate(${graphOriginX}, ${graphOriginY})">`);

    // ---- 边（渐变、柔和曲线） ----
    edges.forEach((e, idx) => {
        const sNode = nodesMap.get(e.source);
        const tNode = nodesMap.get(e.target);
        if (!sNode || !tNode) return;
        const tTheme = themeFor(tNode);

        const dx = e.tx - e.sx;
        const dy = e.ty - e.sy;
        const mag = Math.sqrt(dx * dx + dy * dy) || 1;
        const offset = Math.min(80, mag * 0.25);
        const nx = -dy / mag;
        const ny = dx / mag;
        const cx = (e.sx + e.tx) / 2 + nx * offset;
        const cy = (e.sy + e.ty) / 2 + ny * offset;
        const shrink = 22;
        const ex = e.tx - (dx / mag) * shrink;
        const ey = e.ty - (dy / mag) * shrink;

        const strokeWidth = (e.type === 'discovers' || e.type === 'enables') ? 2.4 : 2;
        const strokeDash = e.type === 'targets' ? 'stroke-dasharray="10,5"' : '';
        // 目标箭头 key（根据目标节点的主题）
        const targetThemeKey = Object.keys(typeTheme).find(k => typeTheme[k] === tTheme) || 'action-neutral';

        // 先画一个轻微的 halo（背景光晕）
        parts.push(`<path d="M ${e.sx.toFixed(1)} ${e.sy.toFixed(1)} Q ${cx.toFixed(1)} ${cy.toFixed(1)} ${ex.toFixed(1)} ${ey.toFixed(1)}" fill="none" stroke="${tTheme.primary}" stroke-width="${strokeWidth + 4}" stroke-linecap="round" stroke-opacity="0.08" ${strokeDash}/>`);
        // 主线
        parts.push(`<path d="M ${e.sx.toFixed(1)} ${e.sy.toFixed(1)} Q ${cx.toFixed(1)} ${cy.toFixed(1)} ${ex.toFixed(1)} ${ey.toFixed(1)}" fill="none" stroke="url(#ac-edge-grad-${idx})" stroke-width="${strokeWidth}" stroke-linecap="round" ${strokeDash} marker-end="url(#ac-arrow-${targetThemeKey})"/>`);
    });

    // ---- 节点（大卡片设计） ----
    nodes.forEach((n, i) => {
        const theme = themeFor(n);
        const themeKey = Object.keys(typeTheme).find(k => typeTheme[k] === theme) || 'action-neutral';

        const x = n.x - n.w / 2;
        const y = n.y - n.h / 2;
        const r = 18;  // 圆角

        // ========== 卡片主体 ==========
        // 柔和阴影 + 纯白背景
        parts.push(`<g filter="url(#ac-shadow-card)">`);
        parts.push(`<rect x="${x}" y="${y}" width="${n.w}" height="${n.h}" rx="${r}" fill="#FFFFFF"/>`);
        parts.push(`</g>`);
        // 顶部主题色条（很淡的渐变）
        parts.push(`<rect x="${x}" y="${y}" width="${n.w}" height="${n.h}" rx="${r}" fill="${theme.primary}" fill-opacity="0.02"/>`);
        // 细边框
        parts.push(`<rect x="${x}" y="${y}" width="${n.w}" height="${n.h}" rx="${r}" fill="none" stroke="${theme.primary}" stroke-opacity="0.18" stroke-width="1"/>`);
        // 顶部彩色装饰条（小圆点序列或渐变条）
        parts.push(`<rect x="${x + 20}" y="${y}" width="${n.w - 40}" height="3" rx="1.5" fill="${theme.primary}" fill-opacity="0.5"/>`);

        const padX = 24;
        const padY = 22;

        // ========== 顶部：大图标 + 类型标签 + 右侧徽章 ==========
        const iconSize = 44;
        const iconX = x + padX;
        const iconY = y + padY;

        // 图标背景（渐变方形）
        parts.push(`<g filter="url(#ac-shadow-icon)">`);
        parts.push(`<rect x="${iconX}" y="${iconY}" width="${iconSize}" height="${iconSize}" rx="12" fill="url(#ac-icon-grad-${themeKey})"/>`);
        parts.push(`</g>`);
        // 图标 path（白色，缩放）
        const iconPath = _acGetNodeIconPath(n.type);
        const iconScale = (iconSize * 0.55) / 24;
        const iconInnerOffset = (iconSize - 24 * iconScale) / 2;
        parts.push(`<g transform="translate(${iconX + iconInnerOffset}, ${iconY + iconInnerOffset}) scale(${iconScale.toFixed(3)})">
            <path d="${iconPath}" fill="#FFFFFF"/>
        </g>`);

        // 类型标签（在图标右侧）
        const typeTextX = iconX + iconSize + 14;
        // 类型英文（TYPE LABEL，淡色小字）
        const typeEn = n.type === 'target' ? 'TARGET' : n.type === 'action' ? 'ACTION' : n.type === 'vulnerability' ? 'VULNERABILITY' : (n.type || '').toUpperCase();
        parts.push(`<text x="${typeTextX}" y="${iconY + 14}" font-size="10" font-weight="700" fill="${theme.dark}" fill-opacity="0.75" letter-spacing="1.2px">${_acEscapeXml(typeEn)}</text>`);
        // 类型中文（大字，主要色）
        parts.push(`<text x="${typeTextX}" y="${iconY + 34}" font-size="16" font-weight="700" fill="${theme.text}" letter-spacing="-0.2px">${_acEscapeXml(theme.label)}</text>`);

        // ========== 右上角徽章 ==========
        const badgeY = iconY + 2;
        const badgeH = 26;
        if (n.type === 'vulnerability' && n.riskScore > 0) {
            // 风险分数徽章（大号，渐变背景）
            const riskLabel = _acGetRiskLabel(n.riskScore);
            const badgeText = `${riskLabel} · ${n.riskScore}`;
            const badgeW = 90;
            const bx = x + n.w - badgeW - padX;
            parts.push(`<g filter="url(#ac-shadow-badge)">`);
            parts.push(`<rect x="${bx}" y="${badgeY}" width="${badgeW}" height="${badgeH}" rx="${badgeH / 2}" fill="url(#ac-icon-grad-${themeKey})"/>`);
            parts.push(`<text x="${bx + badgeW / 2}" y="${badgeY + badgeH / 2 + 4.5}" text-anchor="middle" font-size="12" font-weight="700" fill="#FFFFFF" letter-spacing="0.2px">${_acEscapeXml(badgeText)}</text>`);
            parts.push(`</g>`);
        } else if (n.type === 'action') {
            const m = n.metadata || {};
            const findings = m.findings || [];
            const hasFindings = Array.isArray(findings) && findings.length > 0;
            const isFailed = m.status === 'failed_insight';
            if (hasFindings || isFailed) {
                const text = isFailed ? '有线索' : `发现 ${findings.length}`;
                const badgeW = 70;
                const bx = x + n.w - badgeW - padX;
                parts.push(`<rect x="${bx}" y="${badgeY}" width="${badgeW}" height="${badgeH}" rx="${badgeH / 2}" fill="${theme.primary}" fill-opacity="0.12" stroke="${theme.primary}" stroke-opacity="0.4" stroke-width="1"/>`);
                // 小圆点（状态指示）
                parts.push(`<circle cx="${bx + 12}" cy="${badgeY + badgeH / 2}" r="3" fill="${theme.primary}"/>`);
                parts.push(`<text x="${bx + 20}" y="${badgeY + badgeH / 2 + 4.5}" font-size="11.5" font-weight="700" fill="${theme.dark}">${_acEscapeXml(text)}</text>`);
            }
        } else if (n.type === 'target') {
            // 目标节点显示"目标"标识
            const badgeW = 60;
            const bx = x + n.w - badgeW - padX;
            parts.push(`<rect x="${bx}" y="${badgeY}" width="${badgeW}" height="${badgeH}" rx="${badgeH / 2}" fill="${theme.primary}" fill-opacity="0.12" stroke="${theme.primary}" stroke-opacity="0.4" stroke-width="1"/>`);
            parts.push(`<text x="${bx + badgeW / 2}" y="${badgeY + badgeH / 2 + 4.5}" text-anchor="middle" font-size="11.5" font-weight="700" fill="${theme.dark}" letter-spacing="0.3px">主目标</text>`);
        }

        // ========== 主标题 ==========
        const contentTopY = iconY + iconSize + 18;
        const titleFontSize = 16;
        const titleLineH = titleFontSize + 6;
        const contentAvailW = n.w - padX * 2;
        const charsPerLine = Math.max(10, Math.floor(contentAvailW / (titleFontSize * 0.58)));
        const titleLines = _acWrapLabel(n.label, charsPerLine, 2);
        titleLines.forEach((ln, idx) => {
            parts.push(`<text x="${x + padX}" y="${contentTopY + idx * titleLineH}" font-size="${titleFontSize}" font-weight="700" fill="#0F172A" letter-spacing="-0.2px">${_acEscapeXml(ln)}</text>`);
        });

        // ========== 底部元信息栏 ==========
        const metaY = y + n.h - 22;
        // 分隔线
        parts.push(`<line x1="${x + padX}" y1="${metaY - 10}" x2="${x + n.w - padX}" y2="${metaY - 10}" stroke="rgba(15,23,42,0.06)" stroke-width="1"/>`);

        // 生成元信息文本
        const metaItems = [];
        if (n.type === 'target') {
            const tgt = (n.metadata && n.metadata.target) ? n.metadata.target : null;
            if (tgt) metaItems.push({ icon: 'loc', text: _acTruncateToWidth(tgt, 26) });
        } else if (n.type === 'action') {
            const toolName = n.metadata && n.metadata.tool_name;
            if (toolName) metaItems.push({ icon: 'tool', text: _acTruncateToWidth(toolName, 20) });
            const intent = n.metadata && n.metadata.tool_intent;
            if (intent) metaItems.push({ icon: 'aim', text: _acTruncateToWidth(intent, 22) });
        } else if (n.type === 'vulnerability') {
            const vt = n.metadata && n.metadata.vulnerability_type;
            if (vt) metaItems.push({ icon: 'shield', text: _acTruncateToWidth(vt, 22) });
            const sev = n.metadata && n.metadata.severity;
            if (sev) metaItems.push({ icon: 'alert', text: _acTruncateToWidth(sev, 12) });
        }
        if (metaItems.length === 0) {
            // 没有元信息时显示节点ID简短版
            metaItems.push({ icon: 'hash', text: _acTruncateToWidth(n.id || '', 20) });
        }

        // 元信息图标 path（24x24）
        const metaIconPaths = {
            'loc': 'M12 2C8.13 2 5 5.13 5 9c0 5.25 7 13 7 13s7-7.75 7-13c0-3.87-3.13-7-7-7zm0 9.5a2.5 2.5 0 1 1 0-5 2.5 2.5 0 0 1 0 5z',
            'tool': 'M22.7 19l-9.1-9.1c.9-2.3.4-5-1.5-6.9-2-2-5-2.4-7.4-1.3L9 6 6 9 1.6 4.7C.4 7.1.9 10.1 2.9 12.1c1.9 1.9 4.6 2.4 6.9 1.5l9.1 9.1c.4.4 1 .4 1.4 0l2.3-2.3c.5-.4.5-1.1.1-1.4z',
            'aim': 'M12 2L4 5v6c0 5.5 3.8 10.7 8 12 4.2-1.3 8-6.5 8-12V5l-8-3zm4 10H8V9h3V7l3 3-3 3v-1z',
            'shield': 'M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm-2 16l-4-4 1.4-1.4 2.6 2.6 6.6-6.6L18 9l-8 8z',
            'alert': 'M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z',
            'hash': 'M20 9h-4.5l.9-4h-2l-.9 4H9l.9-4H8l-.9 4H3v2h3.7l-1 4H2v2h3.3l-.9 4h2l.9-4H12l-.9 4h2l.9-4H19v-2h-4.7l1-4H20V9zm-6.3 6H9l1-4h4.7l-1 4z'
        };

        let metaX = x + padX;
        metaItems.forEach((mi, idx) => {
            if (idx > 0) {
                // 分隔符
                parts.push(`<circle cx="${metaX + 6}" cy="${metaY}" r="1.2" fill="#CBD5E1"/>`);
                metaX += 14;
            }
            // 图标
            const path = metaIconPaths[mi.icon] || metaIconPaths.hash;
            parts.push(`<g transform="translate(${metaX}, ${metaY - 7}) scale(${(13 / 24).toFixed(3)})">
                <path d="${path}" fill="${theme.primary}" fill-opacity="0.8"/>
            </g>`);
            metaX += 18;
            // 文本
            parts.push(`<text x="${metaX}" y="${metaY + 3}" font-size="11.5" font-weight="500" fill="#64748B">${_acEscapeXml(mi.text)}</text>`);
            metaX += mi.text.length * 6.5;  // 粗略估算
        });
    });

    parts.push(`</g>`);

    // ==================== 右侧图例 ====================
    const lx = legendX;
    const ly = graphAreaY;
    const lw = LEGEND_W - 16;
    const lh = graphAreaH;

    // 图例主卡片
    parts.push(`<rect x="${lx}" y="${ly}" width="${lw}" height="${lh}" rx="18" fill="#FFFFFF" stroke="rgba(15,23,42,0.06)" stroke-width="1"/>`);
    // 顶部彩色装饰
    parts.push(`<rect x="${lx + 16}" y="${ly}" width="${lw - 32}" height="3" rx="1.5" fill="url(#ac-brand)"/>`);

    let curY = ly + 26;

    // --- 节点类型 ---
    parts.push(`<text x="${lx + 24}" y="${curY}" font-size="10.5" font-weight="800" fill="#64748B" letter-spacing="1.5px">NODE TYPES · 节点类型</text>`);
    curY += 22;
    const typeSummary = [
        { key: 'target', count: targetNodes.length, text: '目标' },
        { key: 'action-success', count: actionNodes.filter(a => { const m = a.metadata || {}; return Array.isArray(m.findings) && m.findings.length > 0 && m.status !== 'failed_insight'; }).length, text: '行动（有发现）' },
        { key: 'action-neutral', count: actionNodes.filter(a => { const m = a.metadata || {}; const f = Array.isArray(m.findings) ? m.findings : []; return f.length === 0 || m.status === 'failed_insight'; }).length, text: '行动（其他）' },
        { key: 'vuln-critical', count: criticalCount, text: '严重漏洞' },
        { key: 'vuln-high', count: highCount, text: '高风险漏洞' },
        { key: 'vuln-med', count: medCount, text: '中风险漏洞' },
        { key: 'vuln-low', count: lowCount, text: '低风险漏洞' }
    ];
    typeSummary.forEach(item => {
        const t = typeTheme[item.key];
        if (item.count === 0) return;  // 不显示零计数项
        // 图标方块
        parts.push(`<rect x="${lx + 24}" y="${curY - 10}" width="14" height="14" rx="4" fill="${t.primary}"/>`);
        // 标签文本
        parts.push(`<text x="${lx + 46}" y="${curY + 1}" font-size="12.5" font-weight="500" fill="#334155">${_acEscapeXml(item.text)}</text>`);
        // 计数
        parts.push(`<text x="${lx + lw - 24}" y="${curY + 1}" font-size="12.5" font-weight="700" fill="#0F172A" text-anchor="end">${item.count}</text>`);
        curY += 22;
    });
    curY += 10;

    // --- 连线含义 ---
    parts.push(`<text x="${lx + 24}" y="${curY}" font-size="10.5" font-weight="800" fill="#64748B" letter-spacing="1.5px">CONNECTIONS · 连线含义</text>`);
    curY += 22;
    const lineItems = [
        { label: '行动发现漏洞', color: '#4F46E5', dash: '' },
        { label: '使能 / 促成关系', color: '#E11D48', dash: '' },
        { label: '逻辑顺序', color: '#64748B', dash: '' },
        { label: '目标定位', color: '#4F46E5', dash: '6,3' }
    ];
    lineItems.forEach(l => {
        const dashAttr = l.dash ? `stroke-dasharray="${l.dash}"` : '';
        parts.push(`<line x1="${lx + 24}" y1="${curY - 3}" x2="${lx + 62}" y2="${curY - 3}" stroke="${l.color}" stroke-width="2.4" stroke-linecap="round" ${dashAttr}/>`);
        parts.push(`<polygon points="${lx + 62},${curY - 6} ${lx + 68},${curY - 3} ${lx + 62},${curY}" fill="${l.color}"/>`);
        parts.push(`<text x="${lx + 78}" y="${curY + 1}" font-size="12.5" font-weight="500" fill="#334155">${_acEscapeXml(l.label)}</text>`);
        curY += 24;
    });
    curY += 10;

    // --- 风险等级条 ---
    parts.push(`<text x="${lx + 24}" y="${curY}" font-size="10.5" font-weight="800" fill="#64748B" letter-spacing="1.5px">RISK LEVELS · 风险等级</text>`);
    curY += 22;
    const riskBar = [
        { label: '严重', range: '80-100', color: '#E11D48' },
        { label: '高', range: '60-79', color: '#EA580C' },
        { label: '中', range: '40-59', color: '#CA8A04' },
        { label: '低', range: '0-39', color: '#0D9488' }
    ];
    riskBar.forEach(r => {
        // 风险等级胶囊
        parts.push(`<rect x="${lx + 24}" y="${curY - 10}" width="46" height="18" rx="9" fill="${r.color}"/>`);
        parts.push(`<text x="${lx + 47}" y="${curY + 2}" text-anchor="middle" font-size="10.5" font-weight="700" fill="#FFFFFF" letter-spacing="0.3px">${_acEscapeXml(r.label)}</text>`);
        // 分数范围
        parts.push(`<text x="${lx + 80}" y="${curY + 1}" font-size="12" font-weight="500" fill="#64748B">分数 ${_acEscapeXml(r.range)}</text>`);
        curY += 26;
    });

    // ==================== 底部信息栏 ====================
    const fY = OUTER_PAD + contentH - FOOTER_H;
    // 分隔线
    parts.push(`<line x1="${OUTER_PAD + 40}" y1="${fY + 16}" x2="${OUTER_PAD + contentW - 40}" y2="${fY + 16}" stroke="rgba(15,23,42,0.06)" stroke-width="1"/>`);
    // 左侧品牌
    parts.push(`<circle cx="${OUTER_PAD + 44}" cy="${fY + 34}" r="5" fill="url(#ac-brand)"/>`);
    parts.push(`<text x="${OUTER_PAD + 56}" y="${fY + 38}" font-size="11.5" font-weight="600" fill="#64748B">CyberStrikeAI <tspan fill="#94A3B8" font-weight="500">· Attack Chain Visualization Report</tspan></text>`);
    // 右侧时间戳
    parts.push(`<text x="${OUTER_PAD + contentW - 40}" y="${fY + 38}" font-size="11.5" font-weight="500" fill="#94A3B8" text-anchor="end">${_acEscapeXml(ts)}</text>`);

    parts.push(`</svg>`);
    return parts.join('\n');
}

// 下载文本文件
function _acDownloadBlob(blob, filename) {
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 150);
}

// 基于 SVG 字符串生成高清 PNG
function _acSvgToPng(svgString, scale) {
    return new Promise((resolve, reject) => {
        try {
            // 读取 SVG 尺寸
            const m = svgString.match(/<svg[^>]*width="(\d+(?:\.\d+)?)"[^>]*height="(\d+(?:\.\d+)?)"/i);
            const w = m ? parseFloat(m[1]) : 1600;
            const h = m ? parseFloat(m[2]) : 900;
            const s = scale || Math.min(2.5, Math.max(1.5, 2000 / Math.max(w, h)));

            const blob = new Blob([svgString], { type: 'image/svg+xml;charset=utf-8' });
            const url = URL.createObjectURL(blob);
            const img = new Image();
            img.onload = function () {
                try {
                    const canvas = document.createElement('canvas');
                    canvas.width = Math.round(w * s);
                    canvas.height = Math.round(h * s);
                    const ctx = canvas.getContext('2d');
                    ctx.imageSmoothingEnabled = true;
                    ctx.imageSmoothingQuality = 'high';
                    ctx.fillStyle = '#ffffff';
                    ctx.fillRect(0, 0, canvas.width, canvas.height);
                    ctx.drawImage(img, 0, 0, canvas.width, canvas.height);
                    URL.revokeObjectURL(url);
                    canvas.toBlob(pngBlob => {
                        if (!pngBlob) reject(new Error('PNG 生成失败'));
                        else resolve(pngBlob);
                    }, 'image/png', 0.95);
                } catch (err) {
                    URL.revokeObjectURL(url);
                    reject(err);
                }
            };
            img.onerror = function (e) {
                URL.revokeObjectURL(url);
                reject(new Error('SVG 加载失败'));
            };
            img.src = url;
        } catch (e) {
            reject(e);
        }
    });
}

// 导出攻击链（美化版）
function exportAttackChain(format) {
    if (!attackChainCytoscape) {
        alert(typeof window.t === 'function' ? window.t('chat.pleaseLoadAttackChainFirst', {}, '请先加载攻击链') : '请先加载攻击链');
        return;
    }

    // 延时确保渲染完成
    setTimeout(() => {
        try {
            const svgString = _acBuildSvgString();
            const convId = currentAttackChainConversationId || 'export';
            const tsName = Date.now();

            if (format === 'svg') {
                const blob = new Blob([svgString], { type: 'image/svg+xml;charset=utf-8' });
                _acDownloadBlob(blob, `attack-chain-${convId}-${tsName}.svg`);
            } else if (format === 'png') {
                _acSvgToPng(svgString, 2)
                    .then(pngBlob => _acDownloadBlob(pngBlob, `attack-chain-${convId}-${tsName}.png`))
                    .catch(err => {
                        logger.error('导出 PNG 失败，回退到 Cytoscape 原生导出:', err);
                        // 回退方案：使用 Cytoscape 自带导出
                        try {
                            const p = attackChainCytoscape.png({ output: 'blob', bg: '#ffffff', full: true, scale: 2 });
                            if (p && typeof p.then === 'function') {
                                p.then(b => _acDownloadBlob(b, `attack-chain-${convId}-${tsName}.png`))
                                    .catch(e => {
                                        const m = '导出 PNG 失败: ' + (e && e.message || e);
                                        if (typeof window.showChatToast === 'function') window.showChatToast(m, 'error');
                                        else if (typeof window.showToast === 'function') window.showToast(m, 'error');
                                        else alert(m);
                                    });
                            } else if (p) {
                                _acDownloadBlob(p, `attack-chain-${convId}-${tsName}.png`);
                            } else {
                                const m = '导出 PNG 失败';
                                if (typeof window.showChatToast === 'function') window.showChatToast(m, 'error');
                                else if (typeof window.showToast === 'function') window.showToast(m, 'error');
                                else alert(m);
                            }
                        } catch (e2) {
                            const m = '导出 PNG 失败: ' + (e2 && e2.message || e2);
                            if (typeof window.showChatToast === 'function') window.showChatToast(m, 'error');
                            else if (typeof window.showToast === 'function') window.showToast(m, 'error');
                            else alert(m);
                        }
                    });
            } else {
                const m = '不支持的导出格式: ' + format;
                if (typeof window.showChatToast === 'function') window.showChatToast(m, 'error');
                else if (typeof window.showToast === 'function') window.showToast(m, 'error');
                else alert(m);
            }
        } catch (error) {
            logger.error('导出失败:', error);
            const m = '导出失败: ' + (error && error.message || '未知错误');
            if (typeof window.showChatToast === 'function') window.showChatToast(m, 'error');
            else if (typeof window.showToast === 'function') window.showToast(m, 'error');
            else alert(m);
        }
    }, 80);
}

// ============================================
// 对话批量管理功能
// ============================================

let contextMenuConversationId = null;
let contextMenuConversationTitle = '';
let conversationsListLoadSeq = 0; // 对话列表加载序号，避免并发请求导致重复渲染
let conversationsListNavigateGen = 0; // 用户主动翻页代数，防止后台刷新覆盖翻页结果
const CONVERSATIONS_PAGE_SIZE_KEY = 'cyberstrike.conversations_page_size';
const CONVERSATIONS_SORT_KEY = 'cyberstrike.conversations_sort_by';
const CONVERSATIONS_PROJECT_FILTER_KEY = 'cyberstrike.conversations_project_filter';
const CONVERSATION_PROJECT_FILTER_NONE = '__none__';
const CONVERSATION_PROJECT_FILTER_SELECT_ID = 'conversation-project-filter';
const CONVERSATION_PROJECT_FILTER_CARET = '<svg class="conversation-project-filter-caret" width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M6 9l6 6 6-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>';
const BATCH_PROJECT_FILTER_SELECT_ID = 'batch-project-filter';
const projectFilterCustomSelectRegistry = {};
let projectFilterCustomSelectDocBound = false;

function projectFilterT(key, fallback) {
    if (typeof window.t === 'function') {
        const value = window.t(key);
        if (value && value !== key) return value;
    }
    return fallback;
}

function closeProjectFilterCustomSelect(selectId) {
    const reg = projectFilterCustomSelectRegistry[selectId];
    if (!reg || !reg.wrapper) return;
    reg.wrapper.classList.remove('open');
    if (reg.trigger) reg.trigger.setAttribute('aria-expanded', 'false');
    if (reg.filterSearchTimer) {
        clearTimeout(reg.filterSearchTimer);
        reg.filterSearchTimer = null;
    }
    reg.filterSearchSeq = (reg.filterSearchSeq || 0) + 1;
    if (reg.searchInput) reg.searchInput.value = '';
}

function closeAllProjectFilterCustomSelects() {
    Object.keys(projectFilterCustomSelectRegistry).forEach(closeProjectFilterCustomSelect);
}

function ensureProjectFilterSearchUi(reg) {
    if (reg.searchInput && reg.optionsList) return;
    const { dropdown } = reg;
    dropdown.innerHTML = '';

    const searchWrap = document.createElement('div');
    searchWrap.className = 'conversation-project-filter-search';
    const searchInput = document.createElement('input');
    searchInput.type = 'search';
    searchInput.className = 'conversation-project-filter-search-input';
    searchInput.setAttribute('autocomplete', 'off');
    searchInput.setAttribute('data-i18n', 'chat.filterProjectSearch');
    searchInput.setAttribute('data-i18n-attr', 'placeholder');
    searchInput.placeholder = projectFilterT('chat.filterProjectSearch', '搜索项目…');
    searchWrap.appendChild(searchInput);
    dropdown.appendChild(searchWrap);
    reg.searchInput = searchInput;

    const optionsList = document.createElement('div');
    optionsList.className = 'conversation-project-filter-options';
    dropdown.appendChild(optionsList);
    reg.optionsList = optionsList;
    reg.filterSearchSeq = 0;
    reg.filterSearchTimer = null;

    searchInput.addEventListener('input', () => loadProjectFilterLocalOptions(reg.select.id));
    searchInput.addEventListener('click', (e) => e.stopPropagation());
    searchInput.addEventListener('keydown', (e) => {
        e.stopPropagation();
        if (e.key === 'Escape') closeProjectFilterCustomSelect(reg.select.id);
    });
}

function createProjectFilterOptionButton(value, label, selectedValue) {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'conversation-project-filter-option';
    item.setAttribute('role', 'option');
    item.setAttribute('data-value', value);
    item.title = label;
    if (value === selectedValue) {
        item.classList.add('is-selected');
        item.setAttribute('aria-selected', 'true');
    } else {
        item.setAttribute('aria-selected', 'false');
    }
    const check = document.createElement('span');
    check.className = 'conversation-project-filter-check';
    check.setAttribute('aria-hidden', 'true');
    check.textContent = '✓';
    const labelEl = document.createElement('span');
    labelEl.className = 'conversation-project-filter-option-label';
    labelEl.textContent = label;
    labelEl.title = label;
    item.appendChild(check);
    item.appendChild(labelEl);
    return item;
}

function appendProjectFilterStatusMessage(optionsList, className, text) {
    const el = document.createElement('div');
    el.className = className;
    el.textContent = text;
    optionsList.appendChild(el);
    return el;
}

function renderProjectFilterPinnedOptions(reg) {
    const { select, optionsList } = reg;
    optionsList.innerHTML = '';
    Array.prototype.forEach.call(select.options, (opt) => {
        if (opt.value === '' || opt.value === CONVERSATION_PROJECT_FILTER_NONE) {
            optionsList.appendChild(createProjectFilterOptionButton(opt.value, opt.textContent || '', select.value));
        }
    });
}

function ensureNativeProjectFilterOption(select, projectId, label) {
    if (!projectId || projectId === CONVERSATION_PROJECT_FILTER_NONE) return;
    if (Array.prototype.some.call(select.options, (opt) => opt.value === projectId)) return;
    const opt = document.createElement('option');
    opt.value = projectId;
    opt.textContent = label || projectId;
    select.appendChild(opt);
}

async function loadProjectFilterLocalOptions(selectId) {
    const reg = projectFilterCustomSelectRegistry[selectId];
    if (!reg || !reg.optionsList) return;
    const query = (reg.searchInput?.value || '').trim();
    const seq = ++reg.filterSearchSeq;

    const needsFetch = typeof window.isProjectsCacheReady === 'function' && !window.isProjectsCacheReady();
    let loadingEl = null;
    if (needsFetch) {
        renderProjectFilterPinnedOptions(reg);
        loadingEl = appendProjectFilterStatusMessage(
            reg.optionsList,
            'conversation-project-filter-status',
            projectFilterT('common.loading', '加载中…')
        );
    }

    try {
        const ensureLoaded = typeof window.ensureProjectsLoaded === 'function'
            ? window.ensureProjectsLoaded
            : null;
        const filterLocal = typeof window.filterActiveProjectsLocal === 'function'
            ? window.filterActiveProjectsLocal
            : null;
        if (!ensureLoaded || !filterLocal) throw new Error('projects cache unavailable');

        const all = await ensureLoaded();
        if (seq !== reg.filterSearchSeq) return;

        renderProjectFilterPinnedOptions(reg);
        const selected = reg.select.value;
        const pinnedValues = new Set(['', CONVERSATION_PROJECT_FILTER_NONE]);
        const projects = filterLocal(all, query);
        projects.forEach((p) => {
            if (pinnedValues.has(p.id)) return;
            reg.optionsList.appendChild(
                createProjectFilterOptionButton(p.id, p.name || p.id, selected)
            );
        });

        if (query && projects.length === 0) {
            appendProjectFilterStatusMessage(
                reg.optionsList,
                'conversation-project-filter-empty',
                projectFilterT('chat.filterProjectSearchEmpty', '没有匹配的项目')
            );
        }
    } catch (e) {
        if (seq !== reg.filterSearchSeq) return;
        renderProjectFilterPinnedOptions(reg);
        appendProjectFilterStatusMessage(
            reg.optionsList,
            'conversation-project-filter-empty',
            projectFilterT('chat.filterProjectSearchFailed', '加载项目失败，请重试')
        );
    } finally {
        if (loadingEl && loadingEl.parentNode) loadingEl.remove();
    }
}

function syncProjectFilterCustomSelect(selectId) {
    const reg = projectFilterCustomSelectRegistry[selectId];
    if (!reg) return;
    ensureProjectFilterSearchUi(reg);
    const { select, trigger } = reg;
    const valueSpan = trigger.querySelector('.conversation-project-filter-value');
    const selectedOpt = select.options[select.selectedIndex];
    const selectedText = selectedOpt ? (selectedOpt.textContent || '') : '';
    if (valueSpan) {
        valueSpan.textContent = selectedText;
        valueSpan.title = selectedText;
    }
}

function ensureSimpleCustomSelectOptionsUi(reg) {
    if (reg.optionsList) return;
    reg.dropdown.innerHTML = '';
    const optionsList = document.createElement('div');
    optionsList.className = 'conversation-project-filter-options';
    reg.dropdown.appendChild(optionsList);
    reg.optionsList = optionsList;
}

function renderSimpleCustomSelectOptions(reg) {
    ensureSimpleCustomSelectOptionsUi(reg);
    const { select, optionsList } = reg;
    optionsList.innerHTML = '';
    Array.prototype.forEach.call(select.options, (opt) => {
        optionsList.appendChild(createProjectFilterOptionButton(opt.value, opt.textContent || '', select.value));
    });
}

function syncSimpleCustomSelect(selectId) {
    const reg = projectFilterCustomSelectRegistry[selectId];
    if (!reg) return;
    const { select, trigger } = reg;
    const valueSpan = trigger.querySelector('.conversation-project-filter-value');
    const selectedOpt = select.options[select.selectedIndex];
    const selectedText = selectedOpt ? (selectedOpt.textContent || '') : '';
    if (valueSpan) {
        valueSpan.textContent = selectedText;
        valueSpan.title = selectedText;
    }
}

function initSimpleCustomSelect(selectId) {
    const select = document.getElementById(selectId);
    if (!select) return;
    if (select.dataset.projectCustomSelect === '1') {
        syncSimpleCustomSelect(selectId);
        return;
    }
    select.dataset.projectCustomSelect = '1';
    select.classList.add('conversation-project-filter-native');
    select.tabIndex = -1;
    select.setAttribute('aria-hidden', 'true');

    const wrapper = document.createElement('div');
    wrapper.className = 'conversation-project-filter-ui';

    const trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = 'conversation-project-filter-trigger';
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    const valueSpan = document.createElement('span');
    valueSpan.className = 'conversation-project-filter-value';
    trigger.appendChild(valueSpan);
    trigger.insertAdjacentHTML('beforeend', CONVERSATION_PROJECT_FILTER_CARET);

    const dropdown = document.createElement('div');
    dropdown.className = 'conversation-project-filter-dropdown';
    dropdown.setAttribute('role', 'listbox');

    const parent = select.parentNode;
    parent.insertBefore(wrapper, select);
    wrapper.appendChild(trigger);
    wrapper.appendChild(dropdown);
    wrapper.appendChild(select);

    projectFilterCustomSelectRegistry[selectId] = { wrapper, trigger, dropdown, select };

    trigger.addEventListener('click', (e) => {
        e.stopPropagation();
        const open = wrapper.classList.contains('open');
        closeAllProjectFilterCustomSelects();
        if (!open) {
            wrapper.classList.add('open');
            trigger.setAttribute('aria-expanded', 'true');
            renderSimpleCustomSelectOptions(projectFilterCustomSelectRegistry[selectId]);
        }
    });

    dropdown.addEventListener('click', (e) => {
        const opt = e.target.closest('.conversation-project-filter-option');
        if (!opt) return;
        e.stopPropagation();
        const val = opt.getAttribute('data-value');
        if (val === null) return;
        if (select.value !== val) {
            select.value = val;
            select.dispatchEvent(new Event('change', { bubbles: true }));
        }
        closeProjectFilterCustomSelect(selectId);
        syncSimpleCustomSelect(selectId);
    });

    if (!projectFilterCustomSelectDocBound) {
        projectFilterCustomSelectDocBound = true;
        document.addEventListener('click', closeAllProjectFilterCustomSelects);
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') closeAllProjectFilterCustomSelects();
        });
    }
    syncSimpleCustomSelect(selectId);
}

function initProjectFilterCustomSelect(selectId) {
    const select = document.getElementById(selectId);
    if (!select) return;
    if (select.dataset.projectCustomSelect === '1') {
        syncProjectFilterCustomSelect(selectId);
        return;
    }
    select.dataset.projectCustomSelect = '1';
    select.classList.add('conversation-project-filter-native');
    select.tabIndex = -1;
    select.setAttribute('aria-hidden', 'true');

    const wrapper = document.createElement('div');
    wrapper.className = 'conversation-project-filter-ui';

    const trigger = document.createElement('button');
    trigger.type = 'button';
    trigger.className = 'conversation-project-filter-trigger';
    trigger.setAttribute('aria-haspopup', 'listbox');
    trigger.setAttribute('aria-expanded', 'false');
    const valueSpan = document.createElement('span');
    valueSpan.className = 'conversation-project-filter-value';
    trigger.appendChild(valueSpan);
    trigger.insertAdjacentHTML('beforeend', CONVERSATION_PROJECT_FILTER_CARET);

    const dropdown = document.createElement('div');
    dropdown.className = 'conversation-project-filter-dropdown';
    dropdown.setAttribute('role', 'listbox');

    const parent = select.parentNode;
    parent.insertBefore(wrapper, select);
    wrapper.appendChild(trigger);
    wrapper.appendChild(dropdown);
    wrapper.appendChild(select);

    projectFilterCustomSelectRegistry[selectId] = { wrapper, trigger, dropdown, select };

    trigger.addEventListener('click', (e) => {
        e.stopPropagation();
        const open = wrapper.classList.contains('open');
        closeAllProjectFilterCustomSelects();
        if (!open) {
            wrapper.classList.add('open');
            trigger.setAttribute('aria-expanded', 'true');
            ensureProjectFilterSearchUi(projectFilterCustomSelectRegistry[selectId]);
            const reg = projectFilterCustomSelectRegistry[selectId];
            if (reg?.searchInput) {
                reg.searchInput.value = '';
                loadProjectFilterLocalOptions(selectId);
                requestAnimationFrame(() => reg.searchInput.focus());
            }
        }
    });

    dropdown.addEventListener('click', (e) => {
        const opt = e.target.closest('.conversation-project-filter-option');
        if (!opt) return;
        e.stopPropagation();
        const val = opt.getAttribute('data-value');
        if (val === null) return;
        const label = opt.querySelector('.conversation-project-filter-option-label')?.textContent || val;
        ensureNativeProjectFilterOption(select, val, label);
        if (select.value !== val) {
            select.value = val;
            select.dispatchEvent(new Event('change', { bubbles: true }));
        }
        closeProjectFilterCustomSelect(selectId);
        syncProjectFilterCustomSelect(selectId);
    });

    if (!projectFilterCustomSelectDocBound) {
        projectFilterCustomSelectDocBound = true;
        document.addEventListener('click', closeAllProjectFilterCustomSelects);
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') closeAllProjectFilterCustomSelects();
        });
    }
    syncProjectFilterCustomSelect(selectId);
}

function syncConversationProjectCustomSelect() {
    syncProjectFilterCustomSelect(CONVERSATION_PROJECT_FILTER_SELECT_ID);
}

function initConversationProjectCustomSelect() {
    initProjectFilterCustomSelect(CONVERSATION_PROJECT_FILTER_SELECT_ID);
}

function getConversationProjectFilter() {
    try {
        return localStorage.getItem(CONVERSATIONS_PROJECT_FILTER_KEY) || '';
    } catch (e) {
        return '';
    }
}

function setConversationProjectFilter(projectId) {
    const value = (projectId || '').trim();
    try {
        if (value) localStorage.setItem(CONVERSATIONS_PROJECT_FILTER_KEY, value);
        else localStorage.removeItem(CONVERSATIONS_PROJECT_FILTER_KEY);
    } catch (e) { /* ignore */ }
    const sel = document.getElementById('conversation-project-filter');
    if (sel && sel.value !== value) sel.value = value;
    syncConversationProjectCustomSelect();
    updateConversationSidebarFilterUI();
}

function appendProjectFilterPinnedNativeOptions(sel) {
    const tFn = typeof window.t === 'function' ? window.t.bind(window) : null;
    const allLabel = tFn ? tFn('chat.filterAllProjects') : '全部项目';
    const unboundLabel = tFn ? tFn('chat.filterUnboundProjects') : '未绑定项目';
    sel.innerHTML = '';
    const allOpt = document.createElement('option');
    allOpt.value = '';
    allOpt.textContent = allLabel;
    allOpt.setAttribute('data-i18n', 'chat.filterAllProjects');
    sel.appendChild(allOpt);
    const unboundOpt = document.createElement('option');
    unboundOpt.value = CONVERSATION_PROJECT_FILTER_NONE;
    unboundOpt.textContent = unboundLabel;
    unboundOpt.setAttribute('data-i18n', 'chat.filterUnboundProjects');
    sel.appendChild(unboundOpt);
}

async function resolveProjectFilterSelection(projectId) {
    const saved = (projectId || '').trim();
    if (!saved || saved === CONVERSATION_PROJECT_FILTER_NONE) return saved;
    const fetchSummary = typeof window.fetchProjectSummary === 'function'
        ? window.fetchProjectSummary
        : null;
    if (!fetchSummary) return saved;
    const project = await fetchSummary(saved);
    if (!project || !project.id || project.status === 'archived') return '';
    return project.id;
}

async function appendSelectedProjectFilterOption(sel, projectId) {
    const id = (projectId || '').trim();
    if (!id || id === CONVERSATION_PROJECT_FILTER_NONE) return;
    if (Array.prototype.some.call(sel.options, (opt) => opt.value === id)) return;
    const fetchSummary = typeof window.fetchProjectSummary === 'function'
        ? window.fetchProjectSummary
        : null;
    const project = fetchSummary ? await fetchSummary(id) : null;
    const label = (project && (project.name || project.id)) || (window.projectNameById && window.projectNameById[id]) || id;
    const opt = document.createElement('option');
    opt.value = id;
    opt.textContent = label;
    sel.appendChild(opt);
}

async function refreshConversationProjectFilter() {
    const sel = document.getElementById('conversation-project-filter');
    if (!sel) return;
    const saved = getConversationProjectFilter();
    appendProjectFilterPinnedNativeOptions(sel);
    const normalized = await resolveProjectFilterSelection(saved);
    if (normalized && normalized !== CONVERSATION_PROJECT_FILTER_NONE) {
        await appendSelectedProjectFilterOption(sel, normalized);
    }
    if (normalized !== saved) setConversationProjectFilter(normalized);
    sel.value = normalized;
    syncConversationProjectCustomSelect();
    updateConversationSidebarFilterUI();
}

function onConversationProjectFilterChange(projectId) {
    setConversationProjectFilter(projectId || '');
    commitConversationsPage(1, { bumpNavigateGen: true });
    loadConversations(conversationsSearchQuery);
}

function updateConversationSidebarFilterUI() {
    const titleEl = document.querySelector('.recent-conversations-section .section-title');
    const filter = getConversationProjectFilter();
    const hasSearch = !!(conversationsSearchQuery && conversationsSearchQuery.trim());
    if (!titleEl) return;
    const tFn = typeof window.t === 'function' ? window.t.bind(window) : null;
    if (filter && filter !== CONVERSATION_PROJECT_FILTER_NONE) {
        const name = (window.projectNameById && window.projectNameById[filter]) || filter;
        const fullTitle = tFn ? tFn('chat.projectConversationsTitle', { name }) : `${name} · 对话`;
        titleEl.textContent = fullTitle;
        titleEl.title = fullTitle;
        titleEl.classList.add('section-title--filtered');
        titleEl.removeAttribute('data-i18n');
    } else if (filter === CONVERSATION_PROJECT_FILTER_NONE) {
        const fullTitle = tFn ? tFn('chat.unboundConversationsTitle') : '未绑定项目';
        titleEl.textContent = fullTitle;
        titleEl.title = fullTitle;
        titleEl.classList.add('section-title--filtered');
        titleEl.setAttribute('data-i18n', 'chat.unboundConversationsTitle');
    } else {
        titleEl.classList.remove('section-title--filtered');
        titleEl.removeAttribute('title');
        titleEl.setAttribute('data-i18n', 'chat.recentConversations');
        if (tFn) titleEl.textContent = tFn('chat.recentConversations');
    }
}

window.onConversationProjectBindingChanged = function onConversationProjectBindingChanged() {
    loadConversations(conversationsSearchQuery);
};

function getConversationSortBy() {
    try {
        const saved = localStorage.getItem(CONVERSATIONS_SORT_KEY);
        if (saved === 'created_at' || saved === 'updated_at') return saved;
    } catch (e) { /* ignore */ }
    return 'updated_at';
}

let conversationSortBy = getConversationSortBy();

function getConversationSortTime(conv) {
    const field = conversationSortBy === 'created_at' ? 'createdAt' : 'updatedAt';
    const raw = conv && conv[field];
    if (!raw) return new Date(0);
    const date = new Date(raw);
    return isNaN(date.getTime()) ? new Date(0) : date;
}

function updateConversationSortMenuUI() {
    const menu = document.getElementById('conversation-sort-menu');
    const btn = document.getElementById('conversation-sort-btn');
    if (!menu) return;
    menu.querySelectorAll('.conversation-sort-option').forEach((option) => {
        const selected = option.dataset.sort === conversationSortBy;
        option.classList.toggle('is-selected', selected);
        option.setAttribute('aria-checked', selected ? 'true' : 'false');
    });
    if (btn) {
        btn.setAttribute('aria-expanded', menu.hidden ? 'false' : 'true');
    }
}

function closeConversationSortMenu() {
    const menu = document.getElementById('conversation-sort-menu');
    const btn = document.getElementById('conversation-sort-btn');
    if (menu) menu.hidden = true;
    if (btn) btn.setAttribute('aria-expanded', 'false');
}

function toggleConversationSortMenu(event) {
    if (event) {
        event.preventDefault();
        event.stopPropagation();
    }
    const menu = document.getElementById('conversation-sort-menu');
    const btn = document.getElementById('conversation-sort-btn');
    if (!menu || !btn) return;
    const willOpen = menu.hidden;
    closeConversationSortMenu();
    if (willOpen) {
        menu.hidden = false;
        btn.setAttribute('aria-expanded', 'true');
        updateConversationSortMenuUI();
    }
}

function setConversationSortBy(sortBy) {
    const next = sortBy === 'created_at' ? 'created_at' : 'updated_at';
    if (next === conversationSortBy) {
        closeConversationSortMenu();
        return;
    }
    conversationSortBy = next;
    try {
        localStorage.setItem(CONVERSATIONS_SORT_KEY, next);
    } catch (e) { /* ignore */ }
    updateConversationSortMenuUI();
    closeConversationSortMenu();
    commitConversationsPage(1, { bumpNavigateGen: true });
    loadConversations(conversationsSearchQuery);
}

if (!window.__conversationSortMenuBound) {
    window.__conversationSortMenuBound = true;
    document.addEventListener('click', (event) => {
        const dropdown = document.getElementById('conversation-sort-dropdown');
        if (!dropdown || dropdown.contains(event.target)) return;
        closeConversationSortMenu();
    });
    document.addEventListener('keydown', (event) => {
        if (event.key === 'Escape') closeConversationSortMenu();
    });
}

window.toggleConversationSortMenu = toggleConversationSortMenu;
window.setConversationSortBy = setConversationSortBy;
window.closeConversationSortMenu = closeConversationSortMenu;

function getConversationsPageSize() {
    try {
        const saved = parseInt(localStorage.getItem(CONVERSATIONS_PAGE_SIZE_KEY), 10);
        if ([20, 50, 100].includes(saved)) return saved;
    } catch (e) { /* ignore */ }
    return 50;
}

let conversationsPagination = {
    page: 1,
    pageSize: getConversationsPageSize(),
    total: 0,
    visibleCount: 0,
};
let conversationsSearchQuery = '';
let conversationsPaginationEventsBound = false;

function getConversationsTotalPages() {
    const { total, pageSize } = conversationsPagination;
    return Math.max(1, Math.ceil((total || 0) / pageSize) || 1);
}

/**
 * 分页状态约定：
 * - conversationsPagination.page 仅在此处（用户操作 / reconcile 钳制 / clamp）写入
 * - loadConversations 只读页码，用 intentPage 或当前 page 计算 offset
 * - isStaleConversationListLoad 丢弃页码或 navigateGen 已变的在途请求
 */
function commitConversationsPage(page, { bumpNavigateGen = false } = {}) {
    const next = Math.max(1, parseInt(page, 10) || 1);
    if (bumpNavigateGen) {
        conversationsListNavigateGen += 1;
    }
    conversationsPagination.page = next;
    return next;
}

function isStaleConversationListLoad(loadSeq, intentPage, navigateGenAtStart, activePage) {
    if (loadSeq !== conversationsListLoadSeq) return true;
    // 后台刷新期间用户已翻页（含 2→1、1→2），丢弃过期结果
    if (intentPage == null && navigateGenAtStart !== conversationsListNavigateGen) return true;
    // 用户主动翻页后，丢弃目标页已变化的请求
    if (intentPage != null && intentPage !== conversationsPagination.page) return true;
    // 后台刷新完成时页码已变（如 reconcile 钳制），丢弃过期结果
    if (intentPage == null && activePage != null && activePage !== conversationsPagination.page) return true;
    return false;
}

function reconcileConversationsPageAfterTotal(activePage, intentPage, parsed, pageSize, offset, resolvedTotal) {
    let total = resolvedTotal;
    const totalPages = () => Math.max(1, Math.ceil((total || 0) / pageSize) || 1);

    if (activePage <= totalPages()) {
        return { ok: true, total };
    }

    const serverTotal = parseListTotalValue(parsed.total, parsed.items.length);
    const hasPageData = parsed.items.length > 0;
    const knownTotal = conversationsPagination.total || 0;
    // 用户主动翻页且服务端确有该页数据时，不信过期/偏低的 total（避免 2>1 被钳回第 1 页）
    if (intentPage != null && (hasPageData || serverTotal > offset || total > offset || knownTotal > offset)) {
        total = Math.max(total, serverTotal, knownTotal, offset + parsed.items.length);
        if (activePage <= totalPages()) {
            return { ok: true, total };
        }
    }

    const clampedPage = totalPages();
    commitConversationsPage(clampedPage);
    return { ok: false, total, clampedPage };
}

function clampConversationsPageToTotal() {
    const totalPages = getConversationsTotalPages();
    if (conversationsPagination.page > totalPages) {
        commitConversationsPage(totalPages);
        return true;
    }
    if (conversationsPagination.page < 1) {
        commitConversationsPage(1);
        return true;
    }
    return false;
}

let conversationsPaginationRenderLock = false;

function initConversationsPaginationEvents() {
    if (conversationsPaginationEventsBound) return;
    const el = document.getElementById('conversations-pagination');
    if (!el) return;
    conversationsPaginationEventsBound = true;
    el.addEventListener('click', (e) => {
        const btn = e.target.closest('[data-conv-page]');
        if (!btn || btn.disabled) return;
        e.preventDefault();
        const page = parseInt(btn.getAttribute('data-conv-page'), 10);
        if (Number.isFinite(page)) {
            goConversationsPage(page);
        }
    });
    el.addEventListener('change', (e) => {
        if (conversationsPaginationRenderLock) return;
        if (e.target && e.target.id === 'conversations-page-size-pagination') {
            changeConversationsPageSize();
        }
    });
}

function parseListTotalValue(raw, itemsLength) {
    if (typeof raw === 'number' && Number.isFinite(raw) && raw >= 0) return raw;
    if (raw != null && raw !== '') {
        const n = parseInt(String(raw), 10);
        if (Number.isFinite(n) && n >= 0) return n;
    }
    return itemsLength;
}

function parseListOffsetValue(raw) {
    if (typeof raw === 'number' && Number.isFinite(raw) && raw >= 0) return raw;
    if (raw != null && raw !== '') {
        const n = parseInt(String(raw), 10);
        if (Number.isFinite(n) && n >= 0) return n;
    }
    return 0;
}

function parseConversationsListResponse(data) {
    if (Array.isArray(data)) {
        return { items: data, total: data.length, limit: data.length, offset: 0, isLegacyArray: true };
    }
    const items = data.conversations || data.items || [];
    const arr = Array.isArray(items) ? items : [];
    return {
        items: arr,
        total: parseListTotalValue(data.total, arr.length),
        limit: parseListTotalValue(data.limit, arr.length) || arr.length,
        offset: parseListOffsetValue(data.offset),
        isLegacyArray: false,
    };
}

async function resolveConversationsListTotal(params, parsed, pageSize, offset) {
    const serverTotal = parsed.total;
    if (!parsed.isLegacyArray && typeof serverTotal === 'number' && Number.isFinite(serverTotal) && serverTotal >= 0) {
        return serverTotal;
    }
    if (!parsed.isLegacyArray && serverTotal > offset + parsed.items.length) {
        return serverTotal;
    }
    if (parsed.items.length < pageSize) {
        return Math.max(serverTotal, offset + parsed.items.length);
    }
    const probe = new URLSearchParams(params);
    probe.set('offset', String(offset + pageSize));
    probe.set('limit', '1');
    try {
        const res = await apiFetch(`/api/conversations?${probe}`);
        if (!res.ok) return Math.max(serverTotal, offset + parsed.items.length);
        const probeParsed = parseConversationsListResponse(await res.json());
        if (probeParsed.total > serverTotal) return probeParsed.total;
        if (probeParsed.items.length > 0) {
            return Math.max(serverTotal, offset + pageSize + 1);
        }
    } catch (e) { /* ignore */ }
    return Math.max(serverTotal, offset + parsed.items.length);
}

async function fetchAllConversations(searchQuery) {
    let all = [];
    const pageSize = 200;
    let offset = 0;
    let total = Infinity;
    const search = (searchQuery || '').trim();
    while (all.length < total) {
        const params = new URLSearchParams({ limit: String(pageSize), offset: String(offset) });
        if (search) params.set('search', search);
        const res = await apiFetch(`/api/conversations?${params}`);
        if (!res.ok) throw new Error('load conversations failed');
        const parsed = parseConversationsListResponse(await res.json());
        all = all.concat(parsed.items);
        total = parsed.total;
        if (!parsed.items.length) break;
        offset += parsed.items.length;
    }
    return all;
}

function getConversationListEmptyHtml() {
    const filter = getConversationProjectFilter();
    if (filter && filter !== CONVERSATION_PROJECT_FILTER_NONE) {
        return '<div class="conversations-list-empty" data-i18n="chat.noProjectConversations"></div>';
    }
    if (filter === CONVERSATION_PROJECT_FILTER_NONE) {
        return '<div class="conversations-list-empty" data-i18n="chat.noUnboundConversations"></div>';
    }
    return '<div class="conversations-list-empty" data-i18n="chat.noHistoryConversations"></div>';
}

function renderConversationsPagination(visibleCount) {
    const el = document.getElementById('conversations-pagination');
    if (!el) return;
    const { page, pageSize, total } = conversationsPagination;
    if (typeof visibleCount === 'number') {
        conversationsPagination.visibleCount = visibleCount;
    }

    if (!total) {
        el.innerHTML = '';
        el.hidden = true;
        return;
    }

    const totalPages = getConversationsTotalPages();
    const navDisabled = totalPages <= 1;
    const recentToggle = document.getElementById('recent-conversations-toggle');
    el.hidden = !recentToggle || recentToggle.getAttribute('aria-expanded') !== 'true';
    const start = total === 0 ? 0 : (page - 1) * pageSize + 1;
    const end = Math.min(page * pageSize, total);
    const tFn = typeof window.t === 'function' ? window.t.bind(window) : null;
    const infoText = tFn
        ? tFn('chat.paginationRange', { start, end, total })
        : `${start}-${end}/${total}`;
    const pageText = tFn
        ? tFn('chat.paginationPage', { page, total: totalPages })
        : `${page}/${totalPages}`;
    const perPageLabel = tFn ? tFn('chat.paginationPerPage') : 'Per page';
    const prevLabel = tFn ? tFn('chat.paginationPrev') : 'Prev';
    const nextLabel = tFn ? tFn('chat.paginationNext') : 'Next';
    const prevPage = page - 1;
    const nextPage = page + 1;
    conversationsPaginationRenderLock = true;
    try {
        el.innerHTML = `
        <div class="sidebar-list-pagination-inner sidebar-list-pagination-inner--compact">
            <span class="pagination-info">${escapeHtml(infoText)}</span>
            <div class="pagination-controls">
                <button type="button" class="btn-icon-pagination" data-conv-page="${prevPage}" ${page <= 1 || navDisabled ? 'disabled' : ''} title="${escapeHtml(prevLabel)}" aria-label="${escapeHtml(prevLabel)}">‹</button>
                <span class="pagination-page">${escapeHtml(pageText)}</span>
                <button type="button" class="btn-icon-pagination" data-conv-page="${nextPage}" ${page >= totalPages || navDisabled ? 'disabled' : ''} title="${escapeHtml(nextLabel)}" aria-label="${escapeHtml(nextLabel)}">›</button>
            </div>
            <label class="pagination-page-size">
                ${escapeHtml(perPageLabel)}
                <select id="conversations-page-size-pagination">
                    <option value="20" ${pageSize === 20 ? 'selected' : ''}>20</option>
                    <option value="50" ${pageSize === 50 ? 'selected' : ''}>50</option>
                    <option value="100" ${pageSize === 100 ? 'selected' : ''}>100</option>
                </select>
            </label>
        </div>`;
    } finally {
        conversationsPaginationRenderLock = false;
    }
}

function goConversationsPage(page) {
    const requestedPage = Math.max(1, parseInt(page, 10) || 1);
    const scrollToTop = requestedPage !== conversationsPagination.page;
    commitConversationsPage(requestedPage, { bumpNavigateGen: true });
    loadConversations(conversationsSearchQuery, {
        refreshMeta: false,
        scrollToTop,
        intentPage: requestedPage,
    });
}

function changeConversationsPageSize() {
    const sel = document.getElementById('conversations-page-size-pagination');
    const newSize = sel ? parseInt(sel.value, 10) : 50;
    if (![20, 50, 100].includes(newSize)) return;
    // 重建 DOM 后浏览器可能异步触发 change，值未变时不应重置页码
    if (newSize === conversationsPagination.pageSize) return;
    try {
        localStorage.setItem(CONVERSATIONS_PAGE_SIZE_KEY, String(newSize));
    } catch (e) { /* ignore */ }
    conversationsPagination.pageSize = newSize;
    commitConversationsPage(1, { bumpNavigateGen: true });
    loadConversations(conversationsSearchQuery);
}
