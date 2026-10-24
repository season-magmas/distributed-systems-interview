const POLL_INTERVAL_MS = 2000;
const REQUEST_TIMEOUT_MS = 12000;
const RUN_FIELDS = `id workflowName status createdAt startedAt finishedAt cancellationRequested dispatchPaused
  steps { name deviceId dependsOn status startedAt finishedAt error dataRef
    attempts { jobId number authorizedAt dispatchDeadline recoveryDeadline recoveryError
      executions { id outcome startedAt finishedAt error retryable measurement { state sha256 dataRef error } }
    }
  }`;
const WORKFLOWS = 'query Workflows { workflows { name isDefault steps { name } } }';
const RUNS = 'query Runs { runs { id workflowName status createdAt } }';
const RECORD = `query Record($id: ID!) { run(id: $id) { ${RUN_FIELDS} } }`;
const MUTATIONS = {
  create: `mutation Create($workflowName: String!) { createRun(workflowName: $workflowName) { ${RUN_FIELDS} } }`,
  start: `mutation Start($id: ID!) { startRun(id: $id) { ${RUN_FIELDS} } }`,
  cancel: `mutation Cancel($id: ID!) { cancelRun(id: $id) { ${RUN_FIELDS} } }`,
};

export async function graphql(query, variables = {}, fetcher = globalThis.fetch) {
  const abort = new AbortController();
  const timeout = setTimeout(() => abort.abort(), REQUEST_TIMEOUT_MS);
  try {
    const response = await fetcher('/graphql', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ query, variables }),
      signal: abort.signal,
    });
    if (!response.ok) throw Error(`Server returned HTTP ${response.status}.`);
    const result = await response.json();
    if (result.errors?.length) throw Error(result.errors.map((e) => e.message).join('; '));
    if (!result.data) throw Error('The server returned no record.');
    return result.data;
  } finally {
    clearTimeout(timeout);
  }
}

export function createNotebook({ request = graphql, onChange = () => {} } = {}) {
  const state = {
    workflows: [],
    runs: [],
    selectedId: '',
    run: null,
    busy: false,
    readError: '',
    actionError: '',
    updatedAt: null,
    loading: true,
    recordLoading: false,
  };
  let refreshing = null;
  const emit = () => onChange(state);

  async function detail(id) {
    try {
      const result = await request(RECORD, { id });
      // A slow response must never replace the record the scientist just selected.
      if (state.selectedId === id) state.run = result.run;
    } finally {
      if (state.selectedId === id) state.recordLoading = false;
    }
  }
  async function select(id) {
    state.selectedId = id;
    state.run = null;
    state.recordLoading = true;
    state.actionError = '';
    emit();
    try {
      await detail(id);
    } catch (error) {
      if (state.selectedId === id) state.readError = error.message;
    }
    emit();
  }
  function refresh() {
    if (refreshing) return refreshing;
    if (state.busy) return Promise.resolve();
    refreshing = (async () => {
      try {
        const [workflows, runs] = await Promise.all([request(WORKFLOWS), request(RUNS)]);
        state.workflows = workflows.workflows;
        state.runs = runs.runs;
        if (!state.selectedId && state.runs.length) state.selectedId = state.runs[0].id;
        if (state.selectedId) await detail(state.selectedId);
        state.updatedAt = new Date();
        state.readError = '';
      } catch (error) {
        state.readError = error.message;
      } finally {
        state.loading = false;
        refreshing = null;
        emit();
      }
    })();
    return refreshing;
  }
  async function act(kind, workflowName) {
    if (state.busy || !MUTATIONS[kind]) return;
    state.busy = true;
    state.actionError = '';
    emit();
    try {
      // Finish an in-flight poll before applying mutation results to the view.
      if (refreshing) await refreshing;
      const id = state.selectedId;
      const result = await request(MUTATIONS[kind], kind === 'create' ? { workflowName } : { id });
      const run = result[`${kind}Run`];
      state.selectedId = run.id;
      state.run = run;
      state.runs = [run, ...state.runs.filter((r) => r.id !== run.id)];
      state.updatedAt = new Date();
    } catch (error) {
      state.actionError = `${error.message} The action may have been applied. Refresh and inspect the record before trying again.`;
    } finally {
      state.busy = false;
      emit();
    }
  }
  return { state, refresh, select, act };
}

