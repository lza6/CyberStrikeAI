// AI 通道配置读写：负责 config.yaml 的 ai.channels 段
// 用 js-yaml 读全量、改字段、dump 回（注释会丢失，小白场景可接受）
const fs = require('fs');
const path = require('path');
const yaml = require('js-yaml');

// 判定一个 api_key 是否「未配置」（空 / 占位符 / 示例值）
function isKeyUnconfigured(k) {
  if (!k) return true;
  const s = String(k).trim();
  if (!s) return true;
  const low = s.toLowerCase();
  if (low === 'proxy_managed') return true;       // 本机 Claude 代理占位，对小白无效
  if (low.startsWith('sk-xxxx')) return true;     // example 占位
  if (low.includes('your_') || low.includes('xxxx')) return true;
  if (s.length < 12) return true;                 // 太短，多半不是真 key
  return false;
}

function loadConfig(cfgPath) {
  if (!fs.existsSync(cfgPath)) return null;
  const text = fs.readFileSync(cfgPath, 'utf8');
  try { return yaml.load(text) || {}; }
  catch { return null; }
}

function saveConfig(cfgPath, cfg) {
  const text = yaml.dump(cfg, { lineWidth: -1, noRefs: true });
  fs.writeFileSync(cfgPath, text, 'utf8');
}

// 返回 {needSetup, channel, channelId}
function inspectAIChannel(cfg) {
  if (!cfg || !cfg.ai) return { needSetup: true };
  const channels = cfg.ai.channels || {};
  const defaultId = cfg.ai.default_channel;
  let id = defaultId;
  if (!id || !channels[id]) {
    const keys = Object.keys(channels);
    if (keys.length === 0) return { needSetup: true };
    id = keys[0];
  }
  const ch = channels[id] || {};
  const need = isKeyUnconfigured(ch.api_key) || !ch.base_url || !ch.model;
  return { needSetup: need, channel: ch, channelId: id, channels, defaultId: id };
}

// 把表单写入 ai.channels.<id>，并设为默认通道
function applyChannel(cfgPath, { id, name, provider, base_url, api_key, model, max_total_tokens, max_completion_tokens }) {
  const cfg = loadConfig(cfgPath) || {};
  if (!cfg.ai) cfg.ai = {};
  if (!cfg.ai.channels) cfg.ai.channels = {};
  const cleanId = (id || 'custom').replace(/[^a-zA-Z0-9_-]/g, '-').toLowerCase() || 'custom';
  cfg.ai.channels[cleanId] = {
    name: name || 'Custom Channel',
    provider: provider || 'openai_compatible',
    base_url: (base_url || '').trim(),
    api_key: (api_key || '').trim(),
    model: (model || '').trim(),
    max_total_tokens: max_total_tokens || 120000,
    max_completion_tokens: max_completion_tokens || 16384,
    reasoning: { mode: 'off', effort: '', allow_client_reasoning: false, profile: 'openai_compat' }
  };
  cfg.ai.default_channel = cleanId;
  saveConfig(cfgPath, cfg);
  return cleanId;
}

module.exports = { isKeyUnconfigured, loadConfig, saveConfig, inspectAIChannel, applyChannel };
