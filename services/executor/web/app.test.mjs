import test from 'node:test';
import assert from 'node:assert/strict';
import { createNotebook, graphql, renderRecord } from './app.mjs';

const pending = { id: 'run-1', workflowName: 'Assay', status: 'PENDING', steps: [] };
function fixture(request) {
  const stateChanges = [];
  const notebook = createNotebook({ request, onChange: (s) => stateChanges.push({ ...s }) });
  return { notebook, stateChanges };
}
function reader(query) {
  if (query.includes('workflows')) return { workflows: [{ name: 'Assay', isDefault: true }] };
  if (query.includes('runs {')) return { runs: [pending] };
  return { run: pending };
}

test('loads workflows and records, then creates and starts with separate mutations', async () => {
  const calls = [];
  const { notebook } = fixture(async (q, v) => {
    calls.push([q, v]);
    if (q.includes('mutation Create')) return { createRun: { ...pending, id: 'run-2' } };
    if (q.includes('mutation Start'))
      return { startRun: { ...pending, id: 'run-2', status: 'RUNNING' } };
    return reader(q);
  });
  await notebook.refresh();
  assert.equal(notebook.state.run.id, 'run-1');
  await notebook.act('create', 'Assay');
  assert.equal(notebook.state.selectedId, 'run-2');
  assert.equal(notebook.state.run.status, 'PENDING');
  assert.equal(calls.filter(([q]) => q.includes('mutation')).length, 1);
  await notebook.act('start');
  assert.equal(notebook.state.run.status, 'RUNNING');
  assert.deepEqual(calls.at(-1)[1], { id: 'run-2' });
});

test('poll failure keeps last record and marks it stale; recovery clears stale state', async () => {
  let offline = false;
  const { notebook } = fixture(async (q) => {
    if (offline) throw Error('offline');
    return reader(q);
  });
  await notebook.refresh();
  offline = true;
  await notebook.refresh();
  assert.equal(notebook.state.run.id, 'run-1');
  assert.match(notebook.state.readError, /offline/);
  offline = false;
  await notebook.refresh();
  assert.equal(notebook.state.readError, '');
});

test('does not replay an uncertain create and releases busy state', async () => {
  let mutations = 0;
  const { notebook } = fixture(async (q) => {
    if (q.includes('mutation')) {
      mutations++;
      throw Error('connection lost');
    }
    return reader(q);
  });
  await notebook.refresh();
  await notebook.act('create', 'Assay');
  assert.equal(mutations, 1);
  assert.match(notebook.state.actionError, /may have been applied/);
  assert.equal(notebook.state.busy, false);
});

test('ignores late detail responses when another run is selected', async () => {
  let finish;
  const { notebook } = fixture(async (q, v) => {
    if (v?.id === 'slow')
      return new Promise((resolve) => {
        finish = resolve;
      });
    if (v?.id === 'fast') return { run: { ...pending, id: 'fast' } };
    return reader(q);
  });
  await notebook.refresh();
  const slow = notebook.select('slow');
  const fast = notebook.select('fast');
  await fast;
  finish({ run: { ...pending, id: 'slow' } });
  await slow;
  assert.equal(notebook.state.run.id, 'fast');
});

test('GraphQL transport uses POST variables and rejects HTTP, GraphQL and partial errors', async () => {
  let sent;
  const fetcher = async (url, options) => {
    sent = [url, options];
    return { ok: true, json: async () => ({ data: { run: pending } }) };
  };
  assert.deepEqual(
    await graphql('query Read($id: ID!) { run(id: $id) { id } }', { id: 'run-1' }, fetcher),
    { run: pending },
  );
  assert.equal(sent[0], '/graphql');
  assert.equal(sent[1].method, 'POST');
  assert.deepEqual(JSON.parse(sent[1].body).variables, { id: 'run-1' });
  await assert.rejects(
    graphql('query', {}, async () => ({ ok: false, status: 503 })),
    /503/,
  );
  await assert.rejects(
    graphql('query', {}, async () => ({
      ok: true,
      json: async () => ({ data: { run: pending }, errors: [{ message: 'unavailable' }] }),
    })),
    /unavailable/,
  );
});

test('renders uncertainty, physical attempts and measurement loss without trusting HTML', () => {
  const html = renderRecord({
    ...pending,
    workflowName: '<img onerror=bad>',
    dispatchPaused: true,
    cancellationRequested: true,
    steps: [
      {
        name: 'read',
        deviceId: 'plate-reader-1',
        status: 'FAILED',
        dependsOn: [],
        attempts: [
          {
            number: 1,
            jobId: 'job',
            executions: [
              {
                id: 'physical-1',
                outcome: 'COMPLETED',
                measurement: { state: 'LOST', error: '<script>bad</script>', sha256: 'hash' },
              },
            ],
          },
        ],
      },
    ],
  });
  assert.match(html, /Dispatch paused/);
  assert.match(html, /Cancellation requested/);
  assert.match(html, /physical-1/);
  assert.match(html, /Lost/);
  assert.match(html, /Completed/);
  assert.ok(!html.includes('<img'));
  assert.ok(!html.includes('<script>'));
});

test('cancellation uses selected ID, while repeated clicks cannot send a second mutation', async () => {
  let finish;
  let calls = 0;
  const { notebook } = fixture(async (q, variables) => {
    if (q.includes('mutation Cancel')) {
      calls++;
      assert.equal(variables.id, pending.id);
      return new Promise((resolve) => {
        finish = resolve;
      });
    }
    return reader(q);
  });
  await notebook.refresh();
  const action = notebook.act('cancel');
  await notebook.act('cancel');
  await notebook.refresh();
  assert.equal(calls, 1);
  finish({ cancelRun: { ...pending, cancellationRequested: true } });
  await action;
  assert.equal(notebook.state.run.cancellationRequested, true);
});

test('a missing run completes loading and does not invent a record', async () => {
  const { notebook } = fixture(async (q) =>
    q.includes('query Record') ? { run: null } : reader(q),
  );
  await notebook.select('missing');
  assert.equal(notebook.state.recordLoading, false);
  assert.equal(notebook.state.run, null);
  assert.match(renderRecord(null), /No record available/);
});
