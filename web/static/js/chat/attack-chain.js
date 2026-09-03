
// 删除对话
async function deleteConversation(conversationId, skipConfirm = false) {
    // 确认删除（如果调用者没有跳过确认）
    if (!skipConfirm) {
        if (!confirm('确定要删除这个对话吗？对话消息将不可恢复，但已记录的漏洞会保留在漏洞库中。')) {
            return;
        }
    }

    try {
        const response = await apiFetch(`/api/conversations/${conversationId}`, {
            method: 'DELETE'
        });

        if (!response.ok) {
            const error = await response.json();
            throw new Error(error.error || '删除失败');
        }

        // 如果删除的是当前对话，清空对话界面
        if (conversationId === currentConversationId) {
            currentConversationId = null;
            try {
                window.currentConversationId = '';
            } catch (e) { /* ignore */ }
            document.getElementById('chat-messages').innerHTML = '';
            renderChatWelcomeEmptyState();
            addAttackChainButton(null);
        }

        invalidateConversationLiteCache(conversationId);

        // 先同步所有侧栏的本地状态，再执行网络刷新。项目文件夹使用独立的
        // conversation cache；如果只刷新“最近对话”，删除项会一直残留到整页刷新。
        try {
            document.dispatchEvent(new CustomEvent('conversation-deleted', { detail: { conversationId } }));
        } catch (e) { /* ignore */ }

        // 刷新对话列表
        if (typeof loadConversations === 'function') {
            loadConversations();
        }

        // 批量管理弹窗打开时，同步刷新弹窗内列表
        const batchModal = document.getElementById('batch-manage-modal');
        if (batchModal && isAppModalOpen('batch-manage-modal')) {
            allConversationsForBatch = allConversationsForBatch.filter(c => c.id !== conversationId);
            applyBatchConversationFilters();
        }

    } catch (error) {
        logger.error('删除对话失败:', error);
        // F5：失败 toast 替代 alert
        const msg = '删除对话失败: ' + error.message;
        if (typeof window.showToast === 'function') window.showToast(msg, 'error');
        else if (typeof window.showChatToast === 'function') window.showChatToast(msg, 'error');
        else alert(msg);
    }
}

// 更新活动对话样式
function updateActiveConversation() {
    document.querySelectorAll('.conversation-item').forEach(item => {
        item.classList.remove('active');
        if (currentConversationId && item.dataset.conversationId === currentConversationId) {
            item.classList.add('active');
        }
    });
}

// ==================== 攻击链可视化功能 ====================

