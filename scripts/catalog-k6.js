import grpc from 'k6/net/grpc';
import exec from 'k6/execution';
import { Counter } from 'k6/metrics';
import { group } from 'k6';
import { product, products } from './catalog-oracle.mjs';

const rate = Number(__ENV.CATALOG_RATE);
const seconds = Number(__ENV.CATALOG_DURATION_SECONDS);
const vus = Number(__ENV.CATALOG_VUS || 64);
const targets = (__ENV.CATALOG_TARGETS || '').split(',');
if (!Number.isInteger(rate) || rate < 1 || rate > 100000 || !Number.isInteger(seconds) || seconds < 1 || seconds > 600 || !Number.isInteger(vus) || vus < 1 || vus > 1024 || targets.some(target => !/^127\.0\.0\.1:[0-9]+$/.test(target))) {
  throw new Error('bounded rate, duration, VUs and owned loopback targets are required');
}
const fixture = JSON.parse(open(__ENV.CATALOG_FIXTURE));
const expected = products(fixture.products);
if (expected.length < 1 || expected.length > 5000) throw new Error('catalog must have 1..5000 products');
const expectedList = JSON.stringify(expected);
const expectedSearch = JSON.stringify(expected.filter(p => p.name.toLowerCase().includes('benchmark-needle') || p.description.toLowerCase().includes('benchmark-needle')));
const clients = targets.map(() => {
  const client = new grpc.Client();
  client.load([__ENV.CATALOG_PROTO_DIR], 'demo.proto');
  return client;
});
const connected = targets.map(() => false);
const started = new Counter('catalog_started');
const completed = new Counter('catalog_completed');
const succeeded = new Counter('catalog_successful');
const failed = new Counter('catalog_failed');
const correctness = new Counter('catalog_correctness_errors');
const timeouts = new Counter('catalog_timeouts');
const codes = new Counter('catalog_rpc_codes');

export const options = {
  scenarios: { catalog: { executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration: `${seconds}s`, preAllocatedVUs: vus, maxVUs: vus, gracefulStop: '10s' } },
  summaryTrendStats: ['med', 'min', 'max', 'p(95)', 'p(99)', 'count'],
  thresholds: { catalog_correctness_errors: ['count==0'], 'grpc_req_duration{status:0}': ['min>=0'] },
};

export default function () {
  const index = exec.scenario.iterationInTest;
  const replica = index % targets.length;
  const method = index % 10 < 5 ? 'ListProducts' : index % 10 < 9 ? 'GetProduct' : 'SearchProducts';
  const selected = expected[Math.floor(index / 10) % expected.length];
  const request = method === 'GetProduct' ? { id: selected.id } : method === 'SearchProducts' ? { query: 'benchmark-needle' } : {};
  const tags = { method, replica: String(replica) };
  let validating = false;
  started.add(1, tags);
  try {
    if (!connected[replica]) {
      clients[replica].connect(targets[replica], { plaintext: true, timeout: '3s', maxReceiveSize: 8 << 20 });
      connected[replica] = true;
    }
    const response = clients[replica].invoke(`hipstershop.ProductCatalogService/${method}`, request, { timeout: '3s', tags });
    codes.add(1, { ...tags, code: String(response.status) });
    if (response.status !== grpc.StatusOK) {
      failed.add(1, tags);
      if (response.status === grpc.StatusDeadlineExceeded) timeouts.add(1, tags);
      return;
    }
    validating = true;
    const actual = group('catalog-validation', () => method === 'GetProduct' ? JSON.stringify(product(response.message)) : JSON.stringify(products(method === 'ListProducts' ? response.message.products : response.message.results)));
    const want = method === 'GetProduct' ? JSON.stringify(selected) : method === 'ListProducts' ? expectedList : expectedSearch;
    if (actual !== want) {
      correctness.add(1, tags);
      failed.add(1, tags);
      return;
    }
    succeeded.add(1, tags);
  } catch (error) {
    failed.add(1, tags);
    codes.add(1, { ...tags, code: 'EXCEPTION' });
    if (validating) correctness.add(1, tags);
  } finally {
    completed.add(1, tags);
  }
}

export function handleSummary(data) {
  return { [__ENV.CATALOG_SUMMARY_PATH]: JSON.stringify({ native: data, workload: { rate, seconds, vus, targets, products: expected.length, planned_offers: rate * seconds, mix: '50% List, 40% Get, 10% Search among started iterations', latency_policy: 'Native successful-RPC duration, with native iteration_duration retaining connection and validation overhead. Dropped iterations have no latency. No scheduled timestamp is inferred from iteration ordinal. Timeouts count explicit RPC DeadlineExceeded; connection exceptions remain failures.' } }, null, 2) };
}