export function escapeHTML(value) {
  return String(value ?? '').replace(
    /[&<>"']/g,
    (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c],
  );
}
const esc = escapeHTML;
const label = (value) =>
  String(value ?? '')
    .toLowerCase()
    .replaceAll('_', ' ')
    .replace(/^./, (c) => c.toUpperCase());
function badge(value) {
  return `<span class="badge ${esc(String(value ?? '').toLowerCase())}">${esc(label(value))}</span>`;
}
function time(value) {
  if (!value) return '—';
  return `<time datetime="${esc(value)}">${esc(new Date(value).toLocaleString())}</time>`;
}
function timestamps(record) {
  return `<div class="timestamps">${['createdAt', 'startedAt', 'finishedAt']
    .filter((key) => key in record)
    .map(
      (key) =>
        `<div><span>${{ createdAt: 'Created', startedAt: 'Started', finishedAt: 'Finished' }[key]}</span>${time(record[key])}</div>`,
    )
    .join('')}</div>`;
}
function errorText(value) {
  return value ? `<p class="error">${esc(value)}</p>` : '';
}
function evidence(name, value) {
  return value ? `<dt>${esc(name)}</dt><dd><code>${esc(value)}</code></dd>` : '';
}
function renderExecution(execution) {
  const m = execution.measurement;
  return `<div class="execution"><div class="run-meta"><strong>Physical execution</strong>${badge(execution.outcome)}</div>
    <dl class="evidence">${evidence('Execution ID', execution.id)}</dl>${timestamps(execution)}${errorText(execution.error)}
    ${execution.retryable == null ? '' : `<p>Instrument permits retry: ${execution.retryable ? 'Yes' : 'No'}</p>`}
    ${
      m
        ? `<div class="data"><strong>Measurement</strong> ${badge(m.state)}${errorText(m.error)}
      <dl class="evidence">${evidence('Data reference', m.dataRef)}${evidence('SHA-256', m.sha256)}</dl></div>`
        : ''
    }</div>`;
}
function renderAttempt(attempt) {
  return `<div class="attempt"><strong>Attempt ${esc(attempt.number)}</strong><dl class="evidence">${evidence('Job ID', attempt.jobId)}</dl>
    <p>Authorized ${time(attempt.authorizedAt)}</p><p>Dispatch deadline ${time(attempt.dispatchDeadline)}</p>
    ${attempt.recoveryDeadline ? `<p>Recovery deadline ${time(attempt.recoveryDeadline)}</p>` : ''}${errorText(attempt.recoveryError)}
    ${attempt.executions?.length ? attempt.executions.map(renderExecution).join('') : '<p>Awaiting physical execution evidence.</p>'}</div>`;
}
export function renderRecord(run) {
  if (!run)
    return '<div class="empty-record"><h2 id="record-title">No record available.</h2><p>Select a run or create one above.</p></div>';
  return `<div class="record-head"><p class="eyebrow">Run record</p><div class="title-row"><h2 id="record-title">${esc(run.workflowName)}</h2>${badge(run.status)}</div>
    <p class="run-id"><code>${esc(run.id)}</code></p>${timestamps(run)}
    ${run.dispatchPaused ? '<div class="notice">Dispatch paused. Execution evidence is unresolved; physical work may still be running.</div>' : ''}
    ${run.cancellationRequested ? '<div class="notice">Cancellation requested. Active physical work may still finish; its outcome will be recorded.</div>' : ''}</div>
    <div class="steps">${run.steps
      .map(
        (
          step,
          i,
        ) => `<article class="step"><div class="step-heading"><h3><span class="step-number">${String(i + 1).padStart(2, '0')}</span>${esc(step.name)}</h3>${badge(step.status)}</div>
      <p class="device">${esc(step.deviceId)}</p>${step.dependsOn?.length ? `<p class="dependency">After ${step.dependsOn.map(esc).join(', ')}</p>` : ''}
      ${timestamps(step)}${errorText(step.error)}${step.dataRef ? `<dl class="evidence">${evidence('Data reference', step.dataRef)}</dl>` : ''}
      <details data-step="${esc(step.name)}"><summary>Attempts &amp; data · ${step.attempts.length}</summary>
      ${step.attempts.length ? step.attempts.map(renderAttempt).join('') : '<p>No attempts authorized.</p>'}</details></article>`,
      )
      .join('')}</div>`;
}

function mount() {
  const el = (id) => document.getElementById(id);
  const notebook = createNotebook({ onChange: render });
  let workflowsSignature = '';
  let recordSignature = '';
  let listSignature = '';
  function render(state) {
    const workflows = JSON.stringify(state.workflows);
    if (workflows !== workflowsSignature) {
      const selected = el('workflow').value;
      el('workflow').innerHTML = state.workflows.length
        ? state.workflows
            .map(
              (w) =>
                `<option value="${esc(w.name)}">${esc(w.name)}${w.isDefault ? ' · default' : ''}</option>`,
            )
            .join('')
        : '<option value="">No workflows available</option>';
      el('workflow').value = state.workflows.some((w) => w.name === selected)
        ? selected
        : (state.workflows.find((w) => w.isDefault) || state.workflows[0])?.name || '';
      workflowsSignature = workflows;
    }
    el('workflow').disabled = state.busy || !state.workflows.length;
    el('create').disabled = state.busy || !state.workflows.length || Boolean(state.readError);
    el('create').firstChild.textContent = state.busy ? 'Working… ' : 'Create run ';
    el('read-error').hidden = !state.readError;
    el('read-error').textContent = state.readError
      ? `Updates interrupted: ${state.readError} Showing the last received record. Polling will resume automatically.`
      : '';
    el('action-error').hidden = !state.actionError;
    el('action-error').textContent = state.actionError;
    el('connection').textContent = state.readError
      ? 'Connection interrupted · record may be out of date'
      : state.updatedAt
        ? `Live · updated ${state.updatedAt.toLocaleTimeString()}`
        : 'Connecting…';
    el('run-count').textContent = state.runs.length;
    const list = JSON.stringify([state.runs, state.selectedId, state.busy]);
    if (list !== listSignature) {
      el('runs').innerHTML = state.runs.length
        ? state.runs
            .map(
              (
                run,
              ) => `<button class="run-item" type="button" data-run="${esc(run.id)}" aria-current="${run.id === state.selectedId}" ${state.busy ? 'disabled' : ''}>
        <strong>${esc(run.workflowName)}</strong><span class="run-meta">${badge(run.status)}<code>${esc(run.id)}</code></span><small>${time(run.createdAt)}</small></button>`,
            )
            .join('')
        : `<p class="empty">${state.loading ? 'Loading run history…' : 'No runs yet. Create your first run above.'}</p>`;
      listSignature = list;
    }
    const record = JSON.stringify([state.selectedId, state.run, state.recordLoading]);
    if (record !== recordSignature) {
      const open = new Set(
        [...el('record').querySelectorAll('details[open]')].map((d) => d.dataset.step),
      );
      const sameRun = el('record').dataset.run === state.selectedId;
      el('record').innerHTML =
        state.recordLoading && !state.run
          ? '<div class="empty-record"><h2 id="record-title">Loading run record…</h2><p>If the record is unavailable, select another run or refresh.</p></div>'
          : renderRecord(state.run);
      el('record').dataset.run = state.selectedId;
      if (sameRun)
        el('record')
          .querySelectorAll('details')
          .forEach((d) => {
            d.open = open.has(d.dataset.step);
          });
      recordSignature = record;
    }
    el('actions').hidden = !['PENDING', 'RUNNING'].includes(state.run?.status);
    const mutable = state.run && !state.busy && !state.readError;
    el('start').hidden = state.run?.status !== 'PENDING';
    el('start').disabled = !mutable || state.run?.cancellationRequested;
    el('cancel').hidden = !['PENDING', 'RUNNING'].includes(state.run?.status);
    el('cancel').disabled = !mutable || state.run?.cancellationRequested;
  }
  async function act(kind, name) {
    await notebook.act(kind, name);
    if (notebook.state.selectedId)
      history.replaceState(null, '', `#${encodeURIComponent(notebook.state.selectedId)}`);
  }
  el('create-form').addEventListener('submit', (event) => {
    event.preventDefault();
    act('create', el('workflow').value);
  });
  el('start').addEventListener('click', () => act('start'));
  el('cancel').addEventListener('click', () => act('cancel'));
  el('refresh').addEventListener('click', () => notebook.refresh());
  el('runs').addEventListener('click', (event) => {
    const button = event.target.closest('[data-run]');
    if (button && !notebook.state.busy) {
      history.replaceState(null, '', `#${encodeURIComponent(button.dataset.run)}`);
      notebook.select(button.dataset.run);
    }
  });
  function selectHash() {
    try {
      notebook.state.selectedId = decodeURIComponent(location.hash.slice(1));
    } catch {
      notebook.state.selectedId = '';
    }
    notebook.refresh();
  }
  window.addEventListener('hashchange', selectHash);
  selectHash();
  let poll;
  function resumePolling() {
    clearInterval(poll);
    poll = setInterval(() => notebook.refresh(), POLL_INTERVAL_MS);
    notebook.refresh();
  }
  resumePolling();
  window.addEventListener('pageshow', resumePolling);
  window.addEventListener('pagehide', () => clearInterval(poll));
}
if (typeof document !== 'undefined') mount();
