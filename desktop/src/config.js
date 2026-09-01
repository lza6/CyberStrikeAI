// 配置页渲染器：预设、测试连接、保存
const PRESETS = {
  openai:      { provider:'openai_compatible', base_url:'https://api.openai.com/v1',                         model:'gpt-4o' },
  deepseek:    { provider:'openai_compatible', base_url:'https://api.deepseek.com/v1',                       model:'deepseek-chat' },
  qwen:        { provider:'openai_compatible', base_url:'https://dashscope.aliyuncs.com/compatible-mode/v1', model:'qwen-max' },
  glm:         { provider:'openai_compatible', base_url:'https://open.bigmodel.cn/api/paas/v4',               model:'glm-4-plus' },
  moonshot:    { provider:'openai_compatible', base_url:'https://api.moonshot.cn/v1',                         model:'moonshot-v1-8k' },
  siliconflow: { provider:'openai_compatible', base_url:'https://api.siliconflow.cn/v1',                      model:'Qwen/Qwen2.5-7B-Instruct' }
};

const $ = (id) => document.getElementById(id);

document.querySelectorAll('.chip').forEach(c => {
  c.addEventListener('click', () => {
    const p = PRESETS[c.dataset.preset];
    if (!p) return;
    $('provider').value = p.provider;
    $('base_url').value = p.base_url;
    $('model').value = p.model;
    $('api_key').focus();
  });
});

function show(cls, msg) {
  const s = $('status'); s.className = 'status ' + cls; s.textContent = msg;
}
window.openExt = (e) => { e.preventDefault(); window.__api.openExternal(e.currentTarget.href); return false; };

$('test-btn').addEventListener('click', async () => {
  const base = $('base_url').value.trim();
  const key = $('api_key').value.trim();
  const model = $('model').value.trim();
  const provider = $('provider').value;
  if (!base || !key || !model) { show('err', '请先填写 Base URL / API Key / 模型'); return; }
  $('test-btn').disabled = true; show('ok', '测试中...');
  try {
    const r = await window.__api.testConnection({ provider, base_url: base, api_key: key, model });
    if (r && r.ok) show('ok', '✓ 连接成功' + (r.model ? ' · 模型: ' + r.model : ''));
    else show('err', '× 连接失败：' + (r && r.error ? r.error : '未知错误'));
  } catch (e) { show('err', '× 测试异常：' + e.message); }
  finally { $('test-btn').disabled = false; }
});

$('save-btn').addEventListener('click', async () => {
  const base = $('base_url').value.trim();
  const key = $('api_key').value.trim();
  const model = $('model').value.trim();
  if (!base || !key || !model) { show('err', '请填写所有必填项（*）'); return; }
  $('save-btn').disabled = true;
  show('ok', '保存中...');
  const r = await window.__api.saveAndLaunch({
    provider: $('provider').value,
    base_url: base, api_key: key, model,
    max_total_tokens: parseInt($('max_total_tokens').value, 10) || 120000,
    max_completion_tokens: parseInt($('max_completion_tokens').value, 10) || 16384
  });
  if (!r || !r.ok) {
    $('save-btn').disabled = false;
    show('err', '保存失败：' + (r && r.error ? r.error : '未知错误（请查看 data/logs/desktop-backend.log）'));
  }
});
