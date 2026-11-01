/* Public deployment adapter. This value is not a service credential. */
(() => {
  'use strict';
  try { sessionStorage.setItem('task-ledger:token', 'public-demo'); } catch {}
  document.addEventListener('DOMContentLoaded', () => {
    const prefix = 'task-ledger-public:pending:';
    const element = (tag, text, attributes = {}) => {
      const node = document.createElement(tag);
      if (text) node.textContent = text;
      for (const [key, value] of Object.entries(attributes)) node.setAttribute(key, value);
      return node;
    };
    const actions = document.querySelector('.top-actions');
    const open = element('button', '提交任务', {id: 'demo-submit-open', class: 'primary'});
    actions.append(element('a', '源码', {href: 'https://github.com/Csneer/task-ledger-demo', class: 'demo-source', target: '_blank', rel: 'noopener noreferrer'}), open);
    document.querySelector('.identity').append(element('span', '公开演示', {class: 'demo-badge'}));
    const dialog = element('dialog', '', {id: 'demo-dialog', 'aria-labelledby': 'demo-title'});
    const form = element('form');
    const heading = element('div', '', {class: 'dialog-head'});
    const close = element('button', '×', {type: 'button', class: 'close', 'aria-label': '关闭游客投稿'});
    heading.append(element('h2', '提出一个任务', {id: 'demo-title'}), close);
    form.append(heading, element('p', '这里展示演示轨迹，也欢迎提出希望人与 Agent 协作完成的任务。游客投稿由站点代发布，维护者决定是否承接并验收；提交不会自动启动 Agent。', {class: 'subtle'}));
    const goal = element('input', '', {id: 'demo-goal', name: 'goal', required: '', minlength: '4', maxlength: '200', placeholder: '例如：为这个开源项目整理一份入门教程'});
    const result = element('textarea', '', {id: 'demo-result', name: 'expected_result', required: '', minlength: '8', maxlength: '2000', rows: '5', placeholder: '描述希望得到的成果、范围和判断依据'});
    const consent = element('input', '', {type: 'checkbox', id: 'demo-consent', required: ''});
    const check = element('label', '', {class: 'demo-consent'});
    check.append(consent, element('span', '我知道任务内容会公开展示，不提交密码、联系方式或其他私人资料。'));
    const status = element('p', '', {id: 'demo-status', class: 'form-error', role: 'status', 'aria-live': 'polite'});
    const submit = element('button', '公开提交任务', {type: 'submit', class: 'primary submit'});
    form.append(element('label', '任务目标', {for: 'demo-goal'}), goal, element('label', '预期结果', {for: 'demo-result'}), result, check, status, submit);
    dialog.append(form); document.body.append(dialog);
    let pending = null, busy = false;
    function restore() {
      const keys = Object.keys(localStorage).filter(key => key.startsWith(prefix)).sort();
      pending = keys.length ? JSON.parse(localStorage.getItem(keys[0])) : null;
      if (pending) {
        goal.value = pending.goal; result.value = pending.expected_result; consent.checked = true;
        status.textContent = '有一份待核对的投稿。请使用原内容重试，避免重复创建任务。';
      }
      goal.readOnly = result.readOnly = Boolean(pending);
      consent.disabled = Boolean(pending);
      submit.textContent = pending ? '核对原投稿' : '公开提交任务';
    }
    open.addEventListener('click', () => {
      try { restore(); } catch {
        submit.disabled = true;
        status.textContent = '无法读取本机投稿记录。请启用网站存储后再试，以免重复提交。';
      }
      dialog.showModal();
    });
    close.addEventListener('click', () => { if (!busy) dialog.close(); });
    dialog.addEventListener('cancel', event => { if (busy) event.preventDefault(); });
    form.addEventListener('submit', async event => {
      event.preventDefault();
      if (busy || !form.reportValidity()) return;
      try {
        if (!pending) {
          pending = {id: crypto.randomUUID(), goal: goal.value.trim(), expected_result: result.value.trim(), public: true};
          localStorage.setItem(prefix + pending.id, JSON.stringify(pending));
        }
      } catch {
        pending = null; status.textContent = '无法保存投稿记录，尚未发送。请启用网站存储后再试。'; return;
      }
      busy = true; submit.disabled = close.disabled = true;
      goal.readOnly = result.readOnly = true; consent.disabled = true;
      status.textContent = '正在提交，请稍候…';
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 15000);
      try {
        const response = await fetch('/demo/tasks', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(pending), signal: controller.signal, redirect: 'error', cache: 'no-store'});
        let data;
        try { data = await response.json(); } catch { throw new Error('结果暂时无法确认，请使用原投稿重试。'); }
        if (!response.ok) {
          if (data.may_have_committed === false) {
            localStorage.removeItem(prefix + pending.id); pending = null;
            goal.readOnly = result.readOnly = false; consent.disabled = false;
          }
          const messages = {INTAKE_FULL: '本站投稿名额暂满，请稍后再来。', INVALID_REQUEST: '请检查任务目标和预期结果的长度。', ORIGIN_REJECTED: '请从 task.pokison.cn 页面提交。', UPSTREAM_REJECTED: '暂时无法接收此投稿，请稍后再试。'};
          throw new Error(messages[data.error] || (pending ? '结果暂时无法确认，请使用原投稿重试。' : '投稿未被接收，请稍后重试。'));
        }
        if (!data.task?.task_id || data.state !== 'offered') throw new Error('结果暂时无法确认，请使用原投稿重试。');
        localStorage.removeItem(prefix + pending.id); pending = null;
        form.reset(); goal.readOnly = result.readOnly = false; consent.disabled = false;
        status.textContent = '任务已公开提交，等待维护者安排。你可以关闭此窗口查看任务。';
        document.getElementById('refresh')?.click();
      } catch (error) {
        status.textContent = error.name === 'AbortError' || error instanceof TypeError ? '连接中断，结果尚未确认。原投稿已保留，请点击“核对原投稿”重试。' : error.message;
      } finally {
        clearTimeout(timeout); busy = false; submit.disabled = close.disabled = false;
        submit.textContent = pending ? '核对原投稿' : '公开提交任务';
      }
    });
  });
})();