// 生成节点图标的 data URL（用于 Cytoscape background-image）
// 返回一个精美的渐变色方块 + 白色矢量图标
function _acBuildNodeIconDataUrl(iconType, color, colorDark) {
    let iconPath = '';
    if (iconType === 'target') {
        iconPath = 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.42 0-8-3.58-8-8s3.58-8 8-8 8 3.58 8 8-3.58 8-8 8zm0-14c-3.31 0-6 2.69-6 6s2.69 6 6 6 6-2.69 6-6-2.69-6-6-6zm0 10c-2.21 0-4-1.79-4-4s1.79-4 4-4 4 1.79 4 4-1.79 4-4 4z';
    } else if (iconType === 'action') {
        iconPath = 'M7 2v11h3v9l7-12h-4l4-8z';
    } else if (iconType === 'vulnerability') {
        iconPath = 'M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm-1 6h2v6h-2V7zm0 8h2v2h-2v-2z';
    } else {
        iconPath = 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z';
    }
    // 64x64 的图标方块（渐变 + 圆角 + 白色矢量图标）
    const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">
<defs>
<linearGradient id="g" x1="0%" y1="0%" x2="100%" y2="100%">
<stop offset="0%" stop-color="${color}"/>
<stop offset="100%" stop-color="${colorDark}"/>
</linearGradient>
</defs>
<rect x="0" y="0" width="64" height="64" rx="14" fill="url(#g)"/>
<g transform="translate(14 14) scale(1.5)"><path d="${iconPath}" fill="#FFFFFF"/></g>
</svg>`;
    // 使用 base64 编码（btoa 在浏览器中原生支持）
    try {
        return 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(svg)));
    } catch (e) {
        // 兜底：URL 编码
        return 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(svg);
    }
}

let attackChainCytoscape = null;
let currentAttackChainConversationId = null;
// 按对话ID管理加载状态，实现不同对话之间的解耦
const attackChainLoadingMap = new Map(); // Map<conversationId, boolean>

// 检查指定对话是否正在加载
function isAttackChainLoading(conversationId) {
    return attackChainLoadingMap.get(conversationId) === true;
}

// 设置指定对话的加载状态
function setAttackChainLoading(conversationId, loading) {
    if (loading) {
        attackChainLoadingMap.set(conversationId, true);
    } else {
        attackChainLoadingMap.delete(conversationId);
    }
}

// 添加攻击链按钮（已移至菜单，此函数保留以保持兼容性，但不再显示顶部按钮）
function addAttackChainButton(conversationId) {
    // 攻击链按钮已移至三点菜单，不再需要显示顶部按钮
    // 此函数保留以保持代码兼容性，但不再执行任何操作
    const conversationHeader = document.getElementById('conversation-header');
    if (conversationHeader) {
        conversationHeader.style.display = 'none';
    }
}

function updateAttackChainAvailability() {
    addAttackChainButton(currentConversationId);
}

// 显示攻击链模态框
async function showAttackChain(conversationId) {
    // 如果当前显示的对话ID不同，或者没有在加载，允许打开
    // 如果正在加载同一个对话，也允许打开（显示加载状态）
    if (isAttackChainLoading(conversationId) && currentAttackChainConversationId === conversationId) {
        // 如果模态框已经打开且显示的是同一个对话，不重复打开
        const modal = document.getElementById('attack-chain-modal');
        if (modal && isAppModalOpen('attack-chain-modal')) {
            return;
        }
    }

    currentAttackChainConversationId = conversationId;
    const modal = document.getElementById('attack-chain-modal');
    if (!modal) {
        logger.error('攻击链模态框未找到');
        return;
    }

    openAppModal('attack-chain-modal', { focus: false });
    updateAttackChainStats({ nodes: [], edges: [] });

    // 清空容器
    const container = document.getElementById('attack-chain-container');
    if (container) {
        container.innerHTML = '<div class="loading-spinner">' + (typeof window.t === 'function' ? window.t('chat.loading') : '加载中...') + '</div>';
    }

    // 隐藏详情面板
    const detailsPanel = document.getElementById('attack-chain-details');
    if (detailsPanel) {
        detailsPanel.style.display = 'none';
    }

    // 禁用重新生成按钮
    const regenerateBtn = document.querySelector('button[onclick="regenerateAttackChain()"]');
    if (regenerateBtn) {
        regenerateBtn.disabled = true;
        regenerateBtn.style.opacity = '0.5';
        regenerateBtn.style.cursor = 'not-allowed';
    }

    // 加载攻击链数据
    await loadAttackChain(conversationId);
}

// 加载攻击链数据
async function loadAttackChain(conversationId) {
    if (isAttackChainLoading(conversationId)) {
        return; // 防止重复调用
    }

    setAttackChainLoading(conversationId, true);

    try {
        const response = await apiFetch(`/api/attack-chain/${conversationId}`);

        if (!response.ok) {
            // 处理 409 Conflict（正在生成中）
            if (response.status === 409) {
                const error = await response.json();
                const container = document.getElementById('attack-chain-container');
                if (container) {
                    container.innerHTML = `
                        <div style="text-align: center; padding: 28px 24px; color: var(--text-secondary);">
                            <div style="display: inline-flex; align-items: center; gap: 8px; font-size: 0.95rem; color: var(--text-primary);">
                                <span role="presentation" aria-hidden="true">⏳</span>
                                <span>攻击链生成中，请稍候</span>
                            </div>
                            <button class="btn-secondary" onclick="refreshAttackChain()" style="margin-top: 12px; font-size: 0.78rem; padding: 4px 12px;">
                                刷新
                            </button>
                        </div>
                    `;
                }
                // 5秒后自动刷新（允许刷新，但保持加载状态防止重复点击）
                // 使用闭包保存 conversationId，防止串台
                setTimeout(() => {
                    // 检查当前显示的对话ID是否匹配
                    if (currentAttackChainConversationId === conversationId) {
                        refreshAttackChain();
                    }
                }, 5000);
                // 在 409 情况下，保持加载状态，防止重复点击
                // 但允许 refreshAttackChain 调用 loadAttackChain 来检查状态
                // 注意：不重置加载状态，保持加载状态
                // 恢复按钮状态（虽然保持加载状态，但允许用户手动刷新）
                const regenerateBtn = document.querySelector('button[onclick="regenerateAttackChain()"]');
                if (regenerateBtn) {
                    regenerateBtn.disabled = false;
                    regenerateBtn.style.opacity = '1';
                    regenerateBtn.style.cursor = 'pointer';
                }
                return; // 提前返回，不执行 finally 块中的 setAttackChainLoading(conversationId, false)
            }

            const error = await response.json();
            throw new Error(error.error || '加载攻击链失败');
        }

        const chainData = await response.json();

        // 检查当前显示的对话ID是否匹配，防止串台
        if (currentAttackChainConversationId !== conversationId) {
            logger.info('攻击链数据已返回，但当前显示的对话已切换，忽略此次渲染', {
                returned: conversationId,
                current: currentAttackChainConversationId
            });
            setAttackChainLoading(conversationId, false);
            return;
        }

        // 渲染攻击链
        renderAttackChain(chainData);

        // 更新统计信息
        updateAttackChainStats(chainData);

        // 成功加载后，重置加载状态
        setAttackChainLoading(conversationId, false);

    } catch (error) {
        logger.error('加载攻击链失败:', error);
        const container = document.getElementById('attack-chain-container');
        if (container) {
            container.innerHTML = '<div class="error-message">' + (typeof window.t === 'function' ? window.t('chat.loadFailed', { message: escapeHtml(error.message) }) : '加载失败: ' + escapeHtml(error.message)) + '</div>';
        }
        // 错误时也重置加载状态
        setAttackChainLoading(conversationId, false);
    } finally {
        // 恢复重新生成按钮
        const regenerateBtn = document.querySelector('button[onclick="regenerateAttackChain()"]');
        if (regenerateBtn) {
            regenerateBtn.disabled = false;
            regenerateBtn.style.opacity = '1';
            regenerateBtn.style.cursor = 'pointer';
        }
    }
}

// 渲染攻击链
function renderAttackChain(chainData) {
    const container = document.getElementById('attack-chain-container');
    if (!container) {
        return;
    }

    // 清空容器
    container.innerHTML = '';

    if (!chainData.nodes || chainData.nodes.length === 0) {
        container.innerHTML = '<div class="empty-message">' + (typeof window.t === 'function' ? window.t('chat.noAttackChainData') : '暂无攻击链数据') + '</div>';
        return;
    }

    // F6：cytoscape+elk 懒加载——攻击链渲染依赖（首屏不加载，按需注入）
    if (typeof cytoscape === 'undefined' && typeof loadScript === 'function') {
        Promise.all([
            loadScript('/static/vendor/cytoscape.min.js'),
            loadScript('/static/vendor/elk.bundled.js')
        ]).then(function () {
            if (typeof ELK === 'undefined' && typeof elk !== 'undefined') window.ELK = elk;
            renderAttackChain(chainData); // 注入完成后重新渲染
        }).catch(function (e) {
            container.innerHTML = '<div class="error-message">' + (typeof window.t === 'function' ? window.t('chat.attackChainLoadFailed') : '攻击链组件加载失败') + ': ' + (e && e.message ? e.message : e) + '</div>';
        });
        return;
    }

    // 计算图的复杂度（用于动态调整布局和样式）
    const nodeCount = chainData.nodes.length;
    const edgeCount = chainData.edges.length;
    const isComplexGraph = nodeCount > 15 || edgeCount > 25;
    const isDarkTheme = document.documentElement.getAttribute('data-theme') === 'dark';

    // 优化节点标签：智能截断和换行
    chainData.nodes.forEach(node => {
        if (node.label) {
            // 智能截断：优先在标点符号、空格处截断
            const maxLength = isComplexGraph ? 18 : 22;
            if (node.label.length > maxLength) {
                let truncated = node.label.substring(0, maxLength);
                // 尝试在最后一个标点符号或空格处截断
                const lastPunct = Math.max(
                    truncated.lastIndexOf('，'),
                    truncated.lastIndexOf('。'),
                    truncated.lastIndexOf('、'),
                    truncated.lastIndexOf(' '),
                    truncated.lastIndexOf('/')
                );
                if (lastPunct > maxLength * 0.6) { // 如果标点符号位置合理
                    truncated = truncated.substring(0, lastPunct + 1);
                }
                node.label = truncated + '...';
            }
        }
    });

    // 准备Cytoscape数据
    const elements = [];

    // 添加节点，并预计算样式信息（与导出保持一致的主题色）
    chainData.nodes.forEach(node => {
        const riskScore = node.risk_score || 0;
        const nodeType = node.type || '';
        const metadata = node.metadata || {};

        // 统一的主题系统（与导出一致）
        let typeLabel = '节点';
        let typeEn = 'NODE';
        let typeColor = '#334155';      // 主文字色
        let accentColor = '#94a3b8';    // 强调色（图标/边框）
        let accentDark = '#475569';     // 深色版本
        let bgGradientStart = '#FFFFFF';
        let bgGradientEnd = '#F8FAFC';
        let iconType = 'default';       // 图标类型

        if (nodeType === 'target') {
            typeLabel = '目标';
            typeEn = 'TARGET';
            typeColor = '#312E81';
            accentColor = '#4F46E5';
            accentDark = '#3730A3';
            bgGradientStart = '#FFFFFF';
            bgGradientEnd = '#F5F3FF';
            iconType = 'target';
        } else if (nodeType === 'action') {
            typeLabel = '行动';
            typeEn = 'ACTION';
            const findings = metadata.findings || [];
            const hasFindings = Array.isArray(findings) && findings.length > 0;
            const isFailedInsight = (metadata.status || '') === 'failed_insight';
            if (hasFindings && !isFailedInsight) {
                typeColor = '#064E3B';
                accentColor = '#10B981';
                accentDark = '#047857';
                bgGradientStart = '#FFFFFF';
                bgGradientEnd = '#ECFDF5';
            } else {
                typeColor = '#334155';
                accentColor = '#64748B';
                accentDark = '#475569';
                bgGradientStart = '#FFFFFF';
                bgGradientEnd = '#F8FAFC';
            }
            iconType = 'action';
        } else if (nodeType === 'vulnerability') {
            typeLabel = '漏洞';
            typeEn = 'VULNERABILITY';
            if (riskScore >= 80) {
                typeColor = '#881337';
                accentColor = '#E11D48';
                accentDark = '#BE123C';
                bgGradientStart = '#FFFFFF';
                bgGradientEnd = '#FFF1F2';
            } else if (riskScore >= 60) {
                typeColor = '#7C2D12';
                accentColor = '#EA580C';
                accentDark = '#C2410C';
                bgGradientStart = '#FFFFFF';
                bgGradientEnd = '#FFF7ED';
            } else if (riskScore >= 40) {
                typeColor = '#713F12';
                accentColor = '#CA8A04';
                accentDark = '#A16207';
                bgGradientStart = '#FFFFFF';
                bgGradientEnd = '#FEFCE8';
            } else {
                typeColor = '#134E4A';
                accentColor = '#0D9488';
                accentDark = '#0F766E';
                bgGradientStart = '#FFFFFF';
                bgGradientEnd = '#F0FDFA';
            }
            iconType = 'vulnerability';
        }

        const labelTextColor = isDarkTheme ? '#E5E7EB' : '#0F172A';
        if (isDarkTheme) {
            typeColor = '#E5E7EB';
            bgGradientStart = '#111827';
            if (nodeType === 'target') {
                bgGradientEnd = '#1E1B4B';
            } else if (nodeType === 'action') {
                bgGradientEnd = accentColor === '#10B981' ? '#052E2B' : '#172033';
            } else if (nodeType === 'vulnerability') {
                if (riskScore >= 80) {
                    bgGradientEnd = '#3F101C';
                } else if (riskScore >= 60) {
                    bgGradientEnd = '#3B1D0D';
                } else if (riskScore >= 40) {
                    bgGradientEnd = '#3A2A0A';
                } else {
                    bgGradientEnd = '#063A36';
                }
            } else {
                bgGradientEnd = '#172033';
            }
        }

        // 为每个节点生成图标 background-image（data URL）
        const iconSvg = _acBuildNodeIconDataUrl(iconType, accentColor, accentDark);

        // 计算徽章文本（右上角）
        let badgeText = '';
        if (nodeType === 'vulnerability' && riskScore > 0) {
            const rl = riskScore >= 80 ? '严重' : riskScore >= 60 ? '高' : riskScore >= 40 ? '中' : '低';
            badgeText = rl + ' · ' + riskScore;
        } else if (nodeType === 'action') {
            const findings = metadata.findings || [];
            if (Array.isArray(findings) && findings.length > 0 && metadata.status !== 'failed_insight') {
                badgeText = '发现 ' + findings.length;
            } else if (metadata.status === 'failed_insight') {
                badgeText = '有线索';
            }
        } else if (nodeType === 'target') {
            badgeText = '主目标';
        }

        elements.push({
            data: {
                id: node.id,
                label: node.label,
                originalLabel: node.label,
                type: nodeType,
                typeLabel: typeLabel,
                typeEn: typeEn,
                typeColor: typeColor,
                accentColor: accentColor,
                accentDark: accentDark,
                bgGradientStart: bgGradientStart,
                bgGradientEnd: bgGradientEnd,
                labelTextColor: labelTextColor,
                iconDataUrl: iconSvg,
                badgeText: badgeText,
                riskScore: riskScore,
                toolExecutionId: node.tool_execution_id || '',
                metadata: metadata
            }
        });
    });

    // 添加边（只添加源节点和目标节点都存在的边）
    const nodeIds = new Set(chainData.nodes.map(node => node.id));

    // 保存有效的边用于ELK布局
    const validEdges = [];
    chainData.edges.forEach(edge => {
        // 验证源节点和目标节点是否存在
        if (nodeIds.has(edge.source) && nodeIds.has(edge.target)) {
            validEdges.push(edge);
            elements.push({
                data: {
                    id: edge.id,
                    source: edge.source,
                    target: edge.target,
                    type: edge.type || 'leads_to',
                    weight: edge.weight || 1
                }
            });
        } else {
            logger.warn('跳过无效的边：源节点或目标节点不存在', {
                edgeId: edge.id,
                source: edge.source,
                target: edge.target,
                sourceExists: nodeIds.has(edge.source),
                targetExists: nodeIds.has(edge.target)
            });
        }
    });

    // 初始化Cytoscape - 现代卡片式节点设计（图标 + 文字 + 徽章）
    attackChainCytoscape = cytoscape({
        container: container,
        elements: elements,
        style: [
            {
                selector: 'node',
                style: {
                    // 节点 label：两行文字（类型英文 | 主标题）
                    'label': function(ele) {
                        const typeEn = ele.data('typeEn') || '';
                        const typeLabel = ele.data('typeLabel') || '';
                        const label = ele.data('label') || '';
                        const badgeText = ele.data('badgeText') || '';
                        // 第一行：TYPE_EN · 类型（小字）
                        // 第二行：主标题（大字）
                        // 第三行：徽章文字（彩色提示）
                        let line1 = typeEn + '  ·  ' + typeLabel;
                        if (badgeText) line1 += '  [' + badgeText + ']';
                        return line1 + '\n' + label;
                    },
                    'width': function(ele) {
                        const type = ele.data('type');
                        if (type === 'target') return isComplexGraph ? 300 : 360;
                        if (type === 'vulnerability') return isComplexGraph ? 280 : 340;
                        return isComplexGraph ? 260 : 320;
                    },
                    'height': function(ele) {
                        return isComplexGraph ? 84 : 100;
                    },
                    'shape': 'round-rectangle',
                    // 浅色渐变背景（白色到主题色极淡）
                    'background-fill': 'linear-gradient',
                    'background-gradient-direction': 'to-bottom-right',
                    'background-gradient-stop-colors': function(ele) {
                        return (ele.data('bgGradientStart') || '#FFFFFF') + ' ' +
                               (ele.data('bgGradientEnd') || '#F8FAFC');
                    },
                    'background-gradient-stop-positions': '0 100',
                    'background-opacity': 1,
                    // 左侧类型图标（SVG dataURL 作为背景图）
                    'background-image': function(ele) {
                        return ele.data('iconDataUrl') || 'none';
                    },
                    'background-image-containment': 'inside',
                    'background-fit': 'none',
                    'background-image-opacity': 1,
                    'background-width': '36px',
                    'background-height': '36px',
                    'background-position-x': '18px',
                    'background-position-y': '50%',
                    'background-offset-y': '0',
                    'background-clip': 'node',
                    'bounds-expansion': 0,
                    // 边框：主题色柔和
                    'border-width': 1.5,
                    'border-color': function(ele) {
                        return ele.data('accentColor') || '#94a3b8';
                    },
                    'border-opacity': 0.5,
                    // 文字样式
                    'color': function(ele) {
                        return ele.data('labelTextColor') || '#0f172a';
                    },
                    'font-size': function(ele) {
                        return isComplexGraph ? '13px' : '14px';
                    },
                    'font-weight': 700,
                    'font-family': '-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", "PingFang SC", "Microsoft YaHei", sans-serif',
                    // 文字左对齐、预留左侧图标空间
                    'text-valign': 'center',
                    'text-halign': 'center',
                    'text-justification': 'left',
                    'text-wrap': 'wrap',
                    'text-max-width': function(ele) {
                        const type = ele.data('type');
                        const w = (type === 'target') ? (isComplexGraph ? 300 : 360)
                                : (type === 'vulnerability') ? (isComplexGraph ? 280 : 340)
                                : (isComplexGraph ? 260 : 320);
                        return (w - 80) + 'px';
                    },
                    'text-overflow-wrap': 'anywhere',
                    'text-margin-x': 28,
                    'text-margin-y': 0,
                    'padding': '12px',
                    'line-height': 1.4,
                    'text-outline-width': 0,
                    // 柔和阴影（overlay 模拟）
                    'overlay-color': '#0f172a',
                    'overlay-opacity': 0,
                    'overlay-padding': 0,
                    'transition-property': 'overlay-opacity, border-width, border-color',
                    'transition-duration': '160ms'
                }
            },
            {
                // 目标节点：边框略粗
                selector: 'node[type = "target"]',
                style: {
                    'border-width': 2
                }
            },
            {
                // 漏洞节点：边框略粗
                selector: 'node[type = "vulnerability"]',
                style: {
                    'border-width': 2
                }
            },
            {
                selector: 'edge',
                style: {
                    'width': function(ele) {
                        const type = ele.data('type');
                        if (type === 'discovers') return 2.6;
                        if (type === 'enables') return 2.8;
                        return 2;
                    },
                    'line-color': function(ele) {
                        const type = ele.data('type');
                        if (type === 'discovers' || type === 'targets') return '#4F46E5';
                        if (type === 'enables') return '#E11D48';
                        if (type === 'leads_to') return '#64748B';
                        return '#cbd5e1';
                    },
                    'target-arrow-color': function(ele) {
                        const type = ele.data('type');
                        if (type === 'discovers' || type === 'targets') return '#4F46E5';
                        if (type === 'enables') return '#E11D48';
                        if (type === 'leads_to') return '#64748B';
                        return '#cbd5e1';
                    },
                    'target-arrow-shape': 'triangle-backcurve',
                    'arrow-scale': 1.35,
                    'curve-style': 'bezier',
                    'control-point-step-size': 60,
                    'opacity': 0.88,
                    'line-style': function(ele) {
                        const type = ele.data('type');
                        if (type === 'targets') return 'dashed';
                        return 'solid';
                    },
                    'line-dash-pattern': function(ele) {
                        const type = ele.data('type');
                        if (type === 'targets') return [10, 5];
                        return [];
                    },
                    'transition-property': 'opacity, width, line-color',
                    'transition-duration': '160ms'
                }
            },
            {
                selector: 'node:selected',
                style: {
                    'border-width': 3.5,
                    'border-color': '#4F46E5',
                    'z-index': 999,
                    'opacity': 1,
                    'overlay-opacity': 0.06,
                    'overlay-color': '#4F46E5',
                    'overlay-padding': 8
                }
            }
        ],
        userPanningEnabled: true,
        userZoomingEnabled: true,
        boxSelectionEnabled: true,
        minZoom: 0.2,
        maxZoom: 3
    });

    // 使用ELK布局（高质量DAG布局，减少边交叉）
    let layoutOptions = {
        name: 'breadthfirst',
        directed: true,
        spacingFactor: isComplexGraph ? 3.0 : 2.5,
        padding: 40
    };

    // 使用ELK.js进行布局计算
    // elk.bundled.js会暴露ELK对象，可以直接使用new ELK()
    let elkInstance = null;
    if (typeof ELK !== 'undefined') {
        try {
            elkInstance = new ELK();
        } catch (e) {
            logger.warn('ELK初始化失败:', e);
        }
    }

    if (elkInstance) {
        try {

            // === 布局参数（始终使用 DOWN 纵向布局）===
            const isSmallGraph = chainData.nodes.length <= 8 && validEdges.length <= 12;
            // 同层节点间距（横向分散）
            const nodeGap = isComplexGraph ? 45 : isSmallGraph ? 80 : 60;
            // 层间距（纵向：给连线足够发挥空间，同时避免图太高）
            const layerGap = isComplexGraph ? 70 : isSmallGraph ? 130 : 95;

            // 构建 ELK 图结构 - 节点尺寸与 Cytoscape 样式保持一致
            const elkGraph = {
                id: 'root',
                layoutOptions: {
                    'elk.algorithm': 'layered',
                    'elk.direction': 'DOWN',
                    'elk.padding': '[top=30,left=50,bottom=30,right=50]',
                    'elk.spacing.nodeNode': String(nodeGap),
                    'elk.spacing.edgeNode': '20',
                    'elk.spacing.edgeEdge': '12',
                    'elk.spacing.componentComponent': '50',
                    'elk.layered.spacing.nodeNodeBetweenLayers': String(layerGap),
                    'elk.layered.spacing.edgeNodeBetweenLayers': '20',
                    'elk.layered.spacing.edgeEdgeBetweenLayers': '12',
                    'elk.layered.nodePlacement.strategy': 'BRANDES_KOEPF',
                    'elk.layered.nodePlacement.bk.fixedAlignment': 'BALANCED',
                    'elk.layered.nodePlacement.bk.edgeStraightening': 'IMPROVE_STRAIGHTNESS',
                    'elk.layered.crossingMinimization.strategy': 'LAYER_SWEEP',
                    'elk.layered.crossingMinimization.semiInteractive': 'false',
                    'elk.layered.thoroughness': String(isComplexGraph ? 10 : 15),
                    'elk.layered.cycleBreaking.strategy': 'GREEDY',
                    'elk.layered.compaction.connectedComponents': 'true',
                    'elk.layered.compaction.postCompaction.strategy': 'LEFT_RIGHT_CONSTRAINT_LOCKING',
                    'elk.layered.unnecessaryBendpoints': 'true',
                    'elk.layered.mergeEdges': 'false'
                },
                children: chainData.nodes.map(node => {
                    const type = node.type || '';
                    return {
                        id: node.id,
                        width: type === 'target' ? (isComplexGraph ? 300 : 360) :
                               type === 'vulnerability' ? (isComplexGraph ? 280 : 340) :
                               (isComplexGraph ? 260 : 320),
                        height: isComplexGraph ? 84 : 100
                    };
                }),
                edges: validEdges.map(edge => ({
                    id: edge.id,
                    sources: [edge.source],
                    targets: [edge.target]
                }))
            };

            // 使用ELK计算布局
            elkInstance.layout(elkGraph).then(laidOutGraph => {
                // 应用ELK计算的布局到Cytoscape节点
                if (laidOutGraph && laidOutGraph.children) {
                    laidOutGraph.children.forEach(elkNode => {
                        const cyNode = attackChainCytoscape.getElementById(elkNode.id);
                        if (cyNode && elkNode.x !== undefined && elkNode.y !== undefined) {
                            cyNode.position({
                                x: elkNode.x + (elkNode.width || 0) / 2,
                                y: elkNode.y + (elkNode.height || 0) / 2
                            });
                        }
                    });

                    // 布局完成后，居中显示图
                    setTimeout(() => {
                        centerAttackChain();
                    }, 150);
                } else {
                    throw new Error('ELK布局返回无效结果');
                }
            }).catch(err => {
                logger.warn('ELK布局计算失败，使用默认布局:', err);
                // 回退到默认布局
                const layout = attackChainCytoscape.layout(layoutOptions);
                layout.one('layoutstop', () => {
                    setTimeout(() => {
                        centerAttackChain();
                    }, 100);
                });
                layout.run();
            });
        } catch (e) {
            logger.warn('ELK布局初始化失败，使用默认布局:', e);
            // 回退到默认布局
            const layout = attackChainCytoscape.layout(layoutOptions);
            layout.one('layoutstop', () => {
                setTimeout(() => {
                    centerAttackChain();
                }, 100);
            });
            layout.run();
        }
    } else {
        logger.warn('ELK.js未加载，使用默认布局。请检查elkjs库是否正确加载。');
        // 使用默认布局
        const layout = attackChainCytoscape.layout(layoutOptions);
        layout.one('layoutstop', () => {
            setTimeout(() => {
                centerAttackChain();
            }, 100);
        });
        layout.run();
    }

    // 居中攻击链的函数：始终让所有节点完整可见
    function centerAttackChain() {
        try {
            if (!attackChainCytoscape) {
                return;
            }
            const container = attackChainCytoscape.container();
            if (!container) return;
            const containerWidth = container.offsetWidth;
            const containerHeight = container.offsetHeight;
            if (containerWidth === 0 || containerHeight === 0) {
                setTimeout(centerAttackChain, 100);
                return;
            }

            // 使用较大 padding 让节点不贴边，视觉上更舒适
            // 核心原则：完全依赖 fit 的结果来保证全局可见，不强制最小缩放
            const padding = 60;
            attackChainCytoscape.fit(undefined, padding);

            // 只在极端情况下微调：小图（2-3 节点）fit 后缩放过大时适当降低
            setTimeout(() => {
                if (!attackChainCytoscape) return;
                const currentZoom = attackChainCytoscape.zoom();
                // 上限：避免节点占满屏幕看起来过大
                const MAX_INITIAL_ZOOM = 1.25;
                // 下限：避免极小图看不清（极小图通常节点很少）
                const MIN_READABLE_ZOOM = 0.25;

                let targetZoom = currentZoom;
                if (currentZoom > MAX_INITIAL_ZOOM) {
                    targetZoom = MAX_INITIAL_ZOOM;
                } else if (currentZoom < MIN_READABLE_ZOOM) {
                    // 如果 fit 后缩放低于 0.25，说明图超大；保持当前结果，让用户可拖动查看
                    targetZoom = MIN_READABLE_ZOOM;
                }

                if (Math.abs(targetZoom - currentZoom) > 0.01) {
                    const extent = attackChainCytoscape.extent();
                    const cx = (extent.x1 + extent.x2) / 2;
                    const cy = (extent.y1 + extent.y2) / 2;
                    attackChainCytoscape.zoom({
                        level: targetZoom,
                        position: { x: cx, y: cy }
                    });
                }
                attackChainCytoscape.center();
            }, 60);
        } catch (error) {
            logger.warn('居中图表时出错:', error);
        }
    }

    // 添加点击事件
    attackChainCytoscape.on('tap', 'node', function(evt) {
        const node = evt.target;
        showNodeDetails(node.data());
    });

    // 点击空白处关闭详情
    attackChainCytoscape.on('tap', function(evt) {
        if (evt.target === attackChainCytoscape) {
            attackChainCytoscape.elements().unselect();
        }
    });

    // 添加悬停效果：增强边框 + 柔光叠加 + 淡化不相关连线
    attackChainCytoscape.on('mouseover', 'node', function(evt) {
        const node = evt.target;
        const accent = node.data('accentColor') || '#4F46E5';
        node.style({
            'border-width': 3,
            'border-color': accent,
            'border-opacity': 1,
            'overlay-color': accent,
            'overlay-opacity': 0.08,
            'overlay-padding': 10,
            'z-index': 998
        });
        const connected = node.connectedEdges();
        attackChainCytoscape.edges().not(connected).style('opacity', 0.2);
        connected.style({ 'opacity': 1, 'width': 3.5 });
    });

    attackChainCytoscape.on('mouseout', 'node', function(evt) {
        const node = evt.target;
        const type = node.data('type');
        const defaultBorderWidth = (type === 'target' || type === 'vulnerability') ? 2 : 1.5;
        node.style({
            'border-width': defaultBorderWidth,
            'border-color': node.data('accentColor') || '#94a3b8',
            'border-opacity': 0.5,
            'overlay-opacity': 0,
            'overlay-padding': 0,
            'z-index': 0
        });
        attackChainCytoscape.edges().style({ 'opacity': 0.88, 'width': '' });
    });

    // 保存原始数据用于过滤
    window.attackChainOriginalData = chainData;
}

// 安全地获取边的源节点和目标节点
function getEdgeNodes(edge) {
    try {
        const source = edge.source();
        const target = edge.target();

        // 检查源节点和目标节点是否存在
        if (!source || !target || source.length === 0 || target.length === 0) {
            return { source: null, target: null, valid: false };
        }

        return { source: source, target: target, valid: true };
    } catch (error) {
        logger.warn('获取边的节点时出错:', error, edge.id());
        return { source: null, target: null, valid: false };
    }
}

// 过滤攻击链节点（按搜索关键词）
function filterAttackChainNodes(searchText) {
    if (!attackChainCytoscape || !window.attackChainOriginalData) {
        return;
    }

    const searchLower = searchText.toLowerCase().trim();
    if (searchLower === '') {
        // 重置所有节点可见性
        attackChainCytoscape.nodes().style('display', 'element');
        attackChainCytoscape.edges().style('display', 'element');
        // 恢复默认边框
        attackChainCytoscape.nodes().style('border-width', 2);
        return;
    }

    // 过滤节点
    attackChainCytoscape.nodes().forEach(node => {
        // 使用原始标签进行搜索，不包含类型标签
        const originalLabel = node.data('originalLabel') || node.data('label') || '';
        const label = originalLabel.toLowerCase();
        const type = (node.data('type') || '').toLowerCase();
        const matches = label.includes(searchLower) || type.includes(searchLower);

        if (matches) {
            node.style('display', 'element');
            // 高亮匹配的节点
            node.style('border-width', 4);
            node.style('border-color', '#0066ff');
        } else {
            node.style('display', 'none');
        }
    });

    // 隐藏没有可见源节点或目标节点的边
    attackChainCytoscape.edges().forEach(edge => {
        const { source, target, valid } = getEdgeNodes(edge);
        if (!valid) {
            edge.style('display', 'none');
            return;
        }

        const sourceVisible = source.style('display') !== 'none';
        const targetVisible = target.style('display') !== 'none';
        if (sourceVisible && targetVisible) {
            edge.style('display', 'element');
        } else {
            edge.style('display', 'none');
        }
    });

    // 重新调整视图
    attackChainCytoscape.fit(undefined, 60);
}

// 按类型过滤攻击链节点
function filterAttackChainByType(type) {
    if (!attackChainCytoscape || !window.attackChainOriginalData) {
        return;
    }

    if (type === 'all') {
        attackChainCytoscape.nodes().style('display', 'element');
        attackChainCytoscape.edges().style('display', 'element');
        attackChainCytoscape.nodes().style('border-width', 2);
        attackChainCytoscape.fit(undefined, 60);
        return;
    }

    // 过滤节点
    attackChainCytoscape.nodes().forEach(node => {
        const nodeType = node.data('type') || '';
        if (nodeType === type) {
            node.style('display', 'element');
        } else {
            node.style('display', 'none');
        }
    });

    // 隐藏没有可见源节点或目标节点的边
    attackChainCytoscape.edges().forEach(edge => {
        const { source, target, valid } = getEdgeNodes(edge);
        if (!valid) {
            edge.style('display', 'none');
            return;
        }

        const sourceVisible = source.style('display') !== 'none';
        const targetVisible = target.style('display') !== 'none';
        if (sourceVisible && targetVisible) {
            edge.style('display', 'element');
        } else {
            edge.style('display', 'none');
        }
    });

    // 重新调整视图
    attackChainCytoscape.fit(undefined, 60);
}

// 按风险等级过滤攻击链节点
function filterAttackChainByRisk(riskLevel) {
    if (!attackChainCytoscape || !window.attackChainOriginalData) {
        return;
    }

    if (riskLevel === 'all') {
        attackChainCytoscape.nodes().style('display', 'element');
        attackChainCytoscape.edges().style('display', 'element');
        attackChainCytoscape.nodes().style('border-width', 2);
        attackChainCytoscape.fit(undefined, 60);
        return;
    }

    // 定义风险范围
    const riskRanges = {
        'high': [80, 100],
        'medium-high': [60, 79],
        'medium': [40, 59],
        'low': [0, 39]
    };

    const [minRisk, maxRisk] = riskRanges[riskLevel] || [0, 100];

    // 过滤节点
    attackChainCytoscape.nodes().forEach(node => {
        const riskScore = node.data('riskScore') || 0;
        if (riskScore >= minRisk && riskScore <= maxRisk) {
            node.style('display', 'element');
        } else {
            node.style('display', 'none');
        }
    });

    // 隐藏没有可见源节点或目标节点的边
    attackChainCytoscape.edges().forEach(edge => {
        const { source, target, valid } = getEdgeNodes(edge);
        if (!valid) {
            edge.style('display', 'none');
            return;
        }

        const sourceVisible = source.style('display') !== 'none';
        const targetVisible = target.style('display') !== 'none';
        if (sourceVisible && targetVisible) {
            edge.style('display', 'element');
        } else {
            edge.style('display', 'none');
        }
    });

    // 重新调整视图
    attackChainCytoscape.fit(undefined, 60);
}

// 重置攻击链筛选
function resetAttackChainFilters() {
    // 重置搜索框
    const searchInput = document.getElementById('attack-chain-search');
    if (searchInput) {
        searchInput.value = '';
    }

    // 重置类型筛选
    const typeFilter = document.getElementById('attack-chain-type-filter');
    if (typeFilter) {
        typeFilter.value = 'all';
    }

    // 重置风险筛选
    const riskFilter = document.getElementById('attack-chain-risk-filter');
    if (riskFilter) {
        riskFilter.value = 'all';
    }

    // 重置所有节点可见性
    if (attackChainCytoscape) {
        attackChainCytoscape.nodes().forEach(node => {
            node.style('display', 'element');
            node.style('border-width', 2); // 恢复默认边框
        });
        attackChainCytoscape.edges().style('display', 'element');
        attackChainCytoscape.fit(undefined, 60);
    }
}

// 显示节点详情
function showNodeDetails(nodeData) {
    const detailsPanel = document.getElementById('attack-chain-details');
    const detailsContent = document.getElementById('attack-chain-details-content');

    if (!detailsPanel || !detailsContent) {
        return;
    }

    // 给 sidebar 标记详情激活态，CSS 会隐藏图例让详情独占空间
    const sidebar = document.querySelector('.attack-chain-sidebar');
    if (sidebar) sidebar.classList.add('details-active');

    // 使用 requestAnimationFrame 优化显示动画
    requestAnimationFrame(() => {
        detailsPanel.style.display = 'flex';
        requestAnimationFrame(() => {
            detailsPanel.style.opacity = '1';
        });
    });

    let html = `
        <div class="node-detail-item">
            <strong>节点ID:</strong> <code>${nodeData.id}</code>
        </div>
        <div class="node-detail-item">
            <strong>类型:</strong> ${getNodeTypeLabel(nodeData.type)}
        </div>
        <div class="node-detail-item">
            <strong>标签:</strong> ${escapeHtml(nodeData.originalLabel || nodeData.label)}
        </div>
        <div class="node-detail-item">
            <strong>风险评分:</strong> ${nodeData.riskScore}/100
        </div>
    `;

    // 显示action节点信息（工具执行 + AI分析）
    if (nodeData.type === 'action' && nodeData.metadata) {
        if (nodeData.metadata.tool_name) {
            html += `
                <div class="node-detail-item">
                    <strong>工具名称:</strong> <code>${escapeHtml(nodeData.metadata.tool_name)}</code>
                </div>
            `;
        }
        if (nodeData.metadata.tool_intent) {
            html += `
                <div class="node-detail-item">
                    <strong>工具意图:</strong> <span style="color: #0066ff; font-weight: bold;">${escapeHtml(nodeData.metadata.tool_intent)}</span>
                </div>
            `;
        }
        if (nodeData.metadata.status === 'failed_insight') {
            html += `
                <div class="node-detail-item">
                    <strong>执行状态:</strong> <span style="color: #ff9800; font-weight: bold;">失败但有线索</span>
                </div>
            `;
        }
        if (nodeData.metadata.ai_analysis) {
            html += `
                <div class="node-detail-item">
                    <strong>AI分析:</strong> <div class="node-detail-ai-analysis">${escapeHtml(nodeData.metadata.ai_analysis)}</div>
                </div>
            `;
        }
        if (nodeData.metadata.findings && Array.isArray(nodeData.metadata.findings) && nodeData.metadata.findings.length > 0) {
            html += `
                <div class="node-detail-item">
                    <strong>关键发现:</strong>
                    <ul style="margin: 5px 0; padding-left: 20px;">
                        ${nodeData.metadata.findings.map(f => `<li>${escapeHtml(f)}</li>`).join('')}
                    </ul>
                </div>
            `;
        }
    }

    // 显示目标信息（如果是目标节点）
    if (nodeData.type === 'target' && nodeData.metadata && nodeData.metadata.target) {
        html += `
            <div class="node-detail-item">
                <strong>测试目标:</strong> <code>${escapeHtml(nodeData.metadata.target)}</code>
            </div>
        `;
    }

    // 显示漏洞信息（如果是漏洞节点）
    if (nodeData.type === 'vulnerability' && nodeData.metadata) {
        if (nodeData.metadata.vulnerability_type) {
            html += `
                <div class="node-detail-item">
                    <strong>漏洞类型:</strong> ${escapeHtml(nodeData.metadata.vulnerability_type)}
                </div>
            `;
        }
        if (nodeData.metadata.description) {
            html += `
                <div class="node-detail-item">
                    <strong>描述:</strong> ${escapeHtml(nodeData.metadata.description)}
                </div>
            `;
        }
        if (nodeData.metadata.severity) {
            html += `
                <div class="node-detail-item">
                    <strong>严重程度:</strong> <span style="color: ${getSeverityColor(nodeData.metadata.severity)}; font-weight: bold;">${escapeHtml(nodeData.metadata.severity)}</span>
                </div>
            `;
        }
        if (nodeData.metadata.location) {
            html += `
                <div class="node-detail-item">
                    <strong>位置:</strong> <code>${escapeHtml(nodeData.metadata.location)}</code>
                </div>
            `;
        }
    }

    if (nodeData.toolExecutionId) {
        html += `
            <div class="node-detail-item">
                <strong>工具执行ID:</strong> <code>${nodeData.toolExecutionId}</code>
            </div>
        `;
    }

    // 详情占满 sidebar 后，内容区滚动由自身处理，重置到顶部
    if (detailsContent) {
        detailsContent.scrollTop = 0;
    }

    requestAnimationFrame(() => {
        detailsContent.innerHTML = html;
        requestAnimationFrame(() => {
            if (detailsContent) {
                detailsContent.scrollTop = 0;
            }
        });
    });
}

// 获取严重程度颜色
function getSeverityColor(severity) {
    const colors = {
        'critical': '#ff0000',
        'high': '#ff4444',
        'medium': '#ff8800',
        'low': '#ffbb00'
    };
    return colors[severity.toLowerCase()] || '#666';
}

// 获取节点类型标签
function getNodeTypeLabel(type) {
    const labels = {
        'action': '行动',
        'vulnerability': '漏洞',
        'target': '目标'
    };
    return labels[type] || type;
}

// 更新统计信息（使用 i18n，与 attackChainModal.nodesEdges 一致）
function updateAttackChainStats(chainData) {
    const statsElement = document.getElementById('attack-chain-stats');
    if (statsElement) {
        const nodeCount = chainData.nodes ? chainData.nodes.length : 0;
        const edgeCount = chainData.edges ? chainData.edges.length : 0;
        if (typeof window.t === 'function') {
            statsElement.textContent = window.t('attackChainModal.nodesEdges', {
                nodes: nodeCount,
                edges: edgeCount
            });
        } else {
            statsElement.textContent = `Nodes: ${nodeCount} | Edges: ${edgeCount}`;
        }
    }
}